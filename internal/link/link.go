// Package link ties containers, repos and compose stacks together: where a
// container came from, which repo a stack deploys, and what is in the way
// before something is started.
package link

import (
	"path/filepath"
	"strings"

	"github.com/bferg314/dockgit/internal/compose"
	"github.com/bferg314/dockgit/internal/config"
	"github.com/bferg314/dockgit/internal/dock"
	"github.com/bferg314/dockgit/internal/match"
)

// Repo is what the index needs to know about a Repos-tab repo.
type Repo struct {
	Key     string // config key (path as written)
	Name    string
	Project compose.Project
	Remotes []string // git remote URLs
}

// Kind says where a container came from.
type Kind int

const (
	None      Kind = iota // started with docker run, or by something dockgit doesn't know
	FromRepo              // a Repos-tab repo's compose project
	FromStack             // a compose file under a compose root
	Gone                  // a compose file that no longer exists
	Compose               // a compose project dockgit doesn't list
)

// Source is where a container came from.
type Source struct {
	Kind Kind
	Key  string // repo key, or stack file
	Name string // repo name, stack name, missing file or compose project
}

// Label is how a source is shown: "repo clock", "stack downloads".
func (s Source) Label() string {
	switch s.Kind {
	case FromRepo:
		return "repo " + s.Name
	case FromStack:
		return "stack " + s.Name
	case Gone:
		return "gone " + s.Name
	case Compose:
		return "compose " + s.Name
	}
	return ""
}

// Index links containers to the repos and stacks dockgit knows.
type Index struct {
	Repos  []Repo
	Stacks []compose.Stack
}

// SourceOf says where c came from. A stack wins over a repo when both
// claim it (a stack's file is an exact match; a repo matches by folder).
func (ix *Index) SourceOf(c *dock.Container) Source {
	files := c.ConfigFiles()
	for _, s := range ix.Stacks {
		if s.Owns(files) {
			return Source{Kind: FromStack, Key: s.File, Name: s.Name()}
		}
	}
	for _, r := range ix.Repos {
		if r.Project.Owns(files, c.WorkingDir()) {
			return Source{Kind: FromRepo, Key: r.Key, Name: r.Name}
		}
	}
	if p := c.Project(); p != "" {
		for _, f := range files {
			if !exists(f) {
				return Source{Kind: Gone, Key: f, Name: shortPath(f)}
			}
		}
		return Source{Kind: Compose, Name: p}
	}
	return Source{}
}

// RepoForStack finds the repo a stack deploys: the one its x-dockgit repo
// names, or else one whose git remote matches an image the stack runs
// (ghcr.io/bferg314/clock ↔ github.com/bferg314/clock).
func (ix *Index) RepoForStack(s compose.Stack) *Repo {
	if s.Meta.Repo != "" {
		want := compose.NormPath(config.ExpandPath(s.Meta.Repo))
		for i, r := range ix.Repos {
			if compose.NormPath(r.Project.Dir) == want {
				return &ix.Repos[i]
			}
		}
	}
	for _, svc := range s.Services {
		key := imageRepoKey(svc.Image)
		if key == "" {
			continue
		}
		for i, r := range ix.Repos {
			for _, remote := range r.Remotes {
				if repoPath(match.Key(remote)) == key {
					return &ix.Repos[i]
				}
			}
		}
	}
	return nil
}

// StacksForRepo lists the stacks that deploy a repo.
func (ix *Index) StacksForRepo(key string) []compose.Stack {
	var out []compose.Stack
	for _, s := range ix.Stacks {
		if r := ix.RepoForStack(s); r != nil && r.Key == key {
			out = append(out, s)
		}
	}
	return out
}

// imageRepoKey is "owner/repo" for images published from a repo, such as
// ghcr.io/bferg314/clock:1.4.0, or "" for others.
func imageRepoKey(image string) string {
	ref := ImageName(image)
	host, rest, ok := strings.Cut(ref, "/")
	if !ok || (host != "ghcr.io" && host != "docker.io" && !strings.Contains(host, ".")) {
		return ""
	}
	return strings.ToLower(rest)
}

// repoPath is "owner/repo" from match.Key's "host/owner/repo".
func repoPath(key string) string {
	_, rest, _ := strings.Cut(key, "/")
	return rest
}

// ImageName drops the tag and digest from an image reference and spells
// Docker Hub's short forms out: "postgres:17" -> "docker.io/library/postgres".
func ImageName(image string) string {
	image, _, _ = strings.Cut(image, "@")
	if i := strings.LastIndex(image, ":"); i > strings.LastIndex(image, "/") {
		image = image[:i]
	}
	image = strings.ToLower(image)
	first, _, hasSlash := strings.Cut(image, "/")
	switch {
	case !hasSlash:
		return "docker.io/library/" + image
	case !strings.ContainsAny(first, ".:") && first != "localhost":
		return "docker.io/" + image
	}
	return image
}

func shortPath(p string) string {
	p = filepath.ToSlash(p)
	parts := strings.Split(p, "/")
	if len(parts) > 3 {
		parts = parts[len(parts)-3:]
	}
	return strings.Join(parts, "/")
}

// Package doctor checks a compose root against the stack layout dockgit
// recommends (PLAN.md §5a): one compose.yaml per folder, stacks that
// depend on each other in one file, every variable listed in .env.example,
// no clashing host ports. Each finding comes with a one-line fix.
// Nothing here is required: loose layouts still run.
package doctor

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/bferg314/dockgit/internal/compose"
)

// Severity orders findings from most to least serious.
type Severity int

const (
	Problem Severity = iota // breaks something now, or will
	Warning                 // works, but fragile or surprising
	Note                    // a suggestion
)

func (s Severity) Icon() string { return [...]string{"✗", "▲", "·"}[s] }

// Finding is one thing the doctor noticed.
type Finding struct {
	Severity Severity
	Stack    string // the stack's name, "" for the root as a whole
	Msg      string
	Fix      string
}

// Container is what the doctor needs to know about a container.
type Container struct {
	Name        string
	ConfigFiles []string
}

// Check examines the stacks found under root, and the containers started
// from files under it.
func Check(root string, stacks []compose.Stack, containers []Container) []Finding {
	var out []Finding
	add := func(sev Severity, stack, fix, format string, args ...any) {
		out = append(out, Finding{Severity: sev, Stack: stack, Msg: fmt.Sprintf(format, args...), Fix: fix})
	}

	ports := map[string][]string{}      // host port -> stacks publishing it
	fixedNames := map[string][]string{} // container_name -> stacks setting it
	byDir := map[string][]string{}

	for _, s := range stacks {
		name := s.Name()
		if s.Err != nil {
			add(Problem, name, "Fix the YAML; docker compose config shows where.", "%v", s.Err)
			continue
		}
		byDir[s.Dir] = append(byDir[s.Dir], filepath.Base(s.File))

		// Rule 1: one compose.yaml per folder.
		if s.Siblings == 0 && filepath.Base(s.File) != "compose.yaml" {
			add(Note, name, "Rename it to compose.yaml, the name compose looks for first.",
				"is named %s", filepath.Base(s.File))
		}

		// Rule 2: dependencies across files.
		own := map[string]bool{}
		names := map[string]bool{}
		for _, svc := range s.Services {
			own[svc.Name] = true
			if svc.ContainerName != "" {
				names[svc.ContainerName] = true
			}
		}
		for _, svc := range s.Services {
			kind, target, ok := strings.Cut(svc.NetworkMode, ":")
			if !ok || (kind != "service" && kind != "container") {
				continue
			}
			if (kind == "service" && !own[target]) || (kind == "container" && !names[target] && !own[target]) {
				add(Problem, name, "Put both in one stack, so they start (and stop) together.",
					"%s uses network_mode %s from another file: it only works if that one is started first", svc.Name, svc.NetworkMode)
			}
		}

		// Rules 3 and 4: variables and .env.example (the stack's own, or a
		// shared one: the root's .env.example or global.env.example).
		stackExample := envKeys(filepath.Join(s.Dir, ".env.example"))
		listed := map[string]bool{}
		hasExample := false
		for _, f := range s.ExampleFiles() {
			if keys := envKeys(f); keys != nil {
				hasExample = true
				for k := range keys {
					listed[k] = true
				}
			}
		}
		var needed, unlisted []string
		for _, v := range s.Vars {
			if v.HasDefault {
				continue
			}
			needed = append(needed, v.Name)
			if !listed[v.Name] {
				unlisted = append(unlisted, v.Name)
			}
		}
		switch {
		case len(needed) > 0 && !hasExample:
			add(Warning, name, "Add a .env.example listing them, so a fresh clone knows what to set.",
				"uses %s but there's no .env.example", list(needed))
		case len(unlisted) > 0:
			add(Warning, name, "Add them to .env.example (or fix the names if they're typos).",
				"uses %s, not in .env.example", list(unlisted))
		}
		// The stack's own .env is only needed for a required variable its
		// .env.example lists; shared ones come from global.env.
		var ownNeeded []string
		for _, n := range needed {
			if stackExample[n] {
				ownNeeded = append(ownNeeded, n)
			}
		}
		if len(ownNeeded) > 0 && !exists(filepath.Join(s.Dir, ".env")) {
			add(Note, name, "E creates .env from .env.example and opens it.", "has no .env yet (needs %s)", list(ownNeeded))
		}

		// Rule 5: fixed container names.
		var fixed []string
		for _, svc := range s.Services {
			if svc.ContainerName != "" {
				fixed = append(fixed, svc.ContainerName)
				if !slicesContains(fixedNames[svc.ContainerName], name) {
					fixedNames[svc.ContainerName] = append(fixedNames[svc.ContainerName], name)
				}
			}
		}
		if len(fixed) > 0 {
			add(Note, name, "Drop container_name unless something looks a container up by it; compose's own names can't clash.",
				"sets container_name: %s", list(fixed))
		}

		// Rule 7: pinnable image tags.
		var unpinned []string
		for _, svc := range s.Services {
			if svc.Build || svc.Image == "" || strings.Contains(svc.Image, "${") {
				continue
			}
			if tag := imageTag(svc.Image); tag == "" || tag == "latest" {
				unpinned = append(unpinned, imageRepo(svc.Image))
			}
		}
		if len(unpinned) > 0 {
			add(Note, name, "Pin a version (e.g. "+unpinned[0]+":1.2.3); an update bot such as Renovate can keep it current.",
				"no pinned version for %s", list(unpinned))
		}

		for _, svc := range s.Services {
			for _, p := range svc.Ports {
				if !slicesContains(ports[p], name) {
					ports[p] = append(ports[p], name)
				}
			}
		}
	}

	// Rule 1, for folders holding several stacks.
	dirs := sortedKeys(byDir)
	for _, d := range dirs {
		files := byDir[d]
		if len(files) < 2 {
			continue
		}
		rel, _ := filepath.Rel(root, d)
		add(Warning, filepath.ToSlash(rel)+"/", "Give each stack its own folder with one compose.yaml.",
			"%d compose files share this folder, so compose gives them all the project name %q: %s",
			len(files), projectName(d), strings.Join(files, ", "))
	}

	// Rule 6: host ports used twice.
	for _, p := range sortedKeys(ports) {
		if users := ports[p]; len(users) > 1 {
			add(Problem, "", "Move one of them to another host port; only one can be running at a time.",
				"host port %s is published by %s", p, strings.Join(users, " and "))
		}
	}

	// The same fixed container name in several stacks.
	for _, n := range sortedKeys(fixedNames) {
		if users := fixedNames[n]; len(users) > 1 {
			add(Problem, "", "Drop container_name from all but one, or give them different names.",
				"container_name %s is set by %s: only one can exist at a time", n, strings.Join(users, " and "))
		}
	}

	// Containers started from files that are gone (moved in a reshuffle).
	for _, c := range containers {
		for _, f := range c.ConfigFiles {
			if !inside(root, f) || exists(f) {
				continue
			}
			rel, _ := filepath.Rel(root, filepath.FromSlash(f))
			add(Warning, "", "Take it down with docker compose down, then start the stack from its new place.",
				"%s was started from %s, which no longer exists", c.Name, filepath.ToSlash(rel))
		}
	}

	sort.SliceStable(out, func(i, j int) bool { return out[i].Severity < out[j].Severity })
	return out
}

// envKeys returns the keys in an env file, or nil when there's no file.
func envKeys(path string) map[string]bool {
	if !exists(path) {
		return nil
	}
	env, err := compose.ReadEnv(path)
	if err != nil {
		return nil
	}
	keys := map[string]bool{}
	for k := range env {
		keys[k] = true
	}
	return keys
}

func exists(path string) bool {
	_, err := os.Stat(filepath.FromSlash(path))
	return err == nil
}

// inside reports whether path (possibly from a compose label) is under root.
func inside(root, path string) bool {
	r, p := compose.NormPath(root), compose.NormPath(path)
	return strings.HasPrefix(p, r+"/")
}

// projectName is compose's default project name for a folder.
func projectName(dir string) string {
	name := strings.ToLower(filepath.Base(dir))
	var b strings.Builder
	for _, r := range name {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-' || r == '_' {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func imageTag(image string) string {
	if i := strings.LastIndex(image, ":"); i > strings.LastIndex(image, "/") {
		return image[i+1:]
	}
	return ""
}

func imageRepo(image string) string {
	if i := strings.LastIndex(image, ":"); i > strings.LastIndex(image, "/") {
		return image[:i]
	}
	return image
}

func list(names []string) string {
	if len(names) > 4 {
		return strings.Join(names[:4], ", ") + fmt.Sprintf(" and %d more", len(names)-4)
	}
	return strings.Join(names, ", ")
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func slicesContains(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}

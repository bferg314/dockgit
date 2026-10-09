// Package updates checks a stack's images against their registries and
// bumps pinned versions in compose files.
package updates

import (
	"context"
	"fmt"
	"os"
	"regexp"
	"strings"
	"sync"

	"github.com/bferg314/dockgit/internal/compose"
	"github.com/bferg314/dockgit/internal/registry"
)

// Registry is the part of registry.Client the check needs.
type Registry interface {
	Tags(ctx context.Context, ref registry.Ref) ([]string, error)
	Digest(ctx context.Context, ref registry.Ref) (string, error)
}

// LocalDigests returns the digests a local image was pulled as.
type LocalDigests func(ctx context.Context, image string) ([]string, error)

// State is what the check found for one image.
type State int

const (
	UpToDate   State = iota
	NewVersion       // a newer pinned version in the same major (Suggest)
	NewImage         // the tag (latest, java17…) points to a newer image: pull it
	NotPulled        // not on this host yet: up pulls the current one
	Unknown          // the registry couldn't be asked (Err)
)

// Result is one service's image.
type Result struct {
	Service string
	Image   string // as in the compose file
	State   State
	Suggest string // NewVersion: the image with the newer tag
	Major   string // a newer major version, mentioned but not suggested
	Err     error
}

// Check looks at every service's image. Pinned versions are compared
// with the registry's tags; moving tags by digest. Built images and
// images written with variables are skipped.
func Check(ctx context.Context, reg Registry, local LocalDigests, services []compose.Service) []Result {
	var mu sync.Mutex
	var wg sync.WaitGroup
	results := make([]Result, 0, len(services))
	sem := make(chan struct{}, 4)
	for _, svc := range services {
		if svc.Build || svc.Image == "" || strings.Contains(svc.Image, "${") {
			continue
		}
		wg.Add(1)
		go func(svc compose.Service) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			r := checkOne(ctx, reg, local, svc)
			mu.Lock()
			results = append(results, r)
			mu.Unlock()
		}(svc)
	}
	wg.Wait()
	// Keep the compose file's order.
	order := map[string]int{}
	for i, s := range services {
		order[s.Name] = i
	}
	for i := 1; i < len(results); i++ {
		for j := i; j > 0 && order[results[j].Service] < order[results[j-1].Service]; j-- {
			results[j], results[j-1] = results[j-1], results[j]
		}
	}
	return results
}

func checkOne(ctx context.Context, reg Registry, local LocalDigests, svc compose.Service) Result {
	r := Result{Service: svc.Name, Image: svc.Image}
	ref := registry.Parse(svc.Image)
	if registry.Pinned(ref.Tag) {
		tags, err := reg.Tags(ctx, ref)
		if err != nil {
			r.State, r.Err = Unknown, err
			return r
		}
		if t := registry.NewestMajor(ref.Tag, tags); t != "" {
			r.Major = t
		}
		if t := registry.Newest(ref.Tag, tags); t != "" {
			r.State, r.Suggest = NewVersion, withTag(svc.Image, t)
			return r
		}
	}
	have, err := local(ctx, svc.Image)
	if err != nil {
		r.State, r.Err = Unknown, err
		return r
	}
	if len(have) == 0 {
		r.State = NotPulled
		return r
	}
	if registry.Pinned(ref.Tag) {
		return r // a pinned version that's the newest: nothing to pull
	}
	remote, err := reg.Digest(ctx, ref)
	if err != nil {
		r.State, r.Err = Unknown, err
		return r
	}
	for _, d := range have {
		if strings.HasSuffix(d, "@"+remote) {
			return r
		}
	}
	r.State = NewImage
	return r
}

// withTag replaces an image reference's tag.
func withTag(image, tag string) string {
	name, _, _ := strings.Cut(image, "@")
	if i := strings.LastIndex(name, ":"); i > strings.LastIndex(name, "/") {
		name = name[:i]
	}
	return name + ":" + tag
}

// Bump rewrites image lines in a compose file, old image -> new image.
// Only `image:` values change, quoted or not; comments are kept.
func Bump(path string, changes map[string]string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	text := string(data)
	for old, repl := range changes {
		re := regexp.MustCompile(`(?m)^(\s*image:\s*["']?)` + regexp.QuoteMeta(old) + `(["']?\s*(?:#.*)?)$`)
		if !re.MatchString(text) {
			return fmt.Errorf("image %s not found in %s", old, path)
		}
		text = re.ReplaceAllString(text, "${1}"+strings.ReplaceAll(repl, "$", "$$")+"${2}")
	}
	return os.WriteFile(path, []byte(text), 0o644)
}

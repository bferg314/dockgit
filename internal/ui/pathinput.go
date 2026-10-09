package ui

import (
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"

	"github.com/bferg314/dockgit/internal/config"
)

// completePath completes the last part of a folder path, like a shell's
// tab: a single match is filled in with a trailing separator, several are
// filled in as far as they agree. ~ and environment variables are kept as
// typed. It returns the input unchanged when nothing matches.
func completePath(in string) (string, []string) {
	if in == "" {
		return in, nil
	}
	sep := string(filepath.Separator)
	// The folder part as typed, and the partial name after it.
	cut := strings.LastIndexAny(in, `/\`)
	typedDir, partial := "", in
	if cut >= 0 {
		typedDir, partial = in[:cut+1], in[cut+1:]
	}
	dir := "."
	switch {
	case typedDir != "":
		dir = config.ExpandPath(typedDir)
	case partial == "~":
		return "~" + sep, nil
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		return in, nil
	}
	fold := runtime.GOOS == "windows" || runtime.GOOS == "darwin"
	var matches []string
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		n := e.Name()
		if strings.HasPrefix(n, ".") && !strings.HasPrefix(partial, ".") {
			continue // hidden folders only when asked for
		}
		if hasPrefix(n, partial, fold) {
			matches = append(matches, n)
		}
	}
	sort.Strings(matches)
	switch len(matches) {
	case 0:
		return in, nil
	case 1:
		return typedDir + matches[0] + sep, nil
	}
	return typedDir + commonPrefix(matches, fold), matches
}

func hasPrefix(s, prefix string, fold bool) bool {
	if fold {
		return strings.HasPrefix(strings.ToLower(s), strings.ToLower(prefix))
	}
	return strings.HasPrefix(s, prefix)
}

func commonPrefix(names []string, fold bool) string {
	p := names[0]
	for _, n := range names[1:] {
		i := 0
		for i < len(p) && i < len(n) {
			a, b := p[i], n[i]
			if fold {
				a, b = lower(a), lower(b)
			}
			if a != b {
				break
			}
			i++
		}
		p = p[:i]
	}
	return p
}

func lower(b byte) byte {
	if 'A' <= b && b <= 'Z' {
		return b + 'a' - 'A'
	}
	return b
}

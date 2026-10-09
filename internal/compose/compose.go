// Package compose finds compose files and builds docker compose command
// lines. Running them is the caller's job.
package compose

import (
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
)

// defaultNames are the files compose reads when no -f is given, in the
// order it looks for them.
var defaultNames = []string{"compose.yaml", "compose.yml", "docker-compose.yaml", "docker-compose.yml"}

// variant matches compose.<x>.yml and docker-compose.<x>.yaml, such as
// docker-compose.build.yml or compose.override.yaml.
var variant = regexp.MustCompile(`^(docker-)?compose\.[^.]+\.ya?ml$`)

// Discover lists the compose files directly in dir: compose's default
// names first, in its order, then variants by name.
func Discover(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	present := map[string]bool{}
	var variants []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := strings.ToLower(e.Name())
		present[name] = true
		if variant.MatchString(name) {
			variants = append(variants, e.Name())
		}
	}
	var files []string
	for _, n := range defaultNames {
		if present[n] {
			files = append(files, n)
		}
	}
	sort.Strings(variants)
	return append(files, variants...), nil
}

// Pick chooses the files to use when the user hasn't: compose's default
// file if there is one, and nothing (ask) when only variants exist.
func Pick(found []string) []string {
	for _, f := range found {
		for _, n := range defaultNames {
			if strings.EqualFold(f, n) {
				return []string{f}
			}
		}
	}
	return nil
}

// Project is how one compose project is run.
type Project struct {
	Dir      string   // where compose runs; files are relative to it
	Files    []string // -f files; empty means compose's default
	Name     string   // -p; empty means compose's default
	EnvFiles []string // --env-file each, later ones win; empty means .env beside the file
}

// Args is the docker command line for a compose subcommand, such as
// Args("up", "-d", "--build"). Output is plain text, for showing as it
// streams.
func (p Project) Args(sub ...string) []string {
	args := []string{"compose"}
	for _, f := range p.Files {
		args = append(args, "-f", f)
	}
	if p.Name != "" {
		args = append(args, "-p", p.Name)
	}
	for _, f := range p.EnvFiles {
		args = append(args, "--env-file", f)
	}
	args = append(args, "--progress", "plain")
	return append(args, sub...)
}

// FilePaths are the absolute paths of the project's files, or of the
// default files present when none are set.
func (p Project) FilePaths() []string {
	files := p.Files
	if len(files) == 0 {
		found, _ := Discover(p.Dir)
		files = Pick(found)
	}
	paths := make([]string, len(files))
	for i, f := range files {
		if filepath.IsAbs(f) {
			paths[i] = f
		} else {
			paths[i] = filepath.Join(p.Dir, f)
		}
	}
	return paths
}

// Owns reports whether a container with these compose labels was started
// from this project: one of its config files is in the project's folder,
// or its working directory is that folder.
func (p Project) Owns(configFiles []string, workingDir string) bool {
	dir := NormPath(p.Dir)
	if workingDir != "" && NormPath(workingDir) == dir {
		return true
	}
	for _, f := range configFiles {
		if NormPath(filepath.Dir(normSlashes(f))) == dir {
			return true
		}
	}
	return false
}

// hostPath matches the forms a Windows path takes in compose labels when
// compose ran somewhere other than Windows itself: WSL (/mnt/c/...) and
// Docker Desktop's VM (/run/desktop/mnt/host/c/...).
var hostPath = regexp.MustCompile(`^(?:/run/desktop/mnt/host|/mnt|/host_mnt)/([a-zA-Z])(/.*)?$`)

// NormPath makes paths from compose labels and from this machine
// comparable: forward slashes, no trailing slash, drive letters for the
// WSL and Docker Desktop forms, and lower case on Windows and macOS, whose
// file systems ignore case.
func NormPath(p string) string {
	p = normSlashes(strings.TrimSpace(p))
	if m := hostPath.FindStringSubmatch(p); m != nil {
		p = m[1] + ":" + m[2]
	}
	p = strings.TrimRight(p, "/")
	if len(p) == 2 && p[1] == ':' {
		p += "/"
	}
	if runtime.GOOS == "windows" || runtime.GOOS == "darwin" {
		p = strings.ToLower(p)
	}
	return p
}

func normSlashes(p string) string { return strings.ReplaceAll(p, `\`, "/") }

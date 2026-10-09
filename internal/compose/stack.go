package compose

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Stack is one compose file found under a compose root.
type Stack struct {
	Root string // the compose root it was found in
	Dir  string // the folder holding the file
	File string // full path
	Rel  string // File relative to Root, with forward slashes

	Services []Service
	Meta     Meta  // the file's x-dockgit block
	Vars     []Var // variables the file uses, in order of first use
	Err      error // set when the file can't be read or parsed
	ModTime  time.Time

	// Siblings is how many other compose files share Dir. Loose layouts
	// put several in one folder; the stack layout has one.
	Siblings int
}

// Service is what dockgit needs to know about one service.
type Service struct {
	Name          string
	Image         string
	Build         bool
	ContainerName string
	NetworkMode   string   // "service:gluetun", "host", ...
	Ports         []string // host side: "8080", "127.0.0.1:5432", "21527/udp"
}

// Meta is the optional x-dockgit block in a compose file.
type Meta struct {
	Description     string   `yaml:"description"`
	Repo            string   `yaml:"repo"`
	DependsOnStacks []string `yaml:"depends_on_stacks"`
	Autostart       bool     `yaml:"autostart"`
}

// Var is a variable a compose file uses.
type Var struct {
	Name       string
	HasDefault bool // ${X:-default} or ${X-default}
	Required   bool // ${X:?message}: compose refuses to start without it
}

// Name is how the stack is listed: its folder for a lone compose file in
// the stack layout, its file otherwise.
func (s Stack) Name() string {
	if s.Siblings == 0 && isDefaultName(filepath.Base(s.File)) {
		return strings.TrimSuffix(s.Rel, "/"+filepath.Base(s.File))
	}
	return s.Rel
}

// Project is how dockgit runs a stack: from its folder, with its own file,
// and the root's .env layered under the stack's when they exist.
func (s Stack) Project() Project {
	p := Project{Dir: s.Dir, Files: []string{filepath.Base(s.File)}}
	for _, f := range s.EnvFiles() {
		if _, err := os.Stat(f); err == nil {
			p.EnvFiles = append(p.EnvFiles, f)
		}
	}
	return p
}

// EnvFiles are the env files that apply to the stack, whether or not they
// exist yet, later ones winning: the root's shared .env, global.env in the
// folder holding the stack's folder (dockge's shared file, so
// stacks/global.env for stacks/<name>), then the stack's own .env.
func (s Stack) EnvFiles() []string {
	var files []string
	seen := map[string]bool{}
	add := func(f string) {
		if k := NormPath(f); !seen[k] {
			seen[k] = true
			files = append(files, f)
		}
	}
	if NormPath(s.Dir) != NormPath(s.Root) {
		add(filepath.Join(s.Root, ".env"))
		add(filepath.Join(filepath.Dir(s.Dir), "global.env"))
	}
	add(filepath.Join(s.Dir, ".env"))
	return files
}

// ExampleFiles are the .env.example files matching EnvFiles: each env file
// with ".example" added (global.env.example for global.env).
func (s Stack) ExampleFiles() []string {
	envs := s.EnvFiles()
	out := make([]string, len(envs))
	for i, f := range envs {
		out[i] = f + ".example"
	}
	return out
}

// Owns reports whether a container was started from this file.
func (s Stack) Owns(configFiles []string) bool {
	want := NormPath(s.File)
	for _, f := range configFiles {
		if NormPath(f) == want {
			return true
		}
	}
	return false
}

func isDefaultName(name string) bool {
	for _, n := range defaultNames {
		if strings.EqualFold(name, n) {
			return true
		}
	}
	return false
}

// skipDirs are never searched for compose files.
var skipDirs = map[string]bool{".git": true, "node_modules": true, "vendor": true, ".venv": true}

// FindStacks lists every compose file under root: YAML files with a
// top-level services key, up to depth folders down.
func FindStacks(root string, depth int) ([]Stack, error) {
	root = filepath.Clean(root)
	if fi, err := os.Stat(root); err != nil || !fi.IsDir() {
		return nil, fmt.Errorf("%s is not a folder", root)
	}
	var stacks []Stack
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil // unreadable folders are skipped
		}
		if d.IsDir() {
			rel, _ := filepath.Rel(root, path)
			if path != root && (skipDirs[d.Name()] || strings.HasPrefix(d.Name(), ".") || strings.Count(rel, string(filepath.Separator)) >= depth) {
				return filepath.SkipDir
			}
			return nil
		}
		ext := strings.ToLower(filepath.Ext(path))
		if ext != ".yml" && ext != ".yaml" {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil || !hasServices(data) {
			return nil
		}
		s := parseStack(root, path, data)
		if fi, err := d.Info(); err == nil {
			s.ModTime = fi.ModTime()
		}
		stacks = append(stacks, s)
		return nil
	})
	sort.Slice(stacks, func(i, j int) bool { return stacks[i].Rel < stacks[j].Rel })
	perDir := map[string]int{}
	for _, s := range stacks {
		perDir[s.Dir]++
	}
	for i := range stacks {
		stacks[i].Siblings = perDir[stacks[i].Dir] - 1
	}
	return stacks, err
}

var servicesKey = regexp.MustCompile(`(?m)^services:`)

func hasServices(data []byte) bool { return servicesKey.Match(data) }

type fileYAML struct {
	Services map[string]struct {
		Image         string      `yaml:"image"`
		Build         interface{} `yaml:"build"`
		ContainerName string      `yaml:"container_name"`
		NetworkMode   string      `yaml:"network_mode"`
		Ports         []yaml.Node `yaml:"ports"`
	} `yaml:"services"`
	Meta Meta `yaml:"x-dockgit"`
}

// ParseFiles reads compose files that are used together (-f a -f b), such
// as a repo's, and returns their services and variables combined.
// Services in later files replace same-named ones in earlier files.
func ParseFiles(paths []string) ([]Service, []Var, error) {
	var services []Service
	var vars []Var
	index := map[string]int{}
	seenVar := map[string]bool{}
	for _, p := range paths {
		data, err := os.ReadFile(p)
		if err != nil {
			return nil, nil, err
		}
		s := parseStack(filepath.Dir(p), p, data)
		if s.Err != nil {
			return nil, nil, s.Err
		}
		for _, svc := range s.Services {
			if i, ok := index[svc.Name]; ok {
				services[i] = svc
				continue
			}
			index[svc.Name] = len(services)
			services = append(services, svc)
		}
		for _, v := range s.Vars {
			if !seenVar[v.Name] {
				seenVar[v.Name] = true
				vars = append(vars, v)
			}
		}
	}
	return services, vars, nil
}

func parseStack(root, path string, data []byte) Stack {
	rel, _ := filepath.Rel(root, path)
	s := Stack{Root: root, Dir: filepath.Dir(path), File: path, Rel: filepath.ToSlash(rel), Vars: findVars(string(data))}
	var f fileYAML
	if err := yaml.Unmarshal(data, &f); err != nil {
		s.Err = fmt.Errorf("can't read %s: %w", filepath.Base(path), err)
		return s
	}
	s.Meta = f.Meta
	for name, svc := range f.Services {
		out := Service{Name: name, Image: svc.Image, Build: svc.Build != nil, ContainerName: svc.ContainerName, NetworkMode: svc.NetworkMode}
		for _, p := range svc.Ports {
			if hp := hostPort(p); hp != "" {
				out.Ports = append(out.Ports, hp)
			}
		}
		s.Services = append(s.Services, out)
	}
	sort.Slice(s.Services, func(i, j int) bool { return s.Services[i].Name < s.Services[j].Name })
	return s
}

// hostPort is the host side of a port mapping, or "" when the port isn't
// published on the host. Short syntax: "8080:80", "127.0.0.1:5432:5432",
// "8080:80/udp", "80" (no host port). Long syntax: {published, target,
// host_ip, protocol}. Variables with defaults resolve to the default.
func hostPort(n yaml.Node) string {
	if n.Kind == yaml.MappingNode {
		var long struct {
			Published string `yaml:"published"`
			HostIP    string `yaml:"host_ip"`
			Protocol  string `yaml:"protocol"`
		}
		if n.Decode(&long) != nil || long.Published == "" {
			return ""
		}
		return portString(long.HostIP, resolveDefaults(long.Published), long.Protocol)
	}
	spec := resolveDefaults(n.Value)
	proto := ""
	if i := strings.LastIndex(spec, "/"); i >= 0 {
		spec, proto = spec[:i], spec[i+1:]
	}
	parts := strings.Split(spec, ":")
	switch len(parts) {
	case 2:
		return portString("", parts[0], proto)
	case 3:
		return portString(parts[0], parts[1], proto)
	}
	return ""
}

func portString(ip, port, proto string) string {
	if port == "" {
		return ""
	}
	if ip != "" && ip != "0.0.0.0" && ip != "::" {
		port = ip + ":" + port
	}
	if proto != "" && proto != "tcp" {
		port += "/" + proto
	}
	return port
}

var defaultedVar = regexp.MustCompile(`\$\{[A-Za-z_][A-Za-z0-9_]*:?-([^}]*)\}`)

// resolveDefaults replaces ${X:-d} with d, for reading port numbers.
func resolveDefaults(s string) string { return defaultedVar.ReplaceAllString(s, "$1") }

var varRef = regexp.MustCompile(`\$\$|\$\{([A-Za-z_][A-Za-z0-9_]*)(:?[-?+])?[^}]*\}|\$([A-Za-z_][A-Za-z0-9_]*)`)

// findVars lists the variables used in a compose file, ignoring comments
// and escaped $$.
func findVars(text string) []Var {
	var vars []Var
	seen := map[string]int{}
	for _, line := range strings.Split(text, "\n") {
		line = stripComment(line)
		for _, m := range varRef.FindAllStringSubmatch(line, -1) {
			if m[0] == "$$" {
				continue
			}
			name, op := m[1], m[2]
			if name == "" {
				name = m[3]
			}
			v := Var{Name: name, HasDefault: strings.Contains(op, "-"), Required: strings.Contains(op, "?")}
			if i, ok := seen[name]; ok {
				// A plain use anywhere makes it needed.
				vars[i].HasDefault = vars[i].HasDefault && v.HasDefault
				vars[i].Required = vars[i].Required || v.Required
				continue
			}
			seen[name] = len(vars)
			vars = append(vars, v)
		}
	}
	return vars
}

// stripComment drops a YAML comment: a # at the start or after a space,
// outside quotes.
func stripComment(line string) string {
	inS, inD := false, false
	for i, r := range line {
		switch r {
		case '\'':
			if !inD {
				inS = !inS
			}
		case '"':
			if !inS {
				inD = !inD
			}
		case '#':
			if !inS && !inD && (i == 0 || line[i-1] == ' ' || line[i-1] == '\t') {
				return line[:i]
			}
		}
	}
	return line
}

// ReadEnv reads KEY=VALUE lines from an env file: comments, blank lines
// and "export " prefixes are allowed. A missing file is an empty map.
func ReadEnv(path string) (map[string]string, error) {
	env := map[string]string{}
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return env, nil
	}
	if err != nil {
		return nil, err
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(strings.TrimRight(line, "\r"))
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimPrefix(line, "export ")
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		v = strings.TrimSpace(v)
		if uq, err := strconv.Unquote(v); err == nil {
			v = uq
		} else if len(v) >= 2 && v[0] == '\'' && v[len(v)-1] == '\'' {
			v = v[1 : len(v)-1]
		}
		env[strings.TrimSpace(k)] = v
	}
	return env, nil
}

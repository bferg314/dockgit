package link

import (
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/bferg314/dockgit/internal/compose"
	"github.com/bferg314/dockgit/internal/dock"
)

// Problem kinds found before starting something.
const (
	PortTaken   = "port"
	NameTaken   = "name"
	AlreadyRuns = "duplicate"
	Unset       = "unset"
	NeedsOther  = "dependency"
)

// Problem is one thing in the way of starting a stack or repo.
type Problem struct {
	Kind string
	Msg  string
	// Holders are the containers in the way: running ones hold a port;
	// any with the name block it, running or not.
	Holders []*dock.Container
	// Remove says the holders must be removed (a name is taken even by a
	// stopped container); otherwise stopping them is enough.
	Remove bool
}

// Target is what's about to be started.
type Target struct {
	Name     string
	Services []compose.Service
	Missing  []string // variables with no value
	// Owns reports whether a container already belongs to the target; its
	// own containers are recreated by compose, so they're never in the way.
	Owns func(*dock.Container) bool
}

// Preflight lists what would stop t from starting cleanly, given every
// container on the host.
func Preflight(t Target, containers []*dock.Container) []Problem {
	var out []Problem
	others := make([]*dock.Container, 0, len(containers))
	for _, c := range containers {
		if t.Owns == nil || !t.Owns(c) {
			others = append(others, c)
		}
	}
	sortByName(others)

	for _, svc := range t.Services {
		// Host ports held by another running container.
		for _, want := range svc.Ports {
			var holders []*dock.Container
			for _, c := range others {
				if c.Running() && holdsPort(c, want) {
					holders = append(holders, c)
				}
			}
			if len(holders) > 0 {
				out = append(out, Problem{Kind: PortTaken, Holders: holders,
					Msg: fmt.Sprintf("port %s (%s) is held by %s", want, svc.Name, names(holders))})
			}
		}

		// A fixed name another container already has.
		if svc.ContainerName != "" {
			for _, c := range others {
				if c.Name == svc.ContainerName {
					out = append(out, Problem{Kind: NameTaken, Holders: []*dock.Container{c}, Remove: true,
						Msg: fmt.Sprintf("the name %s (%s) is taken by a container from %s", c.Name, svc.Name, origin(c))})
				}
			}
		}

		// The same service (same image, same name) already running from
		// somewhere else: an older deployment of the same thing.
		if svc.Image != "" && !svc.Build {
			want := ImageName(svc.Image)
			var dups []*dock.Container
			for _, c := range others {
				if c.Running() && ImageName(c.Image) == want && (c.Service() == svc.Name || c.Name == svc.Name) {
					dups = append(dups, c)
				}
			}
			if len(dups) > 0 {
				out = append(out, Problem{Kind: AlreadyRuns, Holders: dups, Remove: true,
					Msg: fmt.Sprintf("%s already runs %s, started from %s", names(dups), shortImage(svc.Image), origin(dups[0]))})
			}
		}

		// network_mode pointing at a container outside the target.
		if kind, other, ok := strings.Cut(svc.NetworkMode, ":"); ok && (kind == "container" || (kind == "service" && !hasService(t.Services, other))) {
			if !runningNamed(containers, other) {
				out = append(out, Problem{Kind: NeedsOther,
					Msg: fmt.Sprintf("%s uses network_mode %s, which isn't running", svc.Name, svc.NetworkMode)})
			}
		}
	}

	if len(t.Missing) > 0 {
		out = append(out, Problem{Kind: Unset, Msg: "not set: " + strings.Join(t.Missing, ", ") + " (they'd be empty)"})
	}
	return dedupe(out)
}

// holdsPort reports whether c publishes want ("8080", "127.0.0.1:5432",
// "53/udp") on the host. Two bindings clash unless both name different
// specific addresses.
func holdsPort(c *dock.Container, want string) bool {
	wantIP, wantPort, wantProto := splitPort(want)
	for _, p := range c.PublishedPorts() {
		proto := "tcp"
		if _, pr, ok := strings.Cut(p.Container, "/"); ok {
			proto = pr
		}
		if p.HostPort != wantPort || proto != wantProto {
			continue
		}
		if wantIP == "" || p.HostIP == "" || wantIP == p.HostIP {
			return true
		}
	}
	return false
}

func splitPort(s string) (ip, port, proto string) {
	proto = "tcp"
	if a, b, ok := strings.Cut(s, "/"); ok {
		s, proto = a, b
	}
	if i := strings.LastIndex(s, ":"); i >= 0 {
		ip, s = s[:i], s[i+1:]
	}
	if ip == "0.0.0.0" || ip == "::" {
		ip = ""
	}
	return ip, s, proto
}

func hasService(svcs []compose.Service, name string) bool {
	for _, s := range svcs {
		if s.Name == name {
			return true
		}
	}
	return false
}

func runningNamed(cs []*dock.Container, name string) bool {
	for _, c := range cs {
		if c.Running() && (c.Name == name || c.Service() == name) {
			return true
		}
	}
	return false
}

// origin describes where a container was started from, for messages.
func origin(c *dock.Container) string {
	if files := c.ConfigFiles(); len(files) > 0 {
		return shortPath(files[0])
	}
	if p := c.Project(); p != "" {
		return "compose project " + p
	}
	return "docker run"
}

func names(cs []*dock.Container) string {
	n := make([]string, len(cs))
	for i, c := range cs {
		n[i] = c.Name
	}
	return strings.Join(n, ", ")
}

func shortImage(image string) string {
	if i := strings.LastIndex(image, "/"); i >= 0 {
		return image[i+1:]
	}
	return image
}

func sortByName(cs []*dock.Container) {
	sort.Slice(cs, func(i, j int) bool { return cs[i].Name < cs[j].Name })
}

// dedupe drops repeats (two services can run the same image).
func dedupe(ps []Problem) []Problem {
	seen := map[string]bool{}
	var out []Problem
	for _, p := range ps {
		if !seen[p.Msg] {
			seen[p.Msg] = true
			out = append(out, p)
		}
	}
	return out
}

// exists checks a path from a compose label, in any of the forms
// NormPath understands (Windows, WSL, Docker Desktop).
func exists(path string) bool {
	if _, err := os.Stat(path); err == nil {
		return true
	}
	_, err := os.Stat(compose.NormPath(path))
	return err == nil
}

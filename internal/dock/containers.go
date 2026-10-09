package dock

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Compose labels on containers it starts.
const (
	LabelProject     = "com.docker.compose.project"
	LabelService     = "com.docker.compose.service"
	LabelWorkingDir  = "com.docker.compose.project.working_dir"
	LabelConfigFiles = "com.docker.compose.project.config_files"
	LabelOneOff      = "com.docker.compose.oneoff"
)

// Container states as docker reports them.
const (
	StateRunning    = "running"
	StateExited     = "exited"
	StateCreated    = "created"
	StatePaused     = "paused"
	StateRestarting = "restarting"
	StateDead       = "dead"
	StateRemoving   = "removing"
)

// Container is what dockgit knows about one container, from docker inspect.
type Container struct {
	ID      string
	Name    string
	Image   string // as configured, e.g. "postgres:17-alpine"
	ImageID string
	Command string

	State    string
	Health   string // "", "starting", "healthy", "unhealthy"
	ExitCode int
	Error    string
	OOM      bool

	Created, StartedAt, FinishedAt time.Time
	RestartCount                   int

	Labels   map[string]string
	Env      []string
	Ports    []Port
	Mounts   []Mount
	Networks []string
}

// Port is one container port, published or only exposed.
type Port struct {
	Container string // "3000/tcp"
	HostIP    string // "" when not published
	HostPort  string
}

// Published reports whether the port is bound on the host.
func (p Port) Published() bool { return p.HostPort != "" }

// Mount is a volume or bind mount.
type Mount struct {
	Type        string // "volume", "bind", "tmpfs"
	Name        string // volume name
	Source      string
	Destination string
	RW          bool
}

func (c *Container) Project() string    { return c.Labels[LabelProject] }
func (c *Container) Service() string    { return c.Labels[LabelService] }
func (c *Container) WorkingDir() string { return c.Labels[LabelWorkingDir] }
func (c *Container) Running() bool      { return c.State == StateRunning }
func (c *Container) ShortID() string    { return shortID(c.ID) }
func (c *Container) ConfigFiles() []string {
	v := c.Labels[LabelConfigFiles]
	if v == "" {
		return nil
	}
	return strings.Split(v, ",")
}

// PublishedPorts lists host bindings once each: docker reports IPv4 and
// IPv6 bindings of the same port separately.
func (c *Container) PublishedPorts() []Port {
	var out []Port
	seen := map[string]bool{}
	for _, p := range c.Ports {
		if !p.Published() {
			continue
		}
		ip := p.HostIP
		if ip == "0.0.0.0" || ip == "::" {
			ip = ""
		}
		key := ip + ":" + p.HostPort + ">" + p.Container
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, Port{Container: p.Container, HostIP: ip, HostPort: p.HostPort})
	}
	return out
}

// ShortImageID is an image ID as docker prints it: 12 hex digits, no
// "sha256:" prefix.
func ShortImageID(id string) string { return shortID(id) }

func shortID(id string) string {
	id = strings.TrimPrefix(id, "sha256:")
	if len(id) > 12 {
		return id[:12]
	}
	return id
}

// List returns every container, running or not.
func List(ctx context.Context, r Runner) ([]*Container, error) {
	out, err := r.Run(ctx, "", "ps", "-aq", "--no-trunc")
	if err != nil {
		return nil, err
	}
	ids := strings.Fields(string(out))
	if len(ids) == 0 {
		return nil, nil
	}
	cs, _, err := Inspect(ctx, r, ids...)
	return cs, err
}

// Inspect returns the containers among ids that still exist, and the ids
// that don't (removed since they were listed).
func Inspect(ctx context.Context, r Runner, ids ...string) ([]*Container, []string, error) {
	if len(ids) == 0 {
		return nil, nil, nil
	}
	out, runErr := r.Run(ctx, "", append([]string{"inspect", "--type", "container"}, ids...)...)
	// docker inspect prints the containers it found even when others are
	// missing, then fails, so parse first and judge the error after.
	cs, err := parseInspect(out)
	if err != nil {
		if runErr != nil {
			return nil, nil, runErr
		}
		return nil, nil, err
	}
	found := map[string]bool{}
	for _, c := range cs {
		found[c.ID] = true
	}
	var gone []string
	for _, id := range ids {
		if !found[id] && !foundPrefix(found, id) {
			gone = append(gone, id)
		}
	}
	if runErr != nil && len(gone) == 0 {
		return nil, nil, runErr
	}
	return cs, gone, nil
}

// foundPrefix matches a short ID against the full IDs found.
func foundPrefix(found map[string]bool, id string) bool {
	for f := range found {
		if strings.HasPrefix(f, id) {
			return true
		}
	}
	return false
}

type inspectJSON struct {
	ID      string    `json:"Id"`
	Created time.Time `json:"Created"`
	Name    string    `json:"Name"`
	Path    string    `json:"Path"`
	Args    []string  `json:"Args"`
	State   struct {
		Status     string    `json:"Status"`
		OOMKilled  bool      `json:"OOMKilled"`
		ExitCode   int       `json:"ExitCode"`
		Error      string    `json:"Error"`
		StartedAt  time.Time `json:"StartedAt"`
		FinishedAt time.Time `json:"FinishedAt"`
		Health     *struct {
			Status string `json:"Status"`
		} `json:"Health"`
	} `json:"State"`
	Image        string `json:"Image"`
	RestartCount int    `json:"RestartCount"`
	Mounts       []struct {
		Type        string `json:"Type"`
		Name        string `json:"Name"`
		Source      string `json:"Source"`
		Destination string `json:"Destination"`
		RW          bool   `json:"RW"`
	} `json:"Mounts"`
	Config struct {
		Image        string              `json:"Image"`
		Labels       map[string]string   `json:"Labels"`
		Env          []string            `json:"Env"`
		ExposedPorts map[string]struct{} `json:"ExposedPorts"`
	} `json:"Config"`
	NetworkSettings struct {
		Ports map[string][]struct {
			HostIP   string `json:"HostIp"`
			HostPort string `json:"HostPort"`
		} `json:"Ports"`
		Networks map[string]json.RawMessage `json:"Networks"`
	} `json:"NetworkSettings"`
}

func parseInspect(data []byte) ([]*Container, error) {
	data = bytes.TrimSpace(data)
	if len(data) == 0 {
		return nil, fmt.Errorf("docker inspect printed nothing")
	}
	var raw []inspectJSON
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("reading docker inspect: %w", err)
	}
	cs := make([]*Container, 0, len(raw))
	for _, j := range raw {
		c := &Container{
			ID:           j.ID,
			Name:         strings.TrimPrefix(j.Name, "/"),
			Image:        j.Config.Image,
			ImageID:      j.Image,
			Command:      strings.TrimSpace(j.Path + " " + strings.Join(j.Args, " ")),
			State:        j.State.Status,
			ExitCode:     j.State.ExitCode,
			Error:        j.State.Error,
			OOM:          j.State.OOMKilled,
			Created:      j.Created,
			StartedAt:    j.State.StartedAt,
			FinishedAt:   j.State.FinishedAt,
			RestartCount: j.RestartCount,
			Labels:       j.Config.Labels,
			Env:          j.Config.Env,
		}
		if c.Labels == nil {
			c.Labels = map[string]string{}
		}
		if j.State.Health != nil {
			c.Health = j.State.Health.Status
		}
		for _, m := range j.Mounts {
			c.Mounts = append(c.Mounts, Mount{Type: m.Type, Name: m.Name, Source: m.Source, Destination: m.Destination, RW: m.RW})
		}
		for n := range j.NetworkSettings.Networks {
			c.Networks = append(c.Networks, n)
		}
		sort.Strings(c.Networks)

		// Published ports come from NetworkSettings; ports that are only
		// exposed appear in the config (and with no bindings there).
		ports := map[string]bool{}
		for p, binds := range j.NetworkSettings.Ports {
			ports[p] = true
			for _, b := range binds {
				c.Ports = append(c.Ports, Port{Container: p, HostIP: b.HostIP, HostPort: b.HostPort})
			}
			if len(binds) == 0 {
				c.Ports = append(c.Ports, Port{Container: p})
			}
		}
		for p := range j.Config.ExposedPorts {
			if !ports[p] {
				c.Ports = append(c.Ports, Port{Container: p})
			}
		}
		sortPorts(c.Ports)
		cs = append(cs, c)
	}
	return cs, nil
}

// sortPorts orders by container port number, then host port.
func sortPorts(ps []Port) {
	num := func(s string) int {
		n, _ := strconv.Atoi(strings.SplitN(s, "/", 2)[0])
		return n
	}
	sort.SliceStable(ps, func(i, j int) bool {
		a, b := ps[i], ps[j]
		if na, nb := num(a.Container), num(b.Container); na != nb {
			return na < nb
		}
		if a.HostPort != b.HostPort {
			return a.HostPort < b.HostPort
		}
		return a.HostIP < b.HostIP
	})
}

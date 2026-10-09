package dock

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// Usage is one line of `docker system df`: images, containers, local
// volumes or build cache.
type Usage struct {
	Type        string // "Images", "Containers", "Local Volumes", "Build Cache"
	Total       int
	Active      int
	Size        string // "7.705GB"
	Reclaimable string // "6.796GB (88%)"
}

type usageJSON struct {
	Type        string `json:"Type"`
	TotalCount  string `json:"TotalCount"`
	Active      string `json:"Active"`
	Size        string `json:"Size"`
	Reclaimable string `json:"Reclaimable"`
}

// DiskUsage summarizes what Docker stores.
func DiskUsage(ctx context.Context, r Runner) ([]Usage, error) {
	out, err := r.Run(ctx, "", "system", "df", "--format", "json")
	if err != nil {
		return nil, err
	}
	var us []Usage
	err = eachJSONLine(out, func(line []byte) error {
		var j usageJSON
		if err := json.Unmarshal(line, &j); err != nil {
			return err
		}
		total, _ := strconv.Atoi(j.TotalCount)
		active, _ := strconv.Atoi(j.Active)
		us = append(us, Usage{Type: j.Type, Total: total, Active: active, Size: j.Size, Reclaimable: j.Reclaimable})
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("reading docker system df: %w", err)
	}
	return us, nil
}

// PruneBuildCache removes build cache, keeping up to reserved (e.g.
// "10GB") and anything newer than until (e.g. "168h"); empty means no
// limit. It returns the space freed.
func PruneBuildCache(ctx context.Context, r Runner, reserved, until string) (string, error) {
	args := []string{"builder", "prune", "--force"}
	if reserved != "" {
		args = append(args, "--reserved-space", reserved)
	}
	if until != "" {
		args = append(args, "--filter", "until="+until)
	}
	out, err := r.Combined(ctx, "", args...)
	if err != nil {
		return "", err
	}
	return freed(string(out)), nil
}

// PruneContainers removes stopped containers and returns the space freed.
func PruneContainers(ctx context.Context, r Runner) (string, error) {
	out, err := r.Run(ctx, "", "container", "prune", "--force")
	if err != nil {
		return "", err
	}
	return freed(string(out)), nil
}

// RepoDigests returns the registry digests a local image was pulled as
// ("redis@sha256:…"). An image that isn't on this host has none.
func RepoDigests(ctx context.Context, r Runner, image string) ([]string, error) {
	out, err := r.Run(ctx, "", "image", "inspect", "--format", "{{json .RepoDigests}}", image)
	if err != nil {
		if strings.Contains(err.Error(), "No such image") {
			return nil, nil
		}
		return nil, err
	}
	var digests []string
	if err := json.Unmarshal([]byte(strings.TrimSpace(string(out))), &digests); err != nil {
		return nil, fmt.Errorf("reading image digests: %w", err)
	}
	return digests, nil
}

var totalRe = regexp.MustCompile(`(?i)total(?: reclaimed space)?:\s*(\S+)`)

// freed reads the size from a prune command's summary: "Total reclaimed
// space: 412MB" (image and container prune) or "Total:\t1.66GB" (builder
// prune).
func freed(out string) string {
	if m := totalRe.FindStringSubmatch(out); m != nil {
		return m[1]
	}
	return "0B"
}

var sizeRe = regexp.MustCompile(`(?i)^\s*([0-9.]+)\s*([kmgtp]?i?b)?\s*$`)

// ParseSize reads sizes as docker prints them ("6.997GB", "458.8kB",
// "0B", "10GiB") into bytes. Decimal units are powers of 1000 and binary
// ones (KiB, MiB…) powers of 1024, as docker uses them.
func ParseSize(s string) (int64, error) {
	s, _, _ = strings.Cut(s, "(") // "6.796GB (88%)"
	m := sizeRe.FindStringSubmatch(s)
	if m == nil {
		return 0, fmt.Errorf("not a size: %q", s)
	}
	n, err := strconv.ParseFloat(m[1], 64)
	if err != nil {
		return 0, fmt.Errorf("not a size: %q", s)
	}
	unit := strings.ToLower(m[2])
	base := 1000.0
	if strings.Contains(unit, "i") {
		base = 1024
	}
	exp := strings.Index("bkmgtp", unit[:min(1, len(unit))])
	if unit == "" {
		exp = 0
	}
	for range exp {
		n *= base
	}
	return int64(n), nil
}

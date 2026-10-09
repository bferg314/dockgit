package dock

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Image is one local image.
type Image struct {
	ID         string // full, with "sha256:"
	Repository string // "<none>" for dangling images
	Tag        string
	Size       string // as docker prints it, e.g. "57.8MB"
	Created    time.Time
	Containers int // containers using it, running or not
}

// Dangling reports whether the image has no name: a leftover from a
// rebuild.
func (i *Image) Dangling() bool { return i.Repository == "<none>" }

// Ref is "repository:tag", or the short ID for a dangling image.
func (i *Image) Ref() string {
	if i.Dangling() {
		return "<none> " + shortID(i.ID)
	}
	if i.Tag == "" || i.Tag == "<none>" {
		return i.Repository
	}
	return i.Repository + ":" + i.Tag
}

func (i *Image) ShortID() string { return shortID(i.ID) }

type imageJSON struct {
	ID         string `json:"ID"`
	Repository string `json:"Repository"`
	Tag        string `json:"Tag"`
	Size       string `json:"Size"`
	CreatedAt  string `json:"CreatedAt"`
	Containers string `json:"Containers"`
}

// Images lists local images, newest first.
func Images(ctx context.Context, r Runner) ([]*Image, error) {
	out, err := r.Run(ctx, "", "image", "ls", "--no-trunc", "--format", "json")
	if err != nil {
		return nil, err
	}
	var imgs []*Image
	err = eachJSONLine(out, func(line []byte) error {
		var j imageJSON
		if err := json.Unmarshal(line, &j); err != nil {
			return err
		}
		n, _ := strconv.Atoi(j.Containers)
		imgs = append(imgs, &Image{
			ID: j.ID, Repository: j.Repository, Tag: j.Tag, Size: j.Size,
			Created: parseCLITime(j.CreatedAt), Containers: n,
		})
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("reading docker image ls: %w", err)
	}
	sort.SliceStable(imgs, func(i, j int) bool { return imgs[i].Created.After(imgs[j].Created) })
	return imgs, nil
}

// RemoveImage deletes one image by ID.
func RemoveImage(ctx context.Context, r Runner, id string) error {
	_, err := r.Run(ctx, "", "image", "rm", id)
	return err
}

// PruneDanglingImages removes unnamed, unused images and returns docker's
// "Total reclaimed space" figure.
func PruneDanglingImages(ctx context.Context, r Runner) (string, error) {
	out, err := r.Run(ctx, "", "image", "prune", "-f")
	if err != nil {
		return "", err
	}
	return reclaimed(string(out)), nil
}

// Volume is one volume with its size and how many containers use it.
type Volume struct {
	Name   string
	Driver string
	Size   string // "N/A" when docker can't tell
	Links  int    // containers using it, running or not
}

type dfJSON struct {
	Volumes []struct {
		Name   string `json:"Name"`
		Driver string `json:"Driver"`
		Size   string `json:"Size"`
		Links  string `json:"Links"`
	} `json:"Volumes"`
}

// Volumes lists volumes with their sizes. It uses `docker system df -v`,
// the only command that reports volume sizes, which takes a few seconds.
func Volumes(ctx context.Context, r Runner) ([]*Volume, error) {
	out, err := r.Run(ctx, "", "system", "df", "-v", "--format", "json")
	if err != nil {
		return nil, err
	}
	var df dfJSON
	if err := json.Unmarshal(bytes.TrimSpace(out), &df); err != nil {
		return nil, fmt.Errorf("reading docker system df: %w", err)
	}
	vs := make([]*Volume, 0, len(df.Volumes))
	for _, v := range df.Volumes {
		n, _ := strconv.Atoi(v.Links)
		vs = append(vs, &Volume{Name: v.Name, Driver: v.Driver, Size: v.Size, Links: n})
	}
	sort.Slice(vs, func(i, j int) bool { return vs[i].Name < vs[j].Name })
	return vs, nil
}

// RemoveVolume deletes one volume. Docker refuses if a container uses it.
func RemoveVolume(ctx context.Context, r Runner, name string) error {
	_, err := r.Run(ctx, "", "volume", "rm", name)
	return err
}

// Stats is a container's resource use at one moment.
type Stats struct {
	CPU string // "0.06%"
	Mem string // "85.36MiB / 15.34GiB"
}

type statsJSON struct {
	Container string `json:"Container"`
	CPUPerc   string `json:"CPUPerc"`
	MemUsage  string `json:"MemUsage"`
}

// GetStats samples every running container once, keyed by full ID. It
// takes about two seconds, the sampling interval docker uses.
func GetStats(ctx context.Context, r Runner) (map[string]Stats, error) {
	out, err := r.Run(ctx, "", "stats", "--no-stream", "--no-trunc", "--format", "json")
	if err != nil {
		return nil, err
	}
	stats := map[string]Stats{}
	err = eachJSONLine(out, func(line []byte) error {
		var j statsJSON
		if err := json.Unmarshal(line, &j); err != nil {
			return err
		}
		stats[j.Container] = Stats{CPU: j.CPUPerc, Mem: j.MemUsage}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("reading docker stats: %w", err)
	}
	return stats, nil
}

func eachJSONLine(out []byte, f func([]byte) error) error {
	sc := bufio.NewScanner(bytes.NewReader(out))
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	for sc.Scan() {
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 {
			continue
		}
		if err := f(line); err != nil {
			return err
		}
	}
	return sc.Err()
}

// parseCLITime reads the CLI's "2026-10-08 10:12:13 -0400 EDT" format.
func parseCLITime(s string) time.Time {
	t, err := time.Parse("2006-01-02 15:04:05 -0700 MST", s)
	if err != nil {
		return time.Time{}
	}
	return t
}

var reclaimedRe = regexp.MustCompile(`(?i)total reclaimed space:\s*(\S+)`)

// reclaimed pulls the size out of a prune command's summary line.
func reclaimed(out string) string {
	if m := reclaimedRe.FindStringSubmatch(out); m != nil {
		return m[1]
	}
	return "0B"
}

func itoa(n int) string { return strconv.Itoa(n) }

// TrimTimestamp splits a line from `docker logs --timestamps` into its
// RFC 3339 timestamp and the text after it.
func TrimTimestamp(line string) (ts, text string) {
	if i := strings.IndexByte(line, ' '); i > 20 && line[4] == '-' && line[10] == 'T' {
		return line[:i], line[i+1:]
	}
	return "", line
}

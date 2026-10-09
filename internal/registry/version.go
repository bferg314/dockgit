package registry

import (
	"regexp"
	"strconv"
	"strings"
)

// version is a tag read as numbers: "v3.41.3" -> [3 41 3], prefix "v";
// "2026.9.2-java17" -> [2026 9 2], suffix "-java17".
type version struct {
	prefix string
	nums   []int
	suffix string
}

var versionTag = regexp.MustCompile(`^(v?)(\d+(?:\.\d+)*)(.*)$`)

func parseVersion(tag string) (version, bool) {
	m := versionTag.FindStringSubmatch(tag)
	if m == nil {
		return version{}, false
	}
	var v version
	v.prefix, v.suffix = m[1], m[3]
	for _, p := range strings.Split(m[2], ".") {
		n, err := strconv.Atoi(p)
		if err != nil {
			return version{}, false
		}
		v.nums = append(v.nums, n)
	}
	return v, true
}

// sameShape: same prefix, same number of parts, same suffix. A pinned
// "1.43.4" is only compared with other three-part plain tags, not with
// "1.43.4.10903-e5521bd8c-ls327" or "arm64v8-1.43.4".
func (v version) sameShape(o version) bool {
	return v.prefix == o.prefix && v.suffix == o.suffix && len(v.nums) == len(o.nums)
}

func (v version) less(o version) bool {
	for i := range v.nums {
		if v.nums[i] != o.nums[i] {
			return v.nums[i] < o.nums[i]
		}
	}
	return false
}

// Pinned reports whether a tag names a specific version (at least two
// parts, such as 1.5 or 2026.9.2-java17) rather than a moving one
// (latest, java17, or a bare major like 2).
func Pinned(tag string) bool {
	v, ok := parseVersion(tag)
	return ok && len(v.nums) >= 2
}

// calendar reports whether a version's first part is a year (2026.9.2):
// those are one series, with no major versions to hold back.
func (v version) calendar() bool { return len(v.nums) > 0 && v.nums[0] >= 1000 }

// Newest returns the newest tag of the same shape and major version as
// current, or "" when current is the newest: the update that's safe to
// suggest.
func Newest(current string, tags []string) string {
	return newest(current, tags, func(cur, v version) bool { return cur.calendar() || v.nums[0] == cur.nums[0] })
}

// NewestMajor returns the newest tag of the same shape in a later major
// version, or "" if there is none. Majors more than three ahead are
// ignored: they're another naming scheme (linuxserver's old 20.04.1 tags
// for qbittorrent 5.x), not a release.
func NewestMajor(current string, tags []string) string {
	return newest(current, tags, func(cur, v version) bool {
		return !cur.calendar() && v.nums[0] > cur.nums[0] && v.nums[0] <= cur.nums[0]+3
	})
}

func newest(current string, tags []string, keep func(cur, v version) bool) string {
	cur, ok := parseVersion(current)
	if !ok {
		return ""
	}
	best, bestTag := cur, ""
	for _, t := range tags {
		v, ok := parseVersion(t)
		if !ok || !v.sameShape(cur) || !keep(cur, v) {
			continue
		}
		if best.less(v) {
			best, bestTag = v, t
		}
	}
	return bestTag
}

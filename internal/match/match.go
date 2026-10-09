// Package match normalizes git remote URLs, to match repos with images and
// to open their web pages.
package match

import (
	"net/url"
	"strings"
)

// Key turns any common remote URL form into "host/owner/repo" (lowercase).
//
//	git@github.com:Owner/Repo.git        -> github.com/owner/repo
//	https://github.com/Owner/Repo        -> github.com/owner/repo
//	ssh://git@github.com:22/Owner/Repo   -> github.com/owner/repo
//
// It returns "" for URLs it does not understand (local paths, etc).
func Key(raw string) string {
	host, path := parse(raw)
	if host == "" {
		return ""
	}
	return strings.ToLower(host + "/" + path)
}

// WebURL turns a remote URL into the repo's web page, keeping the original
// case: git@github.com:Owner/Repo.git -> https://github.com/Owner/Repo.
// It returns "" for URLs it does not understand (local paths, etc).
func WebURL(raw string) string {
	host, path := parse(raw)
	if host == "" {
		return ""
	}
	return "https://" + host + "/" + path
}

// parse splits a remote URL into host and "owner/repo" path, or returns
// empty strings.
func parse(raw string) (host, path string) {
	raw = strings.TrimSpace(raw)
	if strings.Contains(raw, "://") {
		u, err := url.Parse(raw)
		if err != nil || u.Host == "" || u.Scheme == "file" {
			return "", ""
		}
		host, path = u.Hostname(), u.Path
	} else if at := strings.Index(raw, "@"); at >= 0 {
		// scp-like: user@host:owner/repo
		h, p, ok := strings.Cut(raw[at+1:], ":")
		if !ok {
			return "", ""
		}
		host, path = h, p
	} else {
		return "", ""
	}

	path = strings.Trim(path, "/")
	path = strings.TrimSuffix(path, ".git")
	if host == "" || path == "" {
		return "", ""
	}
	return host, path
}

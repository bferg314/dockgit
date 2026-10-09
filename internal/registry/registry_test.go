package registry

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestParse(t *testing.T) {
	cases := map[string]Ref{
		"postgres":                           {"docker.io", "library/postgres", "latest"},
		"linuxserver/plex:1.43.4":            {"docker.io", "linuxserver/plex", "1.43.4"},
		"ghcr.io/bferg314/clock:1.4.0":       {"ghcr.io", "bferg314/clock", "1.4.0"},
		"lscr.io/linuxserver/plex@sha256:ab": {"lscr.io", "linuxserver/plex", "latest"},
		"localhost:5000/app:dev":             {"localhost:5000", "app", "dev"},
	}
	for in, want := range cases {
		if got := Parse(in); got != want {
			t.Errorf("Parse(%q) = %+v, want %+v", in, got, want)
		}
	}
}

func TestNewest(t *testing.T) {
	tags := []string{"latest", "1", "1.4.0", "1.4.1", "1.5.0", "1.10.0-rc1", "1.9.2", "1.9", "v2.0.0", "arm64v8-2.0.0",
		"2026.9.2-java17", "2026.10.0-java17", "2026.10.1-java21", "1.43.4.10903-e5521bd8c-ls327"}
	tags = append(tags, "v1.2.0", "2.0.0", "2.1.0", "5.2.4", "5.2.5", "20.04.1", "2027.1.0-java17")
	cases := map[string][2]string{ // current: newest in its major, newest major
		"1.4.0":           {"1.9.2", "2.1.0"}, // not 1.10.0-rc1 (suffix), not v2.0.0 (prefix)
		"1.9.2":           {"", "2.1.0"},
		"1.9":             {"", ""},
		"v1.0.0":          {"v1.2.0", "v2.0.0"},
		"2026.9.2-java17": {"2027.1.0-java17", ""}, // calendar versions are one series
		"5.2.4":           {"5.2.5", ""},           // 20.04.1 is another scheme
		"latest":          {"", ""},
	}
	for cur, want := range cases {
		if got := Newest(cur, tags); got != want[0] {
			t.Errorf("Newest(%q) = %q, want %q", cur, got, want[0])
		}
		if got := NewestMajor(cur, tags); got != want[1] {
			t.Errorf("NewestMajor(%q) = %q, want %q", cur, got, want[1])
		}
	}
	for tag, want := range map[string]bool{"1.4.0": true, "v3.41": true, "2026.9.2-java17": true, "latest": false, "java17": false, "2": false} {
		if Pinned(tag) != want {
			t.Errorf("Pinned(%q) = %v", tag, !want)
		}
	}
}

// fakeRegistry needs a token, pages its tags, and answers manifests.
func fakeRegistry(t *testing.T) *httptest.Server {
	t.Helper()
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/token":
			if r.URL.Query().Get("scope") != "repository:me/app:pull" {
				http.Error(w, "bad scope", 400)
				return
			}
			fmt.Fprint(w, `{"token":"t0k"}`)
			return
		case r.Header.Get("Authorization") != "Bearer t0k":
			w.Header().Set("WWW-Authenticate", fmt.Sprintf(`Bearer realm="%s/token",service="fake",scope="repository:me/app:pull"`, srv.URL))
			w.WriteHeader(401)
			return
		case r.URL.Path == "/v2/me/app/tags/list" && r.URL.Query().Get("last") == "":
			w.Header().Set("Link", `</v2/me/app/tags/list?n=2&last=1.0.1>; rel="next"`)
			fmt.Fprint(w, `{"name":"me/app","tags":["1.0.0","1.0.1"]}`)
		case r.URL.Path == "/v2/me/app/tags/list":
			fmt.Fprint(w, `{"name":"me/app","tags":["1.1.0","latest"]}`)
		case r.URL.Path == "/v2/me/app/manifests/latest" && r.Method == http.MethodHead:
			if !strings.Contains(strings.Join(r.Header.Values("Accept"), ","), "image.index") {
				http.Error(w, "accept", 400)
				return
			}
			w.Header().Set("Docker-Content-Digest", "sha256:abc")
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestTagsAndDigest(t *testing.T) {
	srv := fakeRegistry(t)
	c := &Client{Hosts: map[string]string{"reg.test": srv.URL}}
	ref := Parse("reg.test/me/app:1.0.0")
	tags, err := c.Tags(context.Background(), ref)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(tags, ",") != "1.0.0,1.0.1,1.1.0,latest" {
		t.Fatalf("tags across pages: %q", tags)
	}
	if got := Newest(ref.Tag, tags); got != "1.1.0" {
		t.Fatalf("newest: %q", got)
	}
	d, err := c.Digest(context.Background(), Parse("reg.test/me/app:latest"))
	if err != nil || d != "sha256:abc" {
		t.Fatalf("digest: %q %v", d, err)
	}
	if _, err := c.Digest(context.Background(), Parse("reg.test/me/app:nope")); err != ErrNotFound {
		t.Fatalf("missing tag: %v", err)
	}
}

// Package registry asks image registries (Docker Hub, GHCR, lscr.io and
// other OCI registries) which tags an image has and what a tag points to,
// for checking whether a stack's images have updates. Only public images
// are supported: it uses the anonymous tokens registries hand out.
package registry

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"
)

// Client talks to registries. Its zero value uses http.DefaultClient.
type Client struct {
	HTTP *http.Client
	// Hosts maps a registry name to the base URL to use, for tests.
	Hosts map[string]string
}

// Ref is an image reference split into registry, repository and tag.
type Ref struct {
	Registry, Repo, Tag string
}

// Parse splits an image reference: "linuxserver/plex:1.43.4" ->
// {registry-1.docker.io, linuxserver/plex, 1.43.4}. The tag defaults to
// "latest"; a digest is dropped.
func Parse(image string) Ref {
	image, _, _ = strings.Cut(image, "@")
	ref := Ref{Tag: "latest"}
	if i := strings.LastIndex(image, ":"); i > strings.LastIndex(image, "/") {
		image, ref.Tag = image[:i], image[i+1:]
	}
	first, rest, ok := strings.Cut(image, "/")
	switch {
	case !ok:
		ref.Registry, ref.Repo = "docker.io", "library/"+image
	case strings.ContainsAny(first, ".:") || first == "localhost":
		ref.Registry, ref.Repo = first, rest
	default:
		ref.Registry, ref.Repo = "docker.io", image
	}
	ref.Repo = strings.ToLower(ref.Repo)
	return ref
}

func (c *Client) base(registry string) string {
	if b, ok := c.Hosts[registry]; ok {
		return b
	}
	if registry == "docker.io" {
		return "https://registry-1.docker.io"
	}
	return "https://" + registry
}

func (c *Client) http() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return http.DefaultClient
}

// get makes a registry request, fetching an anonymous token when the
// registry asks for one.
func (c *Client) get(ctx context.Context, method, rawURL string, accept []string) (*http.Response, error) {
	do := func(token string) (*http.Response, error) {
		req, err := http.NewRequestWithContext(ctx, method, rawURL, nil)
		if err != nil {
			return nil, err
		}
		for _, a := range accept {
			req.Header.Add("Accept", a)
		}
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		return c.http().Do(req)
	}
	resp, err := do("")
	if err != nil || resp.StatusCode != http.StatusUnauthorized {
		return resp, err
	}
	challenge := resp.Header.Get("WWW-Authenticate")
	resp.Body.Close()
	token, err := c.token(ctx, challenge)
	if err != nil {
		return nil, err
	}
	return do(token)
}

var challengeParam = regexp.MustCompile(`(\w+)="([^"]*)"`)

// token answers a Bearer challenge: realm, service and scope.
func (c *Client) token(ctx context.Context, challenge string) (string, error) {
	if !strings.HasPrefix(strings.ToLower(challenge), "bearer ") {
		return "", fmt.Errorf("registry wants %q auth, which isn't supported", challenge)
	}
	params := map[string]string{}
	for _, m := range challengeParam.FindAllStringSubmatch(challenge, -1) {
		params[m[1]] = m[2]
	}
	realm, err := url.Parse(params["realm"])
	if err != nil || params["realm"] == "" {
		return "", fmt.Errorf("bad auth challenge %q", challenge)
	}
	q := realm.Query()
	for _, k := range []string{"service", "scope"} {
		if v := params[k]; v != "" {
			q.Set(k, v)
		}
	}
	realm.RawQuery = q.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, realm.String(), nil)
	if err != nil {
		return "", err
	}
	resp, err := c.http().Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("registry token: %s (private image?)", resp.Status)
	}
	var t struct {
		Token       string `json:"token"`
		AccessToken string `json:"access_token"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&t); err != nil {
		return "", err
	}
	if t.Token != "" {
		return t.Token, nil
	}
	return t.AccessToken, nil
}

// maxTagPages bounds pagination: images like itzg/minecraft-server have
// thousands of tags.
const maxTagPages = 30

// Tags lists every tag of the image's repository.
func (c *Client) Tags(ctx context.Context, ref Ref) ([]string, error) {
	next := fmt.Sprintf("%s/v2/%s/tags/list?n=1000", c.base(ref.Registry), ref.Repo)
	var tags []string
	for page := 0; next != "" && page < maxTagPages; page++ {
		resp, err := c.get(ctx, http.MethodGet, next, []string{"application/json"})
		if err != nil {
			return nil, err
		}
		var body struct {
			Tags []string `json:"tags"`
		}
		err = json.NewDecoder(resp.Body).Decode(&body)
		link := resp.Header.Get("Link")
		status := resp.Status
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("listing tags of %s: %s", ref.Repo, status)
		}
		if err != nil {
			return nil, err
		}
		tags = append(tags, body.Tags...)
		next = nextPage(c.base(ref.Registry), link)
	}
	return tags, nil
}

var linkNext = regexp.MustCompile(`<([^>]+)>;\s*rel="next"`)

// nextPage reads a Link header's rel="next" URL, which may be relative.
func nextPage(base, link string) string {
	m := linkNext.FindStringSubmatch(link)
	if m == nil {
		return ""
	}
	if strings.HasPrefix(m[1], "/") {
		return base + m[1]
	}
	return m[1]
}

// manifestTypes are the manifest kinds a tag can point to; asking for the
// index first gets the digest that docker records in RepoDigests.
var manifestTypes = []string{
	"application/vnd.oci.image.index.v1+json",
	"application/vnd.docker.distribution.manifest.list.v2+json",
	"application/vnd.docker.distribution.manifest.v2+json",
	"application/vnd.oci.image.manifest.v1+json",
}

// ErrNotFound means the tag doesn't exist in the registry.
var ErrNotFound = errors.New("not found in the registry")

// Digest returns the digest the tag currently points to.
func (c *Client) Digest(ctx context.Context, ref Ref) (string, error) {
	u := fmt.Sprintf("%s/v2/%s/manifests/%s", c.base(ref.Registry), ref.Repo, ref.Tag)
	resp, err := c.get(ctx, http.MethodHead, u, manifestTypes)
	if err != nil {
		return "", err
	}
	resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusNotFound:
		return "", ErrNotFound
	case resp.StatusCode != http.StatusOK:
		return "", fmt.Errorf("checking %s:%s: %s", ref.Repo, ref.Tag, resp.Status)
	}
	d := resp.Header.Get("Docker-Content-Digest")
	if d == "" {
		return "", fmt.Errorf("checking %s:%s: no digest in the reply", ref.Repo, ref.Tag)
	}
	return d, nil
}

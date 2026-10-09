package match

import "testing"

func TestKey(t *testing.T) {
	cases := map[string]string{
		"git@github.com:Owner/Repo.git":          "github.com/owner/repo",
		"https://github.com/Owner/Repo":          "github.com/owner/repo",
		"https://github.com/Owner/Repo.git/":     "github.com/owner/repo",
		"https://user@github.com/owner/repo.git": "github.com/owner/repo",
		"ssh://git@github.com:22/owner/repo.git": "github.com/owner/repo",
		"git://github.com/owner/repo":            "github.com/owner/repo",
		"/home/me/repos/thing":                   "",
		"file:///tmp/thing":                      "",
		"":                                       "",
	}
	for in, want := range cases {
		if got := Key(in); got != want {
			t.Errorf("Key(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestWebURL(t *testing.T) {
	cases := map[string]string{
		"git@github.com:Owner/Repo.git":          "https://github.com/Owner/Repo",
		"https://github.com/Owner/Repo":          "https://github.com/Owner/Repo",
		"ssh://git@github.com:22/owner/repo.git": "https://github.com/owner/repo",
		"https://user@gitlab.com/group/sub/proj": "https://gitlab.com/group/sub/proj",
		"/home/me/repos/thing":                   "",
	}
	for in, want := range cases {
		if got := WebURL(in); got != want {
			t.Errorf("WebURL(%q) = %q, want %q", in, got, want)
		}
	}
}

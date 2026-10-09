package ui

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"

	"github.com/bferg314/dockgit/internal/compose"
	"github.com/bferg314/dockgit/internal/config"
)

// openAddRoot asks for a folder of compose files to list.
func (a *App) openAddRoot() {
	a.openAddRepo()
	a.addRepo.forRoot = true
	a.addRepo.input.Placeholder = "~/code/stacks"
}

// submitAddRoot adds the folder typed as a compose root.
func (a *App) submitAddRoot() tea.Cmd {
	d := a.addRepo
	dir := config.ExpandPath(strings.TrimSpace(d.input.Value()))
	if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
		d.err = "No such folder."
		return nil
	}
	for _, k := range a.cfg.ComposeRoots {
		if compose.NormPath(config.ExpandPath(k)) == compose.NormPath(dir) {
			d.err = filepath.Base(dir) + " is already listed."
			return nil
		}
	}
	key := tildePath(filepath.Clean(dir))
	a.cfg.ComposeRoots = append(a.cfg.ComposeRoots, key)
	a.addRepo = nil
	a.syncRoots()
	return tea.Batch(a.saveConfig(), loadRoot(key, config.ExpandPath(key)), a.notify(1, "Added %s", filepath.Base(dir)))
}

// newStackDialog asks for a new stack's name.
type newStackDialog struct {
	root  string // root key
	input textinput.Model
	err   string
}

func (a *App) openNewStack(r *rootState) {
	in := textinput.New()
	in.Prompt = ""
	in.Placeholder = "apps/clock"
	in.Focus()
	a.newStack = &newStackDialog{root: r.key, input: in}
}

var stackName = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]*(/[a-z0-9][a-z0-9._-]*)?$`)

func (a *App) newStackKey(msg tea.KeyPressMsg) tea.Cmd {
	d := a.newStack
	switch msg.String() {
	case "esc":
		a.newStack = nil
		return nil
	case "enter":
		return a.createStack()
	}
	var cmd tea.Cmd
	d.input, cmd = d.input.Update(msg)
	d.err = ""
	return cmd
}

// createStack writes stacks/<group>/<name>/compose.yaml and .env.example
// from a small template, then opens the compose file.
func (a *App) createStack() tea.Cmd {
	d := a.newStack
	name := strings.ToLower(strings.Trim(strings.TrimSpace(d.input.Value()), "/"))
	if !stackName.MatchString(name) {
		d.err = "Use name or group/name: lower case letters, digits, - . _"
		return nil
	}
	root := config.ExpandPath(d.root)
	dir := filepath.Join(root, "stacks", filepath.FromSlash(name))
	if _, err := os.Stat(dir); err == nil {
		d.err = "stacks/" + name + " already exists."
		return nil
	}
	service := filepath.Base(dir)
	files := map[string]string{
		"compose.yaml": "# " + service + ": what it is and how to reach it.\n" +
			"services:\n" +
			"  " + service + ":\n" +
			"    image: REPLACE_ME:${TAG:-latest}\n" +
			"    restart: unless-stopped\n" +
			"    # ports:\n" +
			"    #   - \"8080:80\"\n" +
			"    # No env_file needed: dockgit passes the root's .env and this folder's.\n" +
			"\n" +
			"x-dockgit:\n" +
			"  description: \"\"\n",
		".env.example": "# Every variable compose.yaml uses. E copies this to .env to fill in.\nTAG=latest\n",
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		d.err = err.Error()
		return nil
	}
	for f, body := range files {
		if err := os.WriteFile(filepath.Join(dir, f), []byte(body), 0o644); err != nil {
			d.err = err.Error()
			return nil
		}
	}
	a.newStack = nil
	key := d.root
	return tea.Batch(loadRoot(key, root), a.openInEditor(name, filepath.Join(dir, "compose.yaml")),
		a.notify(1, "Created stacks/%s: fill in the image, then U starts it", name))
}

func (a *App) renderNewStack() string {
	st := a.st
	d := a.newStack
	w := min(64, max(30, a.w-10))
	d.input.SetWidth(w - 4)
	lines := []string{st.boxTitle.Render("New stack"), "",
		st.dim.Render("Name it name or group/name, e.g. apps/clock or media/plex."),
		st.dim.Render("It's created as stacks/<group>/<name>/compose.yaml."),
		"", fit(st.key.Render("› ")+d.input.View(), w)}
	if d.err != "" {
		lines = append(lines, "", st.bad.Render(d.err))
	}
	lines = append(lines, "", st.dim.Render("enter creates · esc cancels"))
	return a.boxed(st.box, lines)
}

# dockgit design

A terminal dashboard that ties your Docker containers to the git repos and compose files they come from. Written in Go with the same stack, look and conventions as [folgit](https://github.com/bferg314/folgit).

This is the plan dockgit was built from, kept up to date with what was built and the decisions made along the way. [README.md](README.md) describes how to use it.

## 1. Goals and non-goals

**Goals**

- One screen that answers "what's running, where did it come from, and is it current?"
- Switching a repo's branch and rebuilding takes one key press.
- Running a compose stack from any folder or repo of compose files takes one key press, including first-time `.env` setup.
- A recommended stack layout (§5a) that dockgit checks for, without requiring it.
- Every container is linked to its repo or compose file, so you can jump between them.

**Non-goals (v1)**

- Managing remote Docker hosts or contexts other than the current one. dockgit reads `DOCKER_HOST` and the current context, and shows the context's name.
- Swarm, Kubernetes, and publishing images.
- Writing compose YAML. dockgit runs compose files and opens them in your editor; the only edit it makes is bumping image versions (§5, Compose).
- Replacing lazydocker. Logs, stats and exec are included, but there's no deep inspection UI.

## 2. What comes from folgit

The two projects are separate repos. Reusable code was copied, not shared, and a shared module is only worth it if both keep changing in the same ways.

| folgit package | Use in dockgit |
|---|---|
| `internal/ui/styles.go` | As is: palette, light/dark detection, tab styles. |
| `internal/ui/statusbar.go` | Modes become `DOCKER`, `REPOS`, `COMPOSE`, `SETTINGS`, `LOGS`, `FILTER` and so on. |
| `internal/ui/format.go`, help and popup patterns | The `?` overlay, action popups, toasts, `fit` and time formatting. |
| detail pane | A side pane at ≥100 columns, full screen when narrower, `J`/`K`/`ctrl+d`/`ctrl+u` to scroll. dockgit's `d` cycles beside → full screen → hidden. |
| `internal/config` | `ExpandPath`, `~` handling, versioned TOML. New schema (§6). |
| `internal/gitinfo` | Status parsing. dockgit adds a branch listing and runs mutating git commands as jobs. |
| `internal/launcher` | Tools in a folder or on a file, detached or in the terminal, zellij tabs. |
| `internal/match` | Remote URL → `host/owner/repo` and web page. |

Same dependencies: `charm.land/bubbletea/v2`, `bubbles/v2`, `lipgloss/v2`, `BurntSushi/toml`, `sahilm/fuzzy`, plus `gopkg.in/yaml.v3` to read compose files.

Same key conventions: `j`/`k` move, `/` filters, `r` refreshes, `?` shows help, `q` quits, `1`–`4` switch tabs, `enter` opens the action popup, and an action's own key runs it directly.

## 3. Docker access

**Decision: use the `docker` CLI, not the Engine SDK.** Compose has to go through `docker compose` anyway, and using the CLI everywhere means:

- whatever context, `DOCKER_HOST`, credentials and Docker Desktop setup work in your shell also work in dockgit;
- no heavy `github.com/docker/docker` dependency tree;
- it matches folgit, which shells out to `git`.

All calls go through one interface, so the UI is tested with a fake:

```go
type Runner interface {
    Run(ctx context.Context, dir string, args ...string) ([]byte, error)                   // short commands, stdout only
    Combined(ctx context.Context, dir string, args ...string) ([]byte, error)              // stdout+stderr, e.g. docker logs
    Stream(ctx context.Context, dir string, args ...string) (io.ReadCloser, error)         // events
    StreamCombined(ctx context.Context, dir string, args ...string) (io.ReadCloser, error) // followed logs
}
```

Builds, pulls, ups and downs run as **jobs**: a sequence of git and docker steps whose combined output streams into the logs viewer, stopping at the first failure.

| Need | Command |
|---|---|
| Containers | `docker ps -aq --no-trunc`, then `docker inspect <ids>` (batched: `ps` output joins labels with commas, and compose labels contain commas) |
| Images | `docker image ls --no-trunc --format json` (what uses each is worked out from the container list) |
| Volumes | `docker system df -v --format json`, the only command that reports volume sizes (a few seconds, so it loads in the background) |
| Live updates | `docker events --format json`, filtered to container lifecycle events (healthchecks emit `exec_*` events every few seconds) |
| Stats | `docker stats --no-stream --format json`, only while the stats column is on |
| Logs | `docker logs --follow --timestamps --tail N <id>`, or `docker compose logs` for a project |
| Compose | `docker compose -f … [--env-file …] [-p …] --progress plain up -d [--build]`, `down`, `pull` |
| Disk and cache | `docker system df --format json`, `docker builder prune --reserved-space … --filter until=…`, `docker image prune`, `docker container prune` |
| Image updates | registry API directly (tags and digests, anonymous tokens), compared with `docker image inspect` RepoDigests |

At startup dockgit asks for the context and server version. If the daemon isn't reachable, the Docker tab shows the error and a retry key, and the Repos and Compose tabs still work for git actions.

## 4. Linking things together

The links between the tabs come from one index, rebuilt when repos or stacks change:

```
Container ──(compose labels)──► compose file ──► stack (Compose tab)
    │                                    └─────► repo  (Repos tab)
    └──(image)──► registry name ──(matches a repo's remote)──► repo
```

**How links are found, most reliable first:**

1. **Compose labels on containers:** `com.docker.compose.project.config_files` and `.working_dir`. A container belongs to a stack when one of its config files is that stack's file, and to a repo when its files are in the repo's folder. Paths are normalized first (Windows case and slashes, WSL's `/mnt/c/...`, Docker Desktop's `/run/desktop/mnt/host/c/...`).
2. **Published images:** `ghcr.io/me/clock:1.4.0` links to a repo whose `origin` is `github.com/me/clock`. A stack can also name its repo in `x-dockgit.repo`.
3. A compose project whose file no longer exists is **gone** (moved or deleted); one dockgit doesn't list is shown by project name; anything else is unmanaged.

**What the links power:**

- **Docker tab:** each compose project's heading names its source (`repo clock`, `stack monitoring`, `gone services/web.yml`). All of a project's containers share it, so it isn't repeated per row. `g` jumps to that repo or stack.
- **Repos tab:** "stale: built from feature" or "stale: new commits since build" when the running containers came from another branch or an older commit (dockgit records the branch and commit of each build it runs), and "also stack clock ●" when a stack deploys the repo.
- **Compose tab:** a stack shows the repo it deploys.
- **Checks before starting** (`U`/`R` on a stack, `b`/`u`/`U` on a repo):
  - host ports held by another running container;
  - a fixed `container_name` already taken, even by a stopped container;
  - the same service (same image and service name) already running from somewhere else, usually an older deployment;
  - `network_mode` pointing at a container outside the stack that isn't running;
  - variables with no value.

  The popup offers to stop (or, for names and old deployments, remove) those containers and then start, to start anyway, or to cancel.

## 5. Tabs

### Tab 1: Docker

Containers grouped by compose project, unmanaged ones last.

Columns: state dot · name · image · ports · status (uptime, health or exit code) · optional CPU and memory. Below 72 columns the ports column is dropped; the detail pane has them.

| Key | Action |
|---|---|
| `enter` | action popup |
| `l` | logs, full screen and following; `/` searches, `n`/`N` move between matches, `w` wraps, `t` shows timestamps |
| `s` / `S` | start or stop / restart |
| `x` | remove (asks first; a running container is stopped too) |
| `e` | a shell in the container (bash if it has one, else sh), in a zellij tab when inside zellij |
| `w` | open the first published port in the browser |
| `o` | open the compose project's folder in the tool bound to `o` (VS Code by default) |
| `g` | go to the repo or stack it came from |
| `i` / `v` / `C` | images / volumes / disk use and cleanup |
| `a` / `t` / `m` | stopped containers / stats column / env values in the detail pane |
| `d` | detail pane: beside the list, full screen, hidden |

The detail pane shows the source, compose project and file, ports, mounts, the last 20 log lines, container facts, networks and environment (masked until `m`).

### Tab 2: Repos

Git repos added one folder at a time with `n` (with tab completion). dockgit finds the repo's root and its compose files (`compose.yaml`, `compose.yml`, `docker-compose.yaml`, `docker-compose.yml`, and `compose.*.yml` / `docker-compose.*.yml` variants), and asks which to use when there's more than one.

Columns: repo · branch (↑↓) · changes · containers (up 2/2, 1/2, down) · last build, stale badge, linked stacks.

| Key | Action |
|---|---|
| `enter` | action popup |
| `b` | switch branch and build: local and remote branches, newest first, `ctrl+f` fetches; checks out and runs `up -d --build` |
| `u` | fast-forward pull, then build |
| `U` | build without pulling |
| `D` | compose down (asks first) |
| `l` / `O` | logs of all its services / output of its last job |
| `c` | compose files |
| `g` / `w` / `o` | its containers / its web page / open in VS Code |
| `x` | remove from the list (the folder is left alone) |

Safety: uncommitted changes are never discarded (`b` and `u` offer to stash them first), pulls are fast-forward only, checking out a remote-only branch creates a tracking branch, and git runs without prompts (ssh in batch mode unless `core.sshCommand` is set), so a background job can't hang on a password. A failed job opens its output.

### Tab 3: Compose

Any number of **compose roots** (`A` adds one): a git repo of stacks, or a plain folder. Every YAML file with a top-level `services:` key is listed, by folder for a lone `compose.yaml` and by file otherwise. Each row shows how many services are up, unset variables, "changed since up" when the file is newer than its running containers, pending updates and the doctor's findings. Git roots show their branch and `p` pulls them.

| Key | Action |
|---|---|
| `enter` | action popup |
| `U` / `R` / `D` | up / pull newer images then up / down |
| `l` / `O` | logs / output of the last job |
| `E` / `e` | create (from `.env.example`) and open `.env` / open the compose file |
| `u` | check for image updates: pinned versions against the registry's newer tags (the newest in the same major is suggested, a newer major is mentioned), moving tags by digest; `b` then bumps the versions in `compose.yaml`. On a root row it checks every stack |
| `g` / `o` | its containers / its folder in VS Code |
| `p` / `n` / `!` | on a root: pull it / new stack from a template / doctor report |
| `A` / `X` | add / stop listing a root |

**Env files** are layered as dockge does it: the root's `.env`, then `global.env` beside the stack folders, then the stack's own `.env`, later ones winning. `E` writes `.env` with LF line endings even when a Windows checkout gave `.env.example` CRLF ones, so no value ends in a stray carriage return.

**Project names** are compose's default, the folder name, so dockgit recognizes stacks started by hand. It never passes `--remove-orphans`, so `down` on one of several files in a folder only removes that file's services; state is worked out per file from the `config_files` label.

### 5a. Recommended stack layout

Compose repos that grow one file at a time end up mixing file names (`compose.yml`, `docker-compose.yml`, `web.yml`), putting several stacks in one folder, and wiring stacks together across files (`network_mode: service:vpn` in one file, the `vpn` service in another), which only works if files start in the right order. dockgit handles all of that, but a simple layout makes everything more reliable:

**One folder = one stack = one compose project.**

```
<root>/
  stacks/
    global.env.example         # shared values: PUID, PGID, TZ, DATA_DIR …
    global.env                 # real shared values (git-ignored); dockge applies it to every stack
    <stack>/                   # flat: dockge only looks one level down
      compose.yaml             # the one canonical file name
      .env.example             # this stack's variables (committed)
      .env                     # real values (git-ignored)
      README.md                # optional: what it is, ports, first-run notes
      compose.override.yaml    # optional, git-ignored: per-host tweaks, picked up by compose automatically
```

Rules, each checked by the doctor (`!`):

1. **One `compose.yaml` per folder.** The project name is the folder name, so it's unique and predictable.
2. **Stacks that depend on each other live in one file.** Separate stacks share things only through named external networks, never `network_mode: service:` across files.
3. **Shared values in `stacks/global.env`**, stack values in the stack's `.env`. Shared names are prefixed (`DATA_DIR`, not `APPDATA` or `USERNAME`): compose prefers a shell variable over `.env`, and Windows shells already have those.
4. **Every variable is listed** in the stack's `.env.example` or in `global.env.example`.
5. **No `container_name` unless something needs a fixed name.** Compose's `<project>-<service>-1` names can't clash.
6. **No host port used twice** across the root.
7. **Pinned image versions** (`image: louislam/dockge:1.5.0`), so updates are explicit (dockgit's `u`, or a bot such as Renovate). Versions written as variables (`${TAG:-latest}`) can't be read by update tools.
8. **Optional metadata in compose itself**, under `x-dockgit:` (compose ignores `x-` keys):
   ```yaml
   x-dockgit:
     description: Wall clock web app
     repo: ~/code/clock        # links this stack to a Repos-tab repo
   ```

Each finding comes with a one-line fix. The doctor also reports the same `container_name` or host port in several stacks, and containers started from compose files that no longer exist. Nothing requires the layout: loose files run as they are, with a ▲ marker.

### Tab 4: Settings

Toggles and values, saved to `config.toml` as they change: compose roots (listed), zellij tabs, confirmations for destructive actions, stopped containers, the stats column, masked env values, cleanup mode and sizes, dangling-image pruning, log lines to load, tools, and the Powerline status bar.

## 6. Config

`config.toml` in the OS config folder (`%AppData%\dockgit`, `~/Library/Application Support/dockgit`, `~/.config/dockgit`), versioned like folgit's. On first run every tool found on PATH is turned on.

```toml
version = 1
compose_roots = ["~/code/composes"]
zellij_tabs = true
show_stopped = true
stats = false
mask_env = true
log_tail = 500
confirm_destructive = true
powerline = false

[cleanup]
mode = "after_build"        # "off" | "after_build" | "threshold"
keep_storage = "10GB"       # docker builder prune --reserved-space (buildx dropped --keep-storage)
older_than = "168h"         # --filter until=
prune_dangling_images = true
threshold = "20GB"          # used when mode = "threshold"

[[repos]]
path = "~/code/clock"
compose_files = ["docker-compose.yml"]
project = ""                # empty = compose's default
env_file = ""

[[tools]]
name = "VS Code"
cmd = "code"
args = ["{path}"]
key = "o"
mode = "detach"
enabled = true
```

`compose_overrides` (a per-file project name and env files for loose layouts) is read but not applied yet. State that isn't config, the record of each build, lives in the OS cache folder.

## 7. Package layout

```
cmd/dockgit/        main: flags, config, the program
internal/
  config/           TOML config (from folgit)
  cache/            build records
  dock/             Runner and the docker CLI: containers, images, volumes, events, stats, logs, disk use, prune
  compose/          compose file discovery, parsing (services, ports, variables, x-dockgit), env layering, command lines, path matching
  doctor/           layout checks for a compose root
  link/             containers ↔ repos ↔ stacks, and the checks before starting
  registry/         image registry API: tags, digests, version comparison
  updates/          image update checks and version bumps
  job/              runs git/docker steps as one streamed task
  gitinfo/          git status (from folgit), branches, the non-interactive environment
  launcher/         tools, browser, zellij (from folgit)
  match/            remote URLs (from folgit)
  ui/               the Bubble Tea app: one file per tab and view, plus dialogs, help, status bar
```

## 8. Build cache cleanup

After each build dockgit runs (the default), or when `docker system df` reports the build cache above the threshold (checked at startup and after builds), or never:

1. `docker builder prune -f --reserved-space <keep_storage> --filter until=<older_than>`
2. if `prune_dangling_images`: `docker image prune -f` (dangling only, never `-a`)
3. A toast says what was freed; nothing is said when nothing was.

`C` on the Docker tab shows disk use and prunes by hand, each action asking first.

## 9. Concurrency and refresh

- Every slow call is a `tea.Cmd` that returns a message; generation counters drop results from streams or loops that were replaced.
- One `docker events` stream drives the container list: a lifecycle event re-inspects that container only, and a destroyed container can't be brought back by an inspect that started before it. If the stream drops, it reconnects with backoff (2 s doubling to 30 s) and reloads the full list.
- Git status refreshes when the Repos tab opens, after git actions and on `r`; compose roots are rescanned when the Compose tab opens.
- Jobs run one at a time per repo or stack; the status bar lists running jobs.
- No startup cache: measured, containers load in about 220 ms, 8 repos' git status in about 120 ms and 30 stacks in about 120 ms, so a cache would only show stale data. A remote Docker host over SSH might change that.

## 10. Milestones

Each milestone ended with something usable, plus tests.

| | Milestone | Done when |
|---|---|---|
| M0 | Skeleton: four tabs, help, Settings, config | the app starts, switches tabs and themes in light and dark terminals |
| M1 | Docker tab, read-only: list, detail pane, events | a container started or stopped elsewhere shows up within a second (live test) |
| M2 | Docker actions, logs viewer, shell, images, volumes, stats | everything Docker Desktop's container list does can be done here |
| M3 | Repos tab: branches, builds, jobs, build records | `b` → pick a branch → the containers are rebuilt from it with no other input (live test) |
| M4 | Compose tab: roots, stacks, env layering, doctor, new stacks | a fresh clone of a stack-layout repo can create its `.env` and bring a stack up with both env files applied (live test) |
| M5 | Linking: sources, stale builds, checks before starting | starting a stack whose host port another container holds names that container and offers to stop it (live test) |
| M6 | Cleanup, disk use, image update checks, Settings values | — |
| M7 | Polish: every screen fits from 40×10 to 200×40, README and screenshots, CI, releases | — |

## 11. Testing

- **Unit:** each `docker` command's output parsed from recorded fixtures (`testdata/`), including Windows path forms; compose discovery, parsing and the doctor against synthetic layouts; `link` matching and checks; the registry client against a fake registry.
- **UI:** tests drive the app with key presses and a fake `Runner`, then check state and rendered lines. One test renders every screen, popup and dialog at many sizes and checks it fits exactly.
- **Live (opt-in):** `DOCKGIT_IT=1` runs tests against the real daemon that create their own containers, images and projects, clean up after themselves, and turn automatic cleanup off so nothing else is pruned.
- **Screenshots:** `DOCKGIT_SCREENSHOTS=docs` draws the README's SVGs from a demo state.

## 12. Risks and open points

- **Windows path matching.** Compose labels may hold `C:\Users\...`, `/mnt/c/Users/...` or `/run/desktop/mnt/host/c/...` depending on how the stack was started. All are normalized, with tests.
- **Loose layouts.** Several files in one folder share a project name; dockgit tracks state per file and never removes orphans, but the stack layout avoids the problem.
- **Update checks** only work for public images (anonymous registry tokens). Tag schemes vary: a version is only compared with tags of the same shape, and majors more than three ahead are ignored as another naming scheme.
- **Secrets in `.env`.** Values are masked in the UI and never logged or cached.
- **"Gone" files and "changed since up"** are judged on the machine dockgit runs on.

## Decisions

1. `b` on the branch that's already checked out still rebuilds. It doubles as a quick rebuild.
2. Switching branch on a running stack doesn't run `down` first. `up -d --build` recreates only what changed.
3. dockgit and folgit stay independent in v1: no shared repo list, and folgit's pins aren't read.
4. Stacks are flat (`stacks/<name>`) so dockge can manage them, with shared values in `stacks/global.env`.
5. Each project's source goes on its heading on the Docker tab rather than in a column, since a project's containers all share it.
6. The "already running elsewhere" check needs the same image *and* service name; image alone flags unrelated stacks that both use redis or postgres.
7. Version bumps stay within the same major version; newer majors are only mentioned.

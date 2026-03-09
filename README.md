# galph

Autonomous coding loop driver for [Klaudia](../klaudia/) (Claude Code fork). Breaks a PRD into tasks, executes them in Docker via Klaudia, validates with build/test gates, and commits on success.

## Prerequisites

- **Go 1.24+** — to build galph
- **Docker** — galph runs klaudia in a container
- **Klaudia** — built Claude Code fork (`npm install && node build.mjs` in the `klaudia/` directory)
- **Claude auth** — API key, OAuth token (from Claude Code login), or Max subscription

## How it works

```
galph run
  1. Build Docker image (if needed)
  2. Start container with workspace + klaudia mounted
  3. Plan: read PRD.md → ask klaudia to break into tasks → save plan
  4. Execute loop:
     a. Pick next pending task
     b. Run klaudia --dangerously-skip-permissions in container
     c. Run test gate (go test, npm test, etc.)
     d. Pass → mark complete, git commit
     e. Fail → mark failed, try next task
     f. Repeat until all tasks done or max iterations
```

## Quick start

### New project

```bash
galph new my-app
cd my-app
# Edit PRD.md with your requirements
# Edit .galphrc to set test_command
galph run
```

### Existing project

```bash
cd my-existing-project
galph init --describe "Add user authentication with JWT tokens"
galph run
```

`galph init` detects your project type (Go, Node, Python, Rust, C/C++, Dart/Flutter) and auto-configures the test command.

## Commands

| Command | Description |
|---------|-------------|
| `galph new <name>` | Create a new project directory with PRD.md, CLAUDE.md, .galphrc |
| `galph init [--describe "..."]` | Add galph to an existing project in the current directory |
| `galph plan` | Run only the planning phase (generates .galph/plan.json) |
| `galph run` | Run the full plan + execute loop |
| `galph status` | Show current progress, cost, and change detection |
| `galph logs [N]` | Show iteration output (latest or specific) |

## Configuration

galph reads `.galphrc` (JSON) from the project directory:

```json
{
  "project_name": "my-app",
  "workspace": ".",
  "prd": "PRD.md",
  "max_iterations": 50,
  "max_consecutive_failures": 3,
  "test_command": "go build ./... && go test ./... && go vet ./...",
  "model": "claude-sonnet-4-6",
  "docker": {
    "image": "galph-klaudia",
    "memory": "4g",
    "network": "host"
  }
}
```

CLI flags override config values. Run `galph help` for the full list.

## Smart change detection

galph hashes your planning inputs (PRD.md, CLAUDE.md, .galphrc) using SHA-256. On each run:

- **No plan exists** — generates a fresh plan
- **Plan exists, inputs unchanged** — resumes where it left off
- **Plan exists, inputs changed** — additive replan: preserves completed tasks, plans only remaining work

```bash
# Edit PRD.md to add new requirements...
galph status
#   ⚠ plan inputs changed — next 'galph run' will replan

galph run
#   ▶ Replanning phase (inputs changed, preserving 2 completed tasks)
```

## Recovery & interactive fixes

If galph misses part of the spec or produces broken code, you have several ways to recover:

### Re-run galph (retry failed tasks)

Failed tasks are retried on the next run. If the failure was transient, this is often enough:

```bash
galph run
```

The `max_consecutive_failures` setting (default 3) prevents infinite retry loops.

### Refine the PRD and re-run (replan)

Clarify or fix the spec, then let smart change detection handle the rest:

```bash
vim PRD.md          # fix the ambiguous requirement
galph run           # detects hash change → replans, preserving completed tasks
```

This is the cleanest path — galph keeps everything that already passed and only plans the remaining/new work.

### Fix code interactively, then resume

Launch klaudia (or Claude Code) directly in your workspace for surgical fixes:

```bash
cd my-project
CLAUDECODE= node ../klaudia/dist/cli.js    # interactive klaudia session
# or use Claude Code if installed
```

Then resume galph for the remaining tasks:

```bash
galph run           # picks up where it left off, your manual fix is in place
```

Since galph commits per-task, your manual edits sit cleanly on top of completed work.

### Fix code manually

For small issues, just edit the file yourself:

```bash
vim cmd/server/main.go    # fix the bug
galph run                 # continues with remaining tasks
```

### Recommended recovery workflow

1. **First try**: just `galph run` again (retry)
2. **Spec was unclear**: edit PRD.md → `galph run` (triggers replan)
3. **Need precision**: launch klaudia interactively, fix it, then `galph run` for remaining tasks

All paths converge — galph picks up the current state and keeps going.

## Klaudia setup

galph needs a built copy of [Klaudia](../klaudia/) — the Claude Code fork that does the actual coding work. Klaudia is mounted read-only into the Docker container at `/klaudia`.

### Building klaudia

```bash
cd klaudia
npm install
node build.mjs        # builds src/cli.js → dist/cli.js
```

galph invokes klaudia as `node /klaudia/dist/cli.js` inside the container, so `dist/cli.js` must exist.

### How galph finds klaudia

galph resolves the klaudia directory in this order:

1. **Explicit config** — `klaudia_dir` in `.galphrc` or `--klaudia-dir` CLI flag
2. **Sibling of the galph binary** — `../klaudia/` relative to the `galph` executable
3. **Sibling of the working directory** — `../klaudia/` relative to CWD

It validates by checking for `dist/cli.js` inside the resolved directory. If none found:

```
galph: setup: finding klaudia: cannot find klaudia installation
  (set klaudia_dir in .galphrc or place klaudia/ as sibling)
```

### Typical layout

The simplest setup is having galph and klaudia as siblings:

```
projects/
├── klaudia/              # Built klaudia (npm install && node build.mjs)
│   └── dist/cli.js
├── galph/                # galph source + binary
│   └── galph
└── my-project/           # Your project
    ├── .galphrc
    └── PRD.md
```

Running `../galph/galph run` from `my-project/` will auto-discover `../klaudia/`.

For non-standard layouts, set `klaudia_dir` explicitly:

```json
{
  "docker": {
    "klaudia_dir": "/opt/klaudia"
  }
}
```

## Docker architecture

galph runs Klaudia inside a Docker container with these mounts:

| Host | Container | Mode | Purpose |
|------|-----------|------|---------|
| Project directory | `/workspace` | rw | Your source code |
| Klaudia installation | `/klaudia` | ro | Claude Code fork binary |
| `~/.claude` | `/home/node/.claude` | rw | Auth and config |
| `~/.claude.json` | `/home/node/.claude.json` | rw | OAuth credentials |
| `.galph/` | `/home/node/.galph` | rw | Run state persistence |

The container includes Node.js 20, Go 1.24, git, gcc, and build essentials. Container names are per-project (`galph-<name>-<hash>`) so multiple instances can run in parallel.

### Authentication

galph forwards auth automatically:
- `ANTHROPIC_API_KEY` env var (if set and non-empty)
- OAuth token from macOS keychain (`Claude Code-credentials`)
- `CLAUDE_CODE_OAUTH_TOKEN` env var (explicit override)

## State files

All state lives in `.galph/` (gitignored by default):

```
.galph/
├── state.json              # Run metadata, iteration count, cost, plan hash
├── plan.json               # Task list with statuses
└── iterations/
    ├── 001.json            # Per-iteration: prompt, output, cost, duration
    ├── 002.json
    └── ...
```

## Building

```bash
# Build klaudia (one-time)
cd klaudia && npm install && node build.mjs && cd ..

# Build galph
cd galph && go build -o galph ./cmd/galph/

# Build the Docker image (one-time, galph does this automatically on first run)
docker build -t galph-klaudia -f galph/Dockerfile galph/
```

## Project layout

```
galph/
├── cmd/galph/main.go           # CLI entry point
├── internal/
│   ├── runner/runner.go        # Core loop: plan → execute → test → commit
│   ├── docker/docker.go        # Container lifecycle
│   ├── parser/stream.go        # Parse --output-format stream-json
│   ├── state/progress.go       # State persistence
│   ├── hasher/hasher.go        # SHA-256 change detection
│   ├── display/display.go      # Terminal UI
│   └── config/config.go        # .galphrc loading
├── Dockerfile                  # Klaudia container image
├── docker-compose.yml          # Dev compose config
└── go.mod
```

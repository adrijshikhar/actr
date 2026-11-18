# actr — Wiki & Adoption Guide

Welcome to the **actr** Wiki and Adoption Guide. This reference documents how to install, register, adopt, and operate `actr` as a high-performance native self-hosted runner daemon for GitHub Actions workflows.

---

## Table of Contents
1. [The 5-Minute Adoption Path](#1-the-5-minute-adoption-path)
2. [CI Workflow Integration & `runs-on` Conventions](#2-ci-workflow-integration--runs-on-conventions)
3. [Architecture & Runtime Engine](#3-architecture--runtime-engine)
4. [Marketplace Actions & Node Runtime Engine](#4-marketplace-actions--node-runtime-engine)
5. [Workflow Command Files & Toolchain Ingestion](#5-workflow-command-files--toolchain-ingestion)
6. [Post-Step Lifecycle Coordination](#6-post-step-lifecycle-coordination)
7. [Local Run & Log Persistence Layout](#7-local-run--log-persistence-layout)
8. [Day-2 Operations & Service Management](#8-day-2-operations--service-management)
9. [Troubleshooting & FAQ](#9-troubleshooting--faq)

---

## 1. The 5-Minute Adoption Path

Adopting `actr` requires **zero changes to your existing CI steps, scripts, or Marketplace actions**. You only need to install `actr`, register your machine as a runner, and update the `runs-on` field in your workflow YAML.

### Step 1: Install `actr`

Choose your preferred installation method:

#### Option A: Homebrew Tap (macOS & Linux, Recommended)
```bash
brew tap adrijshikhar/tap
brew install --cask actr
```

#### Option B: Standalone Installer (macOS & Linux)
Runs automated SHA256 checksum verification and installs the static binary to `/usr/local/bin` (or `~/.local/bin`), with automatic shell completions for Zsh, Bash, and Fish:
```bash
curl -fsSL https://raw.githubusercontent.com/adrijshikhar/actr/main/install.sh | sh
```

#### Option C: Pre-Compiled Binary Download
Download the static tarball for your platform from [GitHub Releases](https://github.com/adrijshikhar/actr/releases/latest):
```bash
# Example for macOS Apple Silicon
curl -LO https://github.com/adrijshikhar/actr/releases/latest/download/actr_darwin_arm64.tar.gz
tar -xzf actr_darwin_arm64.tar.gz
sudo mv actr /usr/local/bin/
```

---

### Step 2: Pre-Flight Verification (`actr doctor`)

Run `actr doctor` to ensure your environment has the necessary prerequisites:

```bash
actr doctor
```

**Expected output:**
```text
[✓] OS & Architecture: darwin/arm64
[✓] Git: git version 2.54.0
[✓] Node.js (for JS actions): v26.12.1
```

- **OS & Architecture**: Confirms OS (`darwin`/`linux`) and CPU (`arm64`/`amd64`).
- **Git**: Required for repository checkout and remote tracking.
- **Node.js**: Required to execute JavaScript Marketplace Actions (`actions/setup-go`, `actions/setup-node`, `pnpm/action-setup`, etc.). If not found in standard PATH, `actr` will automatically discover Node under `fnm`, `nvm`, `asdf`, `mise`, or Homebrew.

---

### Step 3: Register the Runner (`actr register`)

`actr` provides zero-click registration that automatically detects your repository and obtains registration tokens via the GitHub CLI (`gh`).

#### Interactive Mode (Recommended for Developers)
Inside any local clone of your repository, run:
```bash
actr register
```
`actr` will:
1. Detect your repository URL from `git remote get-url origin`.
2. Query `gh` for an ephemeral runner registration token (prompting for browser authentication if `gh` is unauthenticated).
3. Assign default system labels (`self-hosted`, `macOS` or `Linux`, `ARM64` or `X64`).
4. Save credentials safely into `~/.actr/` (`0600` file permissions).
5. Prompt to install and start the background OS service.

#### Non-Interactive / Headless Mode (For CI, Servers, or Automation)
```bash
actr register \
  --url https://github.com/your-org/your-repo \
  --token <GITHUB_RUNNER_REGISTRATION_TOKEN> \
  --name my-workstation \
  --labels "self-hosted,macOS,ARM64"
```
> *Tip:* You can generate a registration token manually in GitHub: **Repository Settings → Actions → Runners → "New self-hosted runner"**.

---

### Step 4: Install & Start the Background Service

`actr` runs as an unattended background service using your operating system's native service supervisor (`launchd` on macOS, `systemd` on Linux).

```bash
# 1. Install the service definition
actr service install

# 2. Start the runner daemon
actr service start

# 3. Verify it is running and listening for jobs
actr service status
```

**Expected status output:**
```text
Service status: running (PID 13960)
Listening for Jobs
```

The daemon runs continuously in the background, surviving logouts and automatically starting on system boot.

---

### Step 5: Adopt in Your CI Workflow (The "1-Line Change")

Open your workflow file (e.g. `.github/workflows/ci.yml`). Change only the `runs-on` line to target your `actr` runner:

#### Before (GitHub Cloud Runner):
```yaml
jobs:
  build-and-test:
    runs-on: macos-latest
    steps:
      - uses: actions/checkout@v7
      - uses: actions/setup-go@v7
        with:
          go-version-file: 'go.mod'
          cache: true
      - run: go test -v ./...
```

#### After (Routing to `actr`):
```yaml
jobs:
  build-and-test:
    runs-on: [self-hosted, macOS]
    steps:
      - uses: actions/checkout@v7
      - uses: actions/setup-go@v7
        with:
          go-version-file: 'go.mod'
          cache: true
      - run: go test -v ./...
```

Push this change to a branch and open a Pull Request. GitHub will immediately route the job to your `actr` runner!

---

### Step 6: Live Monitoring & Tactical TUI (`actr monitor`)

Attach to the background daemon anytime using `actr monitor` without interrupting running jobs:

```bash
actr monitor
```

The interactive TUI provides complete situational awareness:
- **Explorer Tree**: Navigate hierarchically across `Workflow` → `Matrix` → `Job` → `Steps`.
- **Live Telemetry**: Real-time CPU usage, RSS memory consumption, and execution duration.
- **Streaming Logs**: ANSI-colored output streamed in real time.
- **Keybindings**:
  - `↑` / `k`, `↓` / `j`: Move up and down the step tree.
  - `Enter` / `Space`: Expand or collapse step trees.
  - `z`: **Zoom** — toggles log view to 100% full terminal width for viewing stack traces and compiler errors.
  - `f`: Toggle auto-follow (tail) active step logs.
  - `q`: Detach from the monitor (the runner daemon continues processing in the background).

---

## 2. CI Workflow Integration & `runs-on` Conventions

### Why `[self-hosted, macOS]` and Not `macos`?

When routing CI jobs to an `actr` runner, you must specify the runner labels correctly in your workflow's `runs-on` field.

```yaml
# Recommended single runner target
runs-on: [self-hosted, macOS]

# Or in a build matrix
strategy:
  matrix:
    os: [ [self-hosted, macOS] ]
runs-on: ${{ matrix.os }}
```

Understanding this syntax requires understanding GitHub's runner routing rules:

#### 1. Label Matching is Strictly Case-Sensitive
GitHub Actions treats all runner labels with **strict case sensitivity**.
When an `actr` runner registers on an Apple machine, GitHub's registration API automatically attaches default, read-only system labels:
```json
[
  { "name": "self-hosted" },
  { "name": "macOS" },
  { "name": "ARM64" }
]
```
Notice that GitHub's official system label is **`macOS`** (capital `M`, capital `OS`), **not** lowercase `macos`.

> [!WARNING]
> If your workflow requests `runs-on: [self-hosted, macos]` (all lowercase), GitHub Actions will fail to find a matching runner. Your job will remain indefinitely in the **`Queued`** state waiting for a runner explicitly labeled `macos`.

#### 2. Label Array Conjunction (AND Logic)
When an array of labels is passed to `runs-on`:
```yaml
runs-on: [self-hosted, macOS]
```
GitHub Actions treats this as a logical **AND**: the runner must possess **all** listed labels. This guarantees that:
- The job **never** runs on GitHub-hosted cloud virtual machines.
- The job only dispatches to your designated local macOS `actr` runner.

#### 3. `macos-latest` vs. Self-Hosted `macOS`
- `macos-latest` is an alias reserved by GitHub to route jobs to GitHub-hosted cloud runner pools.
- If your repository has access to GitHub-hosted runners, specifying `runs-on: macos-latest` will match GitHub's cloud VMs first.
- To guarantee the job executes on your local `actr` hardware, always include the `self-hosted` label:
  ```yaml
  runs-on: [self-hosted, macOS]
  ```

> [!WARNING]
> **Security Notice for Public Repositories & Forks:**  
> Because `actr` executes workflows directly on the host machine, never allow untrusted public pull requests from forks to execute on a self-hosted runner. For public repositories, route public fork pull requests to GitHub-hosted cloud virtual machines (`macos-latest`), and reserve `[self-hosted, macOS]` strictly for internal branches, `main` branch pushes, or manual `workflow_dispatch` runs.

#### 4. Dual-Target Matrix Example (Cloud + Local)
To support both GitHub Cloud Linux runners and your local `actr` macOS runner in the same workflow matrix:

```yaml
jobs:
  test:
    name: Test (${{ matrix.platform }})
    timeout-minutes: 25
    runs-on: ${{ matrix.runner }}
    strategy:
      fail-fast: false
      matrix:
        include:
          - platform: Linux
            runner: ubuntu-latest

          # Public fork PRs run on safe cloud runners; trusted repo PRs or main pushes run on actr
          - platform: macOS
            runner: ${{ (github.event_name == 'push' || (github.event_name == 'pull_request' && github.event.pull_request.head.repo.full_name == github.repository) || (github.event_name == 'workflow_dispatch' && inputs.use_actr)) && fromJSON('["self-hosted", "macOS"]') || 'macos-latest' }}
      matrix:
        include:
          - os-name: Linux
            runner: ubuntu-latest
          - os-name: macOS (actr)
            runner: [self-hosted, macOS]
    runs-on: ${{ matrix.runner }}
    steps:
      - uses: actions/checkout@v7
      - uses: actions/setup-go@v7
        with:
          go-version-file: 'go.mod'
          cache: true
      - run: go test -v ./...
```

---

## 3. Architecture & Runtime Engine

`actr` is built from the ground up to replace the heavyweight official .NET CoreCLR runner (`~450 MB` disk, `~350 MB` idle RAM) with a single, statically compiled Go binary (`~11 MB` disk, `< 25 MB` idle RAM).

### Core Architectural Components

```
                          GitHub Actions Broker & Pipeline Service
                                             ▲
                                             │ HTTPS (Long-Poll / OAuth RSA)
                                             ▼
┌─────────────────────────────────────────────────────────────────────────────┐
│ actr Daemon Service (launchd / systemd)                                      │
│                                                                             │
│  ┌───────────────────────┐         ┌─────────────────────────────────────┐  │
│  │   Broker Client       │         │   RunService Client                 │  │
│  │ (Session Auto-Renew)  │         │ (Job Acquisition, Timeline, Logs)   │  │
│  └───────────┬───────────┘         └──────────────────┬──────────────────┘  │
│              ▼                                        ▼                     │
│  ┌───────────────────────────────────────────────────────────────────────┐  │
│  │ Coordinator (Execution Engine)                                        │  │
│  │                                                                       │  │
│  │  • Process Group Lifecycles (Setpgid / Killpg)                        │  │
│  │  • Cumulative Environment Management ($PATH, $GITHUB_ENV)             │  │
│  │  • Toolchain Ingestion ($GITHUB_PATH)                                 │  │
│  │  • Post-Step Hook Stack (LIFO, context.WithoutCancel)                 │  │
│  └───────────┬────────────────────────────────────────┬──────────────────┘  │
│              │                                        │                     │
│              ▼                                        ▼                     │
│  ┌───────────────────────┐         ┌─────────────────────────────────────┐  │
│  │ Shell Step Driver     │         │ Marketplace Action Engine (Node.js) │  │
│  │ (Host bash / sh / zsh)│         │ (Tarball Resolver + Core Emulation) │  │
│  └───────────────────────┘         └─────────────────────────────────────┘  │
└──────────────────────────────────────┬──────────────────────────────────────┘
                                       │ Unix Domain Socket (~/.actr/daemon.sock)
                                       ▼
┌─────────────────────────────────────────────────────────────────────────────┐
│ Interactive Monitor TUI (actr monitor)                                      │
│                                                                             │
│  • Workflow Tree Explorer (Workflow > Matrix > Job > Steps)                 │
│  • Real-time Telemetry (Process CPU %, RSS Memory RAM, I/O rates)           │
│  • Streaming ANSI Log Viewer with Single-Key Zoom ('z')                      │
│  • Offline History Browser (Reads directly from ~/.actr/runs/)              │
└─────────────────────────────────────────────────────────────────────────────┘
```

---

## 4. Marketplace Actions & Node Runtime Engine

`actr` features a built-in Marketplace Actions engine that executes standard JavaScript/TypeScript actions (`using: 'node20'`, `using: 'node16'`) directly on the host using the local Node runtime.

### Action Resolution & Caching
1. **Repository Tarball Download**:
   When a step references `uses: actions/setup-go@v7`, `actr` requests the release tarball directly from GitHub codeload endpoints.
2. **Advisory Locking**:
   Downloads are protected by cross-platform advisory file locks (`<version>.lock`) using non-blocking kernel locks with context timeout polling (`ctx.Done()`). If multiple parallel matrix jobs request the same action simultaneously, only one downloads while others wait, eliminating multi-process race conditions.
3. **Completion Markers**:
   Extracted actions are committed with a `<version>.completed` marker. Subsequent runs resolve in `< 1ms` directly from local cache at:
   ```text
   ~/.actr/_work/_actions/<owner>/<repo>/<version>/
   ```
4. **Security & Zip Slip Protection**:
   Archive extraction validates every file path against the target directory using `filepath.EvalSymlinks` to prevent symlink traversal attacks, rejecting any entries escaping the destination root. Decompression bombs are guarded by strict limits: `500 MB` maximum extraction size, `100 MB` per file, and `20,000` entries maximum.

### `@actions/core` Dual-Key Input Mapping
Official GitHub Actions expect inputs formatted as environment variables. However, action implementations query them in varying formats:
- `@actions/core` uses exact uppercase: `INPUT_GO-VERSION` (hyphens preserved).
- Shell scripts and standard POSIX tools often query: `INPUT_GO_VERSION` (hyphens replaced with underscores).

`actr` automatically emits **both** keys into the action's execution environment:
```text
INPUT_GO-VERSION=1.24
INPUT_GO_VERSION=1.24
```
Default values defined in `action.yml` are automatically resolved and injected if not explicitly provided by the workflow step.

### Tool Cache & Setup Actions (`RUNNER_TOOL_CACHE`)
Actions such as `actions/setup-node`, `actions/setup-python`, and `pnpm/action-setup` rely on the `@actions/tool-cache` toolkit to download and reuse toolchains. `actr` configures and manages this cache directory automatically:

```text
~/.actr/_work/_tool/
├── node/
│   └── 22.23.5/
│       └── arm64/
│           └── bin/node
```

The runner automatically injects the following standard environment variables into every step:
- `RUNNER_TOOL_CACHE`: Absolute path to `~/.actr/_work/_tool`.
- `AGENT_TOOLSDIRECTORY`: Alias for `RUNNER_TOOL_CACHE`.
- `RUNNER_TEMP`: Isolated temp directory (`~/.actr/_work/_temp`).
- `RUNNER_WORKSPACE`: Base work folder (`~/.actr/_work`).
- `RUNNER_OS`: `macOS`, `Linux`, or `Windows`.
- `RUNNER_ARCH`: `ARM64`, `X64`, etc.
- `RUNNER_ENVIRONMENT`: `self-hosted`.

---

## 5. Workflow Command Files & Toolchain Ingestion

GitHub Actions communicate dynamically between steps using special filesystem command files. `actr` creates unique, isolated temp files for every step and automatically ingests them upon step completion:

| Variable | File Purpose | Ingestion Behavior in `actr` |
|---|---|---|
| `$GITHUB_PATH` | Line-delimited directories to add to `$PATH` | Directories are sanitized, validated, and prepended to `$PATH` for all subsequent steps. |
| `$GITHUB_ENV` | Key-value environment variables | Parsed (supporting single-line `KEY=VAL` and EOF delimiter multiline blocks) and merged into cumulative step environment. |
| `$GITHUB_STATE` | Persisted state for post-step cleanup | Captured in the step execution record and passed as `STATE_*` variables to the action's post hook. |
| `$GITHUB_OUTPUT` | Step outputs accessible via `${{ steps.<id>.outputs.* }}` | Captured in memory and propagated to subsequent step expressions. |

---

## 6. Post-Step Lifecycle Coordination

Actions frequently require cleanup or caching after the job completes (e.g. `actions/setup-go` saving cache tarballs or `actions/upload-artifact` finalizing uploads).

1. **LIFO Stack (Reverse Execution Order)**:
   Post hooks are queued in order of appearance and executed in **reverse (Last-In, First-Out)** order at job completion.
2. **Context Decoupling (`context.WithoutCancel`)**:
   Even if a job is cancelled by the user or times out, post-step hooks execute with an independent 5-minute timeout window via `context.WithoutCancel(jobCtx)`.
3. **State Restoration**:
   Values written to `$GITHUB_STATE` during the main execution step are automatically injected into the post-step script as:
   ```text
   STATE_<KEY>=<VALUE>
   ```

---

## 7. Local Run & Log Persistence Layout

`actr` stores past executions on disk under `~/.actr/runs/`:

```text
~/.actr/runs/
├── index.json                  # Index catalog of all runs (IDs, status, duration, vitals)
└── <job-id>/
    ├── meta.json               # Full execution metadata (peak CPU, peak RSS, exit codes)
    └── steps/
        ├── <step-1>.log        # Raw stdout/stderr stream preserving ANSI colors
        └── <step-2>.log
```

### Automatic LRU Retention
Log retention is automatically pruned based on configurable limits in `~/.actr/config.yaml`:
```yaml
retention:
  max_runs: 50         # Retain up to 50 workflow runs
  max_disk_mb: 200     # Prune oldest runs when disk usage exceeds 200 MB
```

### Offline History Browsing
Even when the `actr` daemon service is stopped, you can browse all previous runs and ANSI step logs using:
```bash
actr monitor
```
The monitor boots in **Offline Mode** (`◉ OFFLINE (HISTORY)`). When the background service starts again, it automatically connects and resumes live streaming.

---

## 8. Day-2 Operations & Service Management

### Service Lifecycle Commands
```bash
actr service status     # Check background service status and active PID
actr service stop       # Stop the background runner daemon
actr service start      # Start the background runner daemon
actr service restart    # Restart daemon (or run: kill <PID> to trigger launchd restart)
actr service uninstall  # Remove launchd/systemd service definition
```

### Upgrading `actr`
To upgrade `actr` to the latest release:

```bash
# If installed via Homebrew:
brew update && brew upgrade actr

# If installed via install.sh:
curl -fsSL https://raw.githubusercontent.com/adrijshikhar/actr/main/install.sh | sh
```
After upgrading the binary, restart the daemon:
```bash
actr service stop && actr service start
```

### Multi-Runner Workspaces
If you run multiple distinct runners on the same machine (e.g. for different repositories or organizations), use the `--config-dir` flag to isolate their state:
```bash
actr register --config-dir ~/.actr-repo-b --url https://github.com/org/repo-b
actr run --config-dir ~/.actr-repo-b
```

### Cleaning Up Disk Usage
If you want to manually purge cached actions or downloaded toolchains:
```bash
rm -rf ~/.actr/_work/_actions/*     # Clear downloaded action cache
rm -rf ~/.actr/_work/_tool/*        # Clear cached toolchains (Node/Go/Python)
rm -rf ~/.actr/runs/*               # Clear historical logs
```

---

## 9. Troubleshooting & FAQ

### Q: Why is my job stuck in "Queued" on GitHub?
- **Verify Label Case**: Ensure your workflow specifies `runs-on: [self-hosted, macOS]` with exact case matching (`macOS`, not `macos`).
- **Check Service**: Run `actr service status`. If stopped, run `actr service start`.
- **Check GitHub API**: Run `gh api repos/<owner>/<repo>/actions/runners` and verify your runner reports `"status": "online"`.

### Q: Can I run both Docker and Host steps?
Yes. By default, `actr` executes steps directly on the host using your native compilers and runtimes. When container steps are requested, `actr` seamlessly drives the local Docker daemon without requiring container overhead for simple host steps.

### Q: How do I view logs in real time from the command line?
Run `actr monitor` for the rich TUI dashboard, or inspect the raw background daemon log at:
```bash
tail -f ~/.actr/daemon.log
```

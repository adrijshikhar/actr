# actr — Wiki & Technical Reference Guide

Welcome to the **actr** Wiki. This technical reference covers GitHub Actions workflow integration conventions, the runner runtime architecture, Marketplace Actions execution, environment emulation, and operational troubleshooting.

---

## 1. CI Workflow Integration & `runs-on` Label Conventions

The primary design goal of `actr` is **seamless, single-line CI integration**: any standard GitHub Actions workflow file (`.github/workflows/*.yml`) can be routed to an `actr` runner simply by updating its `runs-on` target, without changing any steps, scripts, actions, or credentials.

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

#### 4. Multi-Platform Matrix Example
To support both GitHub Cloud Linux runners and your local `actr` macOS runner in the same workflow matrix:

```yaml
jobs:
  test:
    name: Test (${{ matrix.os-name }})
    strategy:
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

## 2. Architecture & Runtime Engine

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

## 3. Marketplace Actions & Node Runtime Engine

`actr` features a built-in Marketplace Actions engine that executes standard JavaScript/TypeScript actions (`using: 'node20'`, `using: 'node16'`) directly on the host using the local Node runtime.

### Action Resolution & Caching
1. **Repository Tarball Download**:
   When a step references `uses: actions/setup-go@v7`, `actr` requests the release tarball directly from GitHub codeload endpoints.
2. **Advisory Locking (`syscall.Flock`)**:
   Downloads are protected by kernel-level file locks (`<version>.lock`). If multiple parallel matrix jobs request the same action simultaneously, only one downloads while others wait, eliminating multi-process race conditions.
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

## 4. Workflow Command Files & Toolchain Ingestion

GitHub Actions communicate dynamically between steps using special filesystem command files. `actr` creates unique, isolated temp files for every step and automatically ingests them upon step completion:

| Variable | File Purpose | Ingestion Behavior in `actr` |
|---|---|---|
| `$GITHUB_PATH` | Line-delimited directories to add to `$PATH` | Directories are sanitized, validated, and prepended to `$PATH` for all subsequent steps. |
| `$GITHUB_ENV` | Key-value environment variables | Parsed (supporting single-line `KEY=VAL` and EOF delimiter multiline blocks) and merged into cumulative step environment. |
| `$GITHUB_STATE` | Persisted state for post-step cleanup | Captured in the step execution record and passed as `STATE_*` variables to the action's post hook. |
| `$GITHUB_OUTPUT` | Step outputs accessible via `${{ steps.<id>.outputs.* }}` | Captured in memory and propagated to subsequent step expressions. |

---

## 5. Post-Step Lifecycle Coordination

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

## 6. Local Run & Log Persistence Layout

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

---

## 7. Troubleshooting & FAQ

### Q: Why is my job stuck in "Queued" on GitHub?
- **Check labels**: Ensure your workflow specifies `runs-on: [self-hosted, macOS]` with exact case matching (`macOS`, not `macos`).
- **Check daemon status**: Run `actr service status` to verify the background daemon is active.
- **Check runner online status**: Run `gh api repos/<owner>/<repo>/actions/runners` to confirm your runner reports `"status": "online"`.

### Q: How do I test my workflows locally without committing to `main`?
Create a feature branch in your repository, update `runs-on: [self-hosted, macOS]` in `.github/workflows/ci.yml`, and open a Pull Request. `actr` will acquire and execute the job automatically.

### Q: Where are runner logs located?
- Daemon background logs: `~/.actr/daemon.log`
- Live interactive dashboard: `actr monitor`
- Historical step logs: `~/.actr/runs/<job-id>/steps/`

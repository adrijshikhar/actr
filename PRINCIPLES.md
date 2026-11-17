# actr Core Principles

These principles guide architectural choices, design decisions, and trade-offs for `actr`. They define what `actr` is, what it refuses to become, and how it should behave in production.

---

## 1. Single Static Binary, Zero Bloat (The Anti-CoreCLR Principle)
- **Zero Runtime Dependencies:** Pure Go, statically compiled (`CGO_ENABLED=0`), zero CGO, zero dynamic library dependencies.
- **Microscopic Footprint:** The binary must remain under ~20 MB (vs. ~450 MB for the official .NET runner).
- **Sub-25 MB Idle RAM:** Idle daemon memory must stay below 25 MB (vs. 250–350 MB for .NET CoreCLR).
- **Instantaneous Startup:** Daemon startup and step launches execute in <10ms without virtual machine boot times, runtime JIT warmups, or heavy runtime overhead.

---

## 2. Self-Healing & Unattended Resilience
- **Zero-Touch Reconnection:** The daemon must run indefinitely without manual restarts.
- **Automatic Token Rotation:** GitHub Broker OAuth tokens expire every ~50 minutes. The broker client must detect HTTP 401 Unauthorized, automatically acquire a fresh RSA-signed assertion token, and retry seamlessly.
- **Session Self-Recovery:** When GitHub Broker invalidates a session (HTTP 400 `RunnerSessionInvalid`), the daemon must automatically establish a fresh session rather than looping endlessly or crashing.
- **Network Fault Tolerance:** Network blips, connection resets, and long-poll timeouts are expected lifecycle events, not fatal errors.

---

## 3. Total Workspace & Execution Hygiene
- **Zero Workspace Pollution:** The runner must never write internal runner scripts, wrappers, or temporary state files into the user's workspace directory (`.`). Internal runner files live exclusively in `RUNNER_TEMP` (`_work/_temp`).
- **Clean-Slate Guarantee:** Every job starts in a pristine, clean workspace directory. Leftover state from prior jobs must be cleaned before checkout or script execution.
- **Absolute Path Determinism:** Environment command files (`$GITHUB_ENV`, `$GITHUB_OUTPUT`, `$GITHUB_PATH`) and working directories must always resolve to absolute filesystem paths. A step script `cd`-ing into arbitrary subdirectories must never break runner command file writes.
- **Process Group Isolation:** Every step process runs in its own process group (`Setpgid: true`). On timeout, cancellation, or shutdown, termination signals (`SIGINT`/`SIGTERM`/`SIGKILL`) are sent to the entire process group (`-pgid`), preventing orphaned background processes.

---

## 4. Protocol-Native & AST-Accurate
- **Respect GitHub's Wire AST:** Never assume flat JSON string dictionaries for GitHub Actions workflow messages. Parse GitHub's pipeline syntax nodes (`displayNameToken`, uppercase `Key`/`Value` map structures, literal nodes) faithfully.
- **Human-First Naming:** Always prioritize user-defined, human-readable titles (`jobDisplayName`, step names) over internal platform target identifiers (such as `__default` or raw UUIDs).

---

## 5. Host-First Performance over Container Taxes
- **Bare-Metal Speed:** Favor direct host execution (`os/exec`) utilizing host-installed compilers, tools, and runtimes (Go, Rust, Node, Python, SDKMAN, Homebrew).
- **No Mandatory Container Tax:** Running a simple bash step or checkout should never require spinning up a Docker daemon or container runtime. Containers are an optional isolation layer, not an architectural tax.

---

## 6. Decoupled Observability & Tactical Telemetry
- **Uncoupled Monitoring:** The runner daemon runs headlessly as a background service (`launchd`/`systemd`). Monitoring dashboards connect and detach on demand over a local Unix domain socket IPC endpoint (`daemon.sock`) without interrupting running jobs.
- **Situational Awareness Over Raw Text:** The UI must present both the global workflow topology (the Explorer Tree: Workflow > Matrix > Job > Step lifecycle) and immediate telemetry (CPU %, RSS Memory, process state).
- **The Zen/Zoom Principle:** Provide the full hierarchy by default for situational awareness, but give users a single-key zoom (`z`) to immediately expand logs to 100% full terminal width for debugging compiler errors and stack traces.
- **ANSI Color Fidelity:** Preserve step ANSI color formatting and respect `NO_COLOR` standards.

---

## 7. Test-Driven Rigor & Verification Before Claims
- **TDD as Standard Operating Procedure:** No production protocol parser or engine fix is written without a failing test first.
- **Race-Free Concurrency:** All code must run clean under `go test -race ./...`.
- **Zero Lint Tolerance:** `golangci-lint` must remain at 0 issues.
- **Evidence Over Assertions:** Never claim a feature or fix works until verified with real command output against live GitHub Actions workflows.

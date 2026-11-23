<div align="center">

# actr

**⚡ Lightweight, native GitHub Actions runner daemon and interactive monitoring TUI.**

*Single static binary. Sub-25 MB RAM. Zero-click registration. Real-time telemetry.*

[![CI](https://github.com/adrijshikhar/actr/actions/workflows/ci.yml/badge.svg)](https://github.com/adrijshikhar/actr/actions/workflows/ci.yml)
[![Release](https://img.shields.io/github/v/release/adrijshikhar/actr)](https://github.com/adrijshikhar/actr/releases/latest)
[![Homebrew](https://img.shields.io/badge/homebrew-adrijshikhar%2Ftap-orange.svg)](https://github.com/adrijshikhar/homebrew-tap)
[![Go Version](https://img.shields.io/badge/Go-1.27+-00ADD8?style=flat&logo=go)](https://golang.org)
[![Zero CGO](https://img.shields.io/badge/CGO-disabled-success.svg)](https://golang.org)
[![License: MIT](https://img.shields.io/badge/license-MIT-blue)](LICENSE)

<br/>

<p align="center">
  <img src="docs/images/actr-monitor.png" alt="actr Interactive Monitor TUI" width="850" />
</p>

</div>

```bash
actr register              # zero-click registration (auto-detects repo & token via gh)
actr service install       # install background OS service (launchd on macOS, systemd on Linux)
actr service start         # start the runner daemon in the background
actr monitor               # launch interactive TUI dashboard (tree, telemetry, live logs)
actr secrets list          # list credentials stored in runner-local secret vault
actr doctor                # check prerequisites, git, node, and toolchain dependencies
```

---

## ✨ Features

- **Single static binary** — 100% pure Go (`CGO_ENABLED=0`), zero CGO, zero external binary runtime dependencies.
- **Bare-metal performance** — sub-25 MB idle RAM (vs 350+ MB for .NET CoreCLR); runs directly on native compilers without container overhead.
- **Zero-click registration** — automatically detects repository URL from `git remote origin` and acquires runner tokens via `gh` CLI.
- **Interactive TUI dashboard** — unified explorer tree (`Workflow` → `Job` → `Steps` + `Historical Runs`), live CPU/RAM/Net/Disk telemetry with expandable 4-graph Braille wave profiles (`e`), streaming ANSI logs, and single-key zoom (`z`).
- **Real-time host vitals HUD** — persistent status bar monitoring host CPU, RAM, disk headroom, and dynamic I/O rates (`net rx/tx`, `disk write`).
- **OpenTelemetry & metrics export** — stream live runner and host metrics directly to any OTLP collector (InfluxDB v3, Prometheus, Datadog, Grafana) via `--otel-endpoint`.
- **Local run & log persistence** — automatically preserves past job metadata, duration, peak vitals (CPU/RSS), and full ANSI-colored step logs under `~/.actr/runs/`.
- **Offline history browsing** — inspect historical runs anytime with `actr monitor` even when the daemon is stopped, with seamless background auto-reconnect when the runner starts.
- **Pluggable Runner Secret Vault** — zero-SDK credential management with native macOS Keychain out-of-the-box, authenticated Argon2id + AES-256-GCM file vaults, HashiCorp Vault KV v2, and AWS Secrets Manager.
- **Fork Guard & Automatic Masking** — default-deny security policy preventing secret resolution on untrusted fork PRs, with guaranteed `***` redaction across step logs and stdout.
- **Decoupled daemon architecture** — background daemon runs headlessly; terminal monitor attaches/detaches over a Unix socket (`~/.actr/daemon.sock`) without interrupting jobs.
- **Strict workspace hygiene** — credentials, runner state, and IPC socket live exclusively in `~/.actr/` (`0600` permissions), never polluting your repository.
- **Self-healing & unattended** — automated token refresh, session recovery, and process-group isolation (`-pgid`) on cancellation.

---

## 🚀 Quick Start

### Installation

#### Option 1: Standalone Installer (macOS & Linux, Recommended)
Run the one-line POSIX installer to automatically verify SHA256 checksums, install the binary to `/usr/local/bin` (or `~/.local/bin`), and configure shell completions for Zsh, Bash, and Fish:
```bash
curl -fsSL https://raw.githubusercontent.com/adrijshikhar/actr/main/install.sh | sh
```

#### Option 2: Homebrew Tap (macOS & Linux)
Install via the official verified Homebrew tap:
```bash
brew tap adrijshikhar/tap
brew install --cask actr
```

#### Option 3: Manual Binary Download
Download the latest pre-compiled archive for your OS and architecture from [GitHub Releases](https://github.com/adrijshikhar/actr/releases/latest):
- macOS Apple Silicon: `actr_<version>_darwin_arm64.tar.gz`
- macOS Intel: `actr_<version>_darwin_amd64.tar.gz`
- Linux ARM64: `actr_<version>_linux_arm64.tar.gz`
- Linux x86_64: `actr_<version>_linux_amd64.tar.gz`

Extract and move the binary to your `PATH`:
```bash
tar -xzf actr_*_darwin_arm64.tar.gz
sudo mv actr /usr/local/bin/
```

#### Option 4: From Source
Build directly with Go 1.27+:
```bash
git clone https://github.com/adrijshikhar/actr.git && cd actr
make install   # builds static binary and installs to ~/.local/bin/actr with zsh autocompletion
```

### 1. Register

Run `actr register` inside any Git repository:
```bash
actr register
```
It auto-detects your remote repository URL, acquires a registration token through `gh`, saves credentials to `~/.actr/`, and prompts to start the background service.

*For headless / CI environments:*
```bash
actr register --url https://github.com/owner/repo --token <TOKEN> --name my-runner
```

### 2. Manage Service

```bash
actr service install    # install OS background service (launchd / systemd)
actr service start      # start runner daemon
actr service status     # verify service is running
```

### 3. Monitor

Attach to the live daemon or browse historical runs offline:
```bash
actr monitor                  # connect to live daemon or browse history if offline
actr monitor --runs-dir <dir> # browse history from a custom runs directory
actr monitor --demo           # launch simulation showcasing live and historical runs
```

<p align="center">
  <img src="docs/images/actr-monitor.gif" alt="actr Interactive Monitor Demo" width="850" />
</p>

| Key | Action |
|---|---|
| `↑` / `k`, `↓` / `j` | Navigate items in the Explorer Tree |
| `Enter` / `Space` | Expand or collapse tree node |
| `e` | Expand / collapse 4-graph telemetry HUD (CPU, RAM, Net I/O, Disk Write) |
| `s` | Filter & search Explorer Tree |
| `Ctrl+F` / `/` | Search within active step logs |
| `c` | Copy highlighted / active step logs to system clipboard |
| `z` | Toggle 100% full-screen log zoom |
| `f` | Toggle auto-follow (tail) active step logs |
| `Tab` | Switch focus between Tree and Log Viewport |
| `q` / `Ctrl+C` | Detach monitor (daemon keeps running) |

---

## 🗄️ Local Run & Log Persistence

Every workflow execution processed by `actr` is automatically persisted on disk under `~/.actr/runs/` without requiring any external databases or background infrastructure.

### Storage Layout

```
~/.actr/runs/
├── index.json                  # Catalog index of all runs (ID, status, duration, step counts)
└── <job-id>/
    ├── meta.json               # Full execution record (peak CPU, peak RSS, duration, driver, limits)
    └── steps/
        ├── <step-1>.log        # Raw step stdout/stderr preserving ANSI colors and escapes
        └── <step-2>.log
```

### Offline History Browsing

You can inspect past workflow runs and step logs even when the runner daemon is stopped:
```bash
actr monitor
```
When `daemon.sock` is unreachable, `actr monitor` automatically launches in **Offline Mode** (`◉ OFFLINE (HISTORY)`):
- Displays completed runs grouped by repository under `HISTORICAL RUNS` in the unified Explorer Tree.
- Inspect peak vitals (peak CPU %, peak RSS RAM, total network I/O, disk writes) and duration in the HUD.
- Press `e` to expand multi-phase historical Braille execution profiles (CPU, Memory, Network, Disk).
- Step logs are retrieved directly from disk, preserving ANSI styling.
- Seamless auto-reconnect: if the daemon starts in the background, the monitor connects automatically and transitions to live streaming.

<p align="center">
  <img src="docs/images/actr-monitor-historical.png" alt="actr Offline History & Past Run Profiles" width="850" />
</p>

To browse runs from a custom runs directory:
```bash
actr monitor --runs-dir /path/to/custom/runs
```

### Retention & LRU Pruning

Configure automatic log and run pruning via `~/.actr/config.yaml`:

```yaml
retention:
  max_runs: 50         # Retain up to 50 runs (default: 50)
  max_disk_mb: 200     # Prune oldest runs when total logs exceed 200 MB (default: 200 MB)
```

When either limit is exceeded after a job finishes, `actr` safely deletes the oldest run directories and updates `index.json`.

### Crash Recovery

If the daemon is killed (`SIGKILL`, reboot, power loss) mid-run, any job left in `running` status is automatically recovered and marked as `interrupted` upon the next daemon start or monitor boot.

---

## 📡 OpenTelemetry & Metrics Export

Stream live runner execution and host vitals directly to any OpenTelemetry collector (InfluxDB v3, Prometheus, Datadog, Grafana Mimir, Jaeger):

```bash
# Push metrics via OTLP/HTTP to InfluxDB or local collector
actr run --otel-endpoint http://localhost:4318

# Push via gRPC with custom auth headers
actr run --otel-endpoint localhost:4317 --otel-protocol grpc --otel-headers "Authorization=Bearer <token>"
```

### Exported Gauges
| Metric | Description |
|---|---|
| `actr.step.cpu_percent` | Instantaneous CPU % consumed by the active step process group |
| `actr.step.memory_rss_bytes` | Resident Set Size (RSS) memory consumption |
| `actr.step.net_rx_rate_bytes_per_second` | Network receive throughput |
| `actr.step.net_tx_rate_bytes_per_second` | Network transmit throughput |
| `actr.step.disk_write_rate_bytes_per_second` | Step disk write I/O rate |
| `actr.host.cpu_percent` | Total host machine CPU utilization |
| `actr.host.memory_used_bytes` / `actr.host.memory_total_bytes` | Host RAM consumption & total capacity |
| `actr.host.disk_used_bytes` / `actr.host.disk_total_bytes` | Host storage utilization & capacity |

---

## 🔐 Runner Secret Vault & Pluggable Providers

`actr` includes a native, pluggable secret vault subsystem that resolves empty step environment variables (e.g. `DEPLOY_KEY: ""` from `${{ secrets.DEPLOY_KEY }}`) at runtime directly from runner-managed credential stores, without relying on external cloud SDKs or leaking plaintext credentials into logs.

### Out-of-the-Box Defaults (Zero Configuration)

When running without a custom `vault:` block in `config.yaml`, `actr` defaults automatically:
- **macOS (`darwin`)**: Hardware-backed **Apple Keychain** (`com.github.actr.secrets`) via `/usr/bin/security`. No passphrases or unencrypted key files required.
- **Linux & other OS**: Local encrypted vault at `~/.actr/vault.enc` using **AES-256-GCM** authenticated encryption with **Argon2id** key derivation (`0600` permissions, atomic rename).

### CLI Secret Management

Manage credentials locally on the runner:

```bash
# Interactive masked password prompt
actr secrets set DEPLOY_KEY

# Pass via flag
actr secrets set API_TOKEN --value "secret-token-xyz"

# Pipe from stdin / password managers
echo "my-super-secret" | actr secrets set NPM_TOKEN

# List stored keys and providers
actr secrets list

# Print decrypted value
actr secrets get DEPLOY_KEY

# Delete a key
actr secrets delete DEPLOY_KEY
```

### Pluggable Vault Adapters (`config.yaml`)

Configure single or cascading multi-vault fallback chains in `~/.actr/config.yaml`:

```yaml
vault:
  providers:
    # 1. Native OS Keychain (macOS security CLI, Windows DPAPI stubs)
    - type: keychain
      label: "com.github.actr.secrets"

    # 2. Authenticated AES-256-GCM + Argon2id encrypted file
    - type: encrypted-file
      path: "~/.actr/vault.enc"
      auth:
        passphrase: "runner-master-passphrase"

    # 3. HashiCorp Vault (pure-Go REST client, KV v2 engine)
    - type: hashicorp-vault
      address: "https://vault.internal.corp:8200"
      path: "actr/runner"
      auth:
        token: "s.xxxxxxxxxxxx"

    # 4. AWS Secrets Manager (pure-Go REST client, zero AWS SDK bloat)
    - type: aws-secrets-manager
      region: "us-east-1"
      prefix: "actr/ci/"
```

When multiple providers are defined, `actr` cascades through them in priority order:
- **Get**: Checks Provider 1 → Provider 2 → Provider 3 until found.
- **Set**: Writes to the first writable provider, bypassing read-only backends.
- **List**: Merges and deduplicates keys across all providers.

### Security Guarantees

1. **Fork Guard Execution Policy**:
   Untrusted pull requests (originating from forks where `GITHUB_HEAD_REF_REPO != GITHUB_REPOSITORY`) are strictly blocked from resolving secrets. An audit warning is logged and environment variables remain empty, preventing secret exfiltration through malicious PR workflows. Trusted `push`, `workflow_dispatch`, same-repo PRs, and local executions resolve secrets normally.
2. **Automatic Output Redaction**:
   All secrets resolved from the vault are registered with `SecretMasker` and dynamically sanitized to `***` across stdout, stderr, and step logs.
3. **Zero CGO & Zero Cloud SDKs**:
   Built with pure Go standard library primitives, maintaining instant startup, cross-compilation portability, and a lean ~24MB static binary.

---

## 🛠️ Usage

| Command | Description |
|---|---|
| `actr register` | Interactive zero-click runner registration wizard |
| `actr run` | Run runner daemon in foreground (listens for and executes jobs) |
| `actr monitor` | Open interactive TUI dashboard (connects to live daemon or browses offline history) |
| `actr secrets set <KEY>` | Store or update a secret in the vault (masked prompt, flag, or stdin pipe) |
| `actr secrets get <KEY>` | Retrieve and print a decrypted secret from the vault |
| `actr secrets list` | List all keys and provider names stored in the vault |
| `actr secrets delete <KEY>` | Remove a secret key from the vault |
| `actr service install` | Install native OS background daemon (`launchd` on macOS, `systemd` on Linux) |
| `actr service start` | Start the background daemon service |
| `actr service status` | Check service status (`running`, `installed/stopped`, `not installed`) |
| `actr service stop` | Stop the background daemon service |
| `actr service uninstall` | Remove background daemon service definition |
| `actr completion <shell>` | Generate shell completions (`zsh`, `bash`, `fish`, `powershell`) |
| `actr completion install [shell]` | Install shell completion script directly to your shell configuration directory |
| `actr doctor` | Validate OS, architecture, Git, and Node.js prerequisites |
| `actr --version` | Print version and commit build info |

---

## ⌨️ Shell Autocompletion

`actr` features rich, context-aware dynamic shell completion powered by Cobra:

- **Dynamic Secret Keys**: Tab-complete stored keys in `actr secrets get <KEY>` and `actr secrets delete <KEY>` resolved live from your active vault with timeout protection.
- **Contextual Runner Labels**: Tab-complete standard runner labels (`self-hosted`, `macOS`, `ARM64`, `linux`, `x64`) with descriptions, supporting comma-separated multi-label chaining (`--labels self-hosted,m<TAB>`).
- **Runner Instance Names**: Tab-complete configured runner and supervisor worker names (`actr register --name <TAB>`) discovered from local `config.yaml` or `.runner`.
- **Directory & File Directives**: Native shell tab completion for paths on `--config-dir`, `--work-folder`, `--log-dir`, `--runs-dir`, `--bin`, and `--sock`.

### Quick Installation

Automatically detect your active shell and install the completion file into standard site-functions directories:

```bash
actr completion install
```

Or specify your shell explicitly:

```bash
actr completion install zsh   # Installs to Homebrew site-functions or ~/.zsh/completions/_actr
actr completion install bash  # Installs to ~/.local/share/bash-completion/completions/actr
actr completion install fish  # Installs to ~/.config/fish/completions/actr.fish
```

After installation:
- **Zsh**: Run `autoload -Uz compinit && compinit` or open a new terminal window.
- **Bash / Fish**: Open a new terminal window or source the installed path.

### Manual Sourcing

Load completions temporarily into your current terminal session:

```bash
# Zsh
source <(actr completion zsh)

# Bash
source <(actr completion bash)

# Fish
actr completion fish | source

# PowerShell
actr completion powershell | Out-String | Invoke-Expression
```

---

## 📖 Architecture & Principles

`actr` follows strict design axioms documented in [**PRINCIPLES.md**](PRINCIPLES.md):
- Single static binary, zero bloat (sub-25 MB idle RAM vs. 350 MB for .NET CoreCLR).
- Total workspace hygiene (zero temporary wrapper scripts inside repo).
- Decoupled observability (IPC via Unix socket).

---

## 📄 License

[MIT](LICENSE) © [Adrij Shikhar](https://github.com/adrijshikhar)

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/adrijshikhar/actr/internal/config"
	"github.com/adrijshikhar/actr/internal/demo"
	"github.com/adrijshikhar/actr/internal/engine"
	"github.com/adrijshikhar/actr/internal/history"
	"github.com/adrijshikhar/actr/internal/logger"
	"github.com/adrijshikhar/actr/internal/monitor"
	"github.com/adrijshikhar/actr/internal/service"
	"github.com/adrijshikhar/actr/internal/vault"
	"github.com/adrijshikhar/actr/internal/web"
	"github.com/spf13/cobra"
)

const defaultWebPort = 18490

var execCommand = exec.Command

type webPIDInfo struct {
	PID       int       `json:"pid"`
	Port      int       `json:"port"`
	StartedAt time.Time `json:"started_at"`
}

func resolveDefaultPort() int {
	if envPort := os.Getenv("ACTR_WEB_PORT"); envPort != "" {
		if p, err := strconv.Atoi(envPort); err == nil && p > 0 && p <= 65535 {
			return p
		}
	}
	return defaultWebPort
}

func webPIDPath(cfgDir string) string {
	return filepath.Join(cfgDir, "web.pid")
}

func webLogPath(cfgDir string) string {
	return filepath.Join(cfgDir, "web.log")
}

func readWebPID(cfgDir string) (*webPIDInfo, error) {
	data, err := os.ReadFile(webPIDPath(cfgDir))
	if err != nil {
		return nil, err
	}
	var info webPIDInfo
	if err := json.Unmarshal(data, &info); err != nil {
		return nil, err
	}
	return &info, nil
}

func writeWebPID(cfgDir string, pid, port int) error {
	info := webPIDInfo{
		PID:       pid,
		Port:      port,
		StartedAt: time.Now().UTC(),
	}
	data, err := json.MarshalIndent(info, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(cfgDir, 0755); err != nil {
		return err
	}
	return os.WriteFile(webPIDPath(cfgDir), data, 0600)
}

func removeOwnWebPID(cfgDir string) error {
	info, err := readWebPID(cfgDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	if info != nil && info.PID != os.Getpid() {
		return nil
	}
	return removeWebPID(cfgDir)
}

func removeWebPID(cfgDir string) error {
	err := os.Remove(webPIDPath(cfgDir))
	if os.IsNotExist(err) {
		return nil
	}
	return err
}

func checkServerHealth(port int) bool {
	client := http.Client{Timeout: 400 * time.Millisecond}
	resp, err := client.Get(fmt.Sprintf("http://127.0.0.1:%d/api/status", port))
	if err != nil {
		return false
	}
	defer func() { _ = resp.Body.Close() }()
	return resp.StatusCode == http.StatusOK
}

func newWebCmd() *cobra.Command {
	var (
		port          int
		devMode       bool
		noOpen        bool
		foreground    bool
		demoMode      bool
		configDirFlag string
		sockPath      string
	)

	cmd := &cobra.Command{
		Use:   "web [flags]",
		Short: "Start and manage the actr web dashboard",
		Long: `Start the actr web dashboard HTTP server with REST APIs, live telemetry, and embedded UI.

By default, "actr web" runs as a background process and returns to your shell immediately.
To view or control the server, use:
  actr web status
  actr web stop

Examples:
  actr web
  actr web --demo
  actr web --no-open
  actr web --port 18490
  actr web --foreground`,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfgDir := resolveConfigDir(configDirFlag, cmd.Flags().Changed("config-dir"))
			if foreground {
				return runWebForeground(cmd, port, devMode, noOpen, demoMode, cfgDir, sockPath)
			}
			return startWebBackground(cmd, port, devMode, noOpen, demoMode, cfgDir, sockPath)
		},
	}

	cmd.Flags().IntVarP(&port, "port", "p", resolveDefaultPort(), "port to listen on (default 18490 or ACTR_WEB_PORT)")
	cmd.Flags().BoolVar(&devMode, "dev", false, "run in development proxy mode")
	cmd.Flags().BoolVar(&noOpen, "no-open", false, "do not open browser automatically")
	cmd.Flags().BoolVarP(&foreground, "foreground", "f", false, "run in foreground attached to terminal")
	cmd.Flags().BoolVar(&demoMode, "demo", false, "run web dashboard with simulated live workflow execution and telemetry")
	cmd.Flags().StringVar(&configDirFlag, "config-dir", config.DefaultConfigDir(), "path to directory containing runner configuration and daemon socket")
	cmd.Flags().StringVar(&sockPath, "sock", "", "path to daemon unix socket")

	_ = cmd.MarkFlagDirname("config-dir")
	_ = cmd.MarkFlagFilename("sock")

	cmd.AddCommand(
		newWebStartCmd(),
		newWebStopCmd(),
		newWebStatusCmd(),
	)

	return cmd
}

func newWebStartCmd() *cobra.Command {
	var (
		port          int
		devMode       bool
		noOpen        bool
		foreground    bool
		demoMode      bool
		configDirFlag string
		sockPath      string
	)

	cmd := &cobra.Command{
		Use:   "start [flags]",
		Short: "Start the actr web dashboard server",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfgDir := resolveConfigDir(configDirFlag, cmd.Flags().Changed("config-dir"))
			if foreground {
				return runWebForeground(cmd, port, devMode, noOpen, demoMode, cfgDir, sockPath)
			}
			return startWebBackground(cmd, port, devMode, noOpen, demoMode, cfgDir, sockPath)
		},
	}

	cmd.Flags().IntVarP(&port, "port", "p", resolveDefaultPort(), "port to listen on (default 18490 or ACTR_WEB_PORT)")
	cmd.Flags().BoolVar(&devMode, "dev", false, "run in development proxy mode")
	cmd.Flags().BoolVar(&noOpen, "no-open", false, "do not open browser automatically")
	cmd.Flags().BoolVarP(&foreground, "foreground", "f", false, "run in foreground attached to terminal")
	cmd.Flags().BoolVar(&demoMode, "demo", false, "run web dashboard with simulated live workflow execution and telemetry")
	cmd.Flags().StringVar(&configDirFlag, "config-dir", config.DefaultConfigDir(), "path to directory containing runner configuration and daemon socket")
	cmd.Flags().StringVar(&sockPath, "sock", "", "path to daemon unix socket")

	_ = cmd.MarkFlagDirname("config-dir")
	_ = cmd.MarkFlagFilename("sock")

	return cmd
}

func newWebStopCmd() *cobra.Command {
	var configDirFlag string

	cmd := &cobra.Command{
		Use:   "stop",
		Short: "Stop the background actr web dashboard server",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfgDir := resolveConfigDir(configDirFlag, cmd.Flags().Changed("config-dir"))
			info, err := readWebPID(cfgDir)
			if err != nil || !isProcessAlive(info.PID) {
				_ = removeWebPID(cfgDir)
				fmt.Fprintln(cmd.OutOrStdout(), "actr web dashboard is not running.")
				return nil
			}

			if err := terminateProcess(info.PID); err != nil {
				return fmt.Errorf("failed to terminate dashboard process (PID %d): %w", info.PID, err)
			}
			_ = removeWebPID(cfgDir)
			fmt.Fprintf(cmd.OutOrStdout(), "🛑 actr web dashboard stopped (PID %d)\n", info.PID)
			return nil
		},
	}

	cmd.Flags().StringVar(&configDirFlag, "config-dir", config.DefaultConfigDir(), "path to directory containing runner configuration and daemon socket")
	_ = cmd.MarkFlagDirname("config-dir")
	return cmd
}

func newWebStatusCmd() *cobra.Command {
	var configDirFlag string

	cmd := &cobra.Command{
		Use:   "status",
		Short: "Check the running status of the actr web dashboard",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfgDir := resolveConfigDir(configDirFlag, cmd.Flags().Changed("config-dir"))
			info, err := readWebPID(cfgDir)
			if err == nil {
				if isProcessAlive(info.PID) {
					if checkServerHealth(info.Port) {
						fmt.Fprintf(cmd.OutOrStdout(), "● actr web dashboard is running at http://127.0.0.1:%d (PID %d)\n", info.Port, info.PID)
						return nil
					}
					fmt.Fprintf(cmd.OutOrStdout(), "▲ actr web dashboard process is running (PID %d) but unresponsive at http://127.0.0.1:%d\n", info.PID, info.Port)
					return nil
				}
				// Stale PID file: process is dead
				_ = removeWebPID(cfgDir)
			}
			fmt.Fprintln(cmd.OutOrStdout(), "○ actr web dashboard is stopped.")
			return nil
		},
	}

	cmd.Flags().StringVar(&configDirFlag, "config-dir", config.DefaultConfigDir(), "path to directory containing runner configuration and daemon socket")
	_ = cmd.MarkFlagDirname("config-dir")
	return cmd
}

func startWebBackground(cmd *cobra.Command, port int, devMode, noOpen, demoMode bool, cfgDir, sockPath string) error {
	// 1. Check if already running
	if info, err := readWebPID(cfgDir); err == nil {
		if isProcessAlive(info.PID) {
			if checkServerHealth(info.Port) {
				serverURL := fmt.Sprintf("http://127.0.0.1:%d", info.Port)
				fmt.Fprintf(cmd.OutOrStdout(), "🚀 actr web dashboard is already running at %s (PID %d)\n", serverURL, info.PID)
				if !noOpen {
					_ = openBrowserFunc(serverURL)
				}
				return nil
			}
			return fmt.Errorf("actr web dashboard process is already running (PID %d) but unresponsive at port %d; stop it first with 'actr web stop'", info.PID, info.Port)
		}
		// Stale PID file: process is dead
		_ = removeWebPID(cfgDir)
	}

	// 2. Resolve executable
	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("failed to determine executable path: %w", err)
	}

	args := []string{"web", "--foreground", "--port", strconv.Itoa(port), "--no-open"}
	if devMode {
		args = append(args, "--dev")
	}
	if demoMode {
		args = append(args, "--demo")
	}
	if cfgDir != "" {
		args = append(args, "--config-dir", cfgDir)
	}
	if sockPath != "" {
		args = append(args, "--sock", sockPath)
	}

	child := execCommand(exe, args...)
	setSysProcAttr(child)

	if err := os.MkdirAll(cfgDir, 0755); err != nil {
		return fmt.Errorf("create config dir %s: %w", cfgDir, err)
	}
	logPath := webLogPath(cfgDir)
	if fi, err := os.Stat(logPath); err == nil && fi.Size() > 5*1024*1024 {
		_ = os.Rename(logPath, logPath+".1")
	}
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err == nil {
		child.Stdout = logFile
		child.Stderr = logFile
		defer func() { _ = logFile.Close() }()
	}

	if err := child.Start(); err != nil {
		return fmt.Errorf("failed to spawn web dashboard process: %w", err)
	}

	childErrChan := make(chan error, 1)
	go func() {
		childErrChan <- child.Wait()
	}()

	// 3. Poll checkServerHealth until server is ready (up to 4 seconds)
	serverURL := fmt.Sprintf("http://127.0.0.1:%d", port)
	deadline := time.Now().Add(4 * time.Second)
	ready := false

	for time.Now().Before(deadline) {
		effectivePort := port
		if effectivePort == 0 {
			if pidInfo, err := readWebPID(cfgDir); err == nil && pidInfo.Port > 0 {
				effectivePort = pidInfo.Port
			}
		}
		if effectivePort > 0 && checkServerHealth(effectivePort) {
			ready = true
			serverURL = fmt.Sprintf("http://127.0.0.1:%d", effectivePort)
			break
		}
		select {
		case err := <-childErrChan:
			if err != nil {
				return fmt.Errorf("web dashboard exited unexpectedly (%v): check logs at %s", err, logPath)
			}
			return fmt.Errorf("web dashboard exited unexpectedly: check logs at %s", logPath)
		default:
		}
		time.Sleep(100 * time.Millisecond)
	}

	if !ready {
		_ = child.Process.Kill()
		return fmt.Errorf("timed out waiting for web dashboard to start on %s (check logs at %s)", serverURL, logPath)
	}

	fmt.Fprintf(cmd.OutOrStdout(), "🚀 actr web dashboard started in background at %s (PID %d)\n", serverURL, child.Process.Pid)
	fmt.Fprintln(cmd.OutOrStdout(), "To stop: actr web stop | Status: actr web status")

	if !noOpen {
		_ = openBrowserFunc(serverURL)
	}

	return nil
}

func runWebForeground(cmd *cobra.Command, port int, devMode, noOpen, demoMode bool, cfgDir, sockPath string) error {
	if info, err := readWebPID(cfgDir); err == nil && isProcessAlive(info.PID) {
		return fmt.Errorf("actr web dashboard is already running at http://127.0.0.1:%d (PID %d)", info.Port, info.PID)
	}

	serverCfg := web.ServerConfig{
		Port:      port,
		Host:      "127.0.0.1",
		ConfigDir: cfgDir,
		DevMode:   devMode,
		Version:   GetVersion(),
	}

	masker := engine.NewSecretMasker()

	var tmpDemoDir string
	defer func() {
		if tmpDemoDir != "" {
			_ = os.RemoveAll(tmpDemoDir)
		}
	}()

	if demoMode {
		var err error
		tmpDemoDir, err = os.MkdirTemp("", "actr-demo-web-*")
		if err != nil {
			return fmt.Errorf("create demo temp dir: %w", err)
		}
		demoStore, err := history.NewSQLiteStore(filepath.Join(tmpDemoDir, "demo_history.db"))
		if err != nil {
			return fmt.Errorf("create demo sqlite store: %w", err)
		}
		defer func() { _ = demoStore.Close() }()

		if err := demo.SeedHistoryStore(demoStore); err != nil {
			return fmt.Errorf("seed demo history store: %w", err)
		}
		hAdapter := newHistoryRunStoreAdapter(demoStore)
		hAdapter.demoMode = true
		serverCfg.RunStore = hAdapter

		tmpSock := filepath.Join(os.TempDir(), fmt.Sprintf("actr-demo-web-%d.sock", os.Getpid()))
		demoCtx, cancel := context.WithCancel(cmd.Context())
		defer cancel()

		demoServer, err := demo.StartServerWithStore(demoCtx, tmpSock, demoStore)
		if err != nil {
			return fmt.Errorf("start demo monitor server: %w", err)
		}
		defer func() {
			_ = demoServer.Stop()
			_ = os.Remove(tmpSock)
		}()

		mClient := monitor.NewClient(tmpSock)
		mAdapter := &monitorClientAdapter{client: mClient}
		serverCfg.EventSubscriber = mAdapter
		serverCfg.MetricsProvider = mAdapter

		serverCfg.RunnerProvider = &demoRunnerProvider{}
		demoSecMgr := newDemoSecretManager()
		serverCfg.SecretManager = demoSecMgr
		for _, sec := range demoSecMgr.listSecretsInternal() {
			masker.AddMask(sec.Value)
		}
		serverCfg.DaemonController = &demoDaemonController{status: "running"}
	} else {
		// 1. History RunStore
		dbPath := filepath.Join(cfgDir, "history.db")
		if store, err := history.NewSQLiteStore(dbPath); err == nil && store != nil {
			defer func() { _ = store.Close() }()
			serverCfg.RunStore = newHistoryRunStoreAdapter(store)
		}

		// 2. SecretManager & Masker
		if vProvider, err := resolveVaultForDir(cfgDir); err == nil && vProvider != nil {
			serverCfg.SecretManager = &vaultSecretManagerAdapter{provider: vProvider}
			if secList, err := vProvider.List(cmd.Context()); err == nil {
				for _, sec := range secList {
					if val, err := vProvider.Get(cmd.Context(), sec.Key); err == nil && val != "" {
						masker.AddMask(val)
					}
				}
			}
		}

		// 3. Monitor Client (EventSubscriber & MetricsProvider)
		// Always initialize when sockPath is available because monitor.Client dials lazily on HTTP request
		if sockPath == "" {
			sockPath = filepath.Join(cfgDir, "daemon.sock")
		}
		if sockPath != "" {
			mClient := monitor.NewClient(sockPath)
			mAdapter := &monitorClientAdapter{client: mClient}
			serverCfg.EventSubscriber = mAdapter
			serverCfg.MetricsProvider = mAdapter
		}

		// 4. DaemonController
		serverCfg.DaemonController = &serviceDaemonController{}
	}
	serverCfg.Masker = masker

	srv := web.NewServer(serverCfg)

	ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := srv.Start(ctx); err != nil {
		return fmt.Errorf("failed to start web dashboard on port %d: %w", port, err)
	}
	defer func() {
		_ = srv.Stop()
	}()

	actualPort := port
	if addr := srv.Addr(); addr != "" {
		if _, portStr, err := net.SplitHostPort(addr); err == nil {
			if p, err := strconv.Atoi(portStr); err == nil {
				actualPort = p
			}
		}
	}

	if err := writeWebPID(cfgDir, os.Getpid(), actualPort); err != nil {
		logger.Debug("[web] Failed to write PID file: %v", err)
	}
	defer func() { _ = removeOwnWebPID(cfgDir) }()

	serverURL := fmt.Sprintf("http://127.0.0.1:%d", actualPort)
	fmt.Fprintf(cmd.OutOrStdout(), "🚀 actr web dashboard running at %s (PID %d)\n", serverURL, os.Getpid())
	if devMode {
		fmt.Fprintln(cmd.OutOrStdout(), "🛠️  Development mode enabled (proxy ready)")
	}
	fmt.Fprintln(cmd.OutOrStdout(), "Press Ctrl+C to stop")

	if !noOpen {
		go func() {
			select {
			case <-time.After(100 * time.Millisecond):
				_ = openBrowserFunc(serverURL)
			case <-ctx.Done():
			}
		}()
	}

	<-ctx.Done()
	fmt.Fprintln(cmd.OutOrStdout(), "\nShutting down web dashboard...")
	return srv.Stop()
}

// resolveVaultForDir initializes the vault provider for the specified config directory.
func resolveVaultForDir(cfgDir string) (vault.Provider, error) {
	var vaultCfg *config.VaultYAMLConfig
	for _, name := range []string{"config.yaml", "config.yml"} {
		p := filepath.Join(cfgDir, name)
		if _, err := os.Stat(p); err == nil {
			loaded, err := config.LoadYAMLConfig(p)
			if err != nil {
				return nil, fmt.Errorf("load config file %q: %w", p, err)
			}
			if loaded != nil && len(loaded.Vault.Providers) > 0 {
				vaultCfg = &loaded.Vault
			}
			break
		}
	}
	return vault.NewProviderFromConfig(vaultCfg, cfgDir)
}

// historyRunStoreAdapter adapts *history.SQLiteStore to satisfy web.RunStore.
type historyRunStoreAdapter struct {
	store    *history.SQLiteStore
	demoMode bool
}

var _ web.RunStore = (*historyRunStoreAdapter)(nil)

func newHistoryRunStoreAdapter(store *history.SQLiteStore) *historyRunStoreAdapter {
	return &historyRunStoreAdapter{
		store: store,
	}
}

func (a *historyRunStoreAdapter) GetRuns(ctx context.Context, limit, offset int, repo, status, q string) ([]*history.RunRecord, int, error) {
	summaries, err := a.store.ListRuns(0)
	if err != nil {
		return nil, 0, err
	}
	var filtered []*monitor.RunSummary
	for _, s := range summaries {
		if repo != "" && s.Repo != repo && s.RepoAlias != repo {
			continue
		}
		if status != "" && string(s.Status) != status {
			continue
		}
		if q != "" && !strings.Contains(s.Name, q) && !strings.Contains(s.ID, q) {
			continue
		}
		filtered = append(filtered, s)
	}
	total := len(filtered)
	if offset >= len(filtered) {
		return []*history.RunRecord{}, total, nil
	}
	end := offset + limit
	if limit <= 0 || end > len(filtered) {
		end = len(filtered)
	}
	slice := filtered[offset:end]
	records := make([]*history.RunRecord, len(slice))
	for i, sm := range slice {
		rec, err := a.store.GetRun(sm.ID)
		if err != nil {
			records[i] = &history.RunRecord{
				ID:        sm.ID,
				Name:      sm.Name,
				Workflow:  sm.Workflow,
				Repo:      sm.Repo,
				RepoAlias: sm.RepoAlias,
				RepoURL:   sm.RepoURL,
				Owner:     sm.Owner,
				Status:    sm.Status,
				StartedAt: sm.StartedAt,
				EndedAt:   sm.EndedAt,
			}
		} else {
			records[i] = rec
		}
	}
	return records, total, nil
}

func (a *historyRunStoreAdapter) GetRun(ctx context.Context, id string) (*history.RunRecord, error) {
	return a.store.GetRun(id)
}

func (a *historyRunStoreAdapter) GetRunSteps(ctx context.Context, runID string) ([]*history.StepRecord, error) {
	run, err := a.store.GetRun(runID)
	if err != nil {
		return nil, err
	}
	if run != nil {
		return run.Steps, nil
	}
	return nil, nil
}

func (a *historyRunStoreAdapter) GetStepLogs(ctx context.Context, runID, stepID string) ([]string, error) {
	return a.store.GetStepLogs(runID, stepID)
}

func (a *historyRunStoreAdapter) GetRunMetrics(ctx context.Context, runID string) ([]*monitor.TelemetrySample, error) {
	if a.demoMode {
		now := time.Now().UTC()
		var samples []*monitor.TelemetrySample
		for i := 20; i >= 0; i-- {
			t := now.Add(-time.Duration(i*10) * time.Second)
			factor := float64(20-i) / 20.0
			cpu := 25.0 + 45.0*factor + float64((i*7)%15)
			mem := uint64(180*1024*1024) + uint64(factor*float64(120*1024*1024))
			diskWrite := uint64(2*1024*1024) + uint64((i%4)*1024*1024)
			diskRead := uint64(5*1024*1024) + uint64((i%3)*2*1024*1024)
			samples = append(samples, &monitor.TelemetrySample{
				Timestamp:        t,
				StepCPU:          cpu,
				StepRSSBytes:     mem,
				HostCPU:          cpu,
				HostMemUsed:      mem,
				DiskWriteRate:    diskWrite,
				StepDiskReadRate: diskRead,
				NetRxRate:        uint64(512 * 1024),
				NetTxRate:        uint64(128 * 1024),
			})
		}
		return samples, nil
	}
	return []*monitor.TelemetrySample{}, nil
}

// vaultSecretManagerAdapter adapts vault.Provider to satisfy web.SecretManager.
type vaultSecretManagerAdapter struct {
	provider vault.Provider
}

var _ web.SecretManager = (*vaultSecretManagerAdapter)(nil)

func (a *vaultSecretManagerAdapter) ListSecrets(ctx context.Context) ([]vault.SecretMetadata, error) {
	return a.provider.List(ctx)
}

func (a *vaultSecretManagerAdapter) SetSecret(ctx context.Context, key, val, scope string) error {
	return a.provider.Set(ctx, key, val)
}

func (a *vaultSecretManagerAdapter) DeleteSecret(ctx context.Context, key string) error {
	return a.provider.Delete(ctx, key)
}

// monitorClientAdapter adapts *monitor.Client to satisfy web.EventSubscriber and web.MetricsProvider.
type monitorClientAdapter struct {
	client *monitor.Client
}

var _ web.EventSubscriber = (*monitorClientAdapter)(nil)
var _ web.MetricsProvider = (*monitorClientAdapter)(nil)

func (a *monitorClientAdapter) SubscribeEvents(ctx context.Context) (<-chan monitor.Event, func(), error) {
	subCtx, cancel := context.WithCancel(ctx)
	events, _ := a.client.StreamEvents(subCtx)
	return events, cancel, nil
}

func (a *monitorClientAdapter) GetCurrentMetrics(ctx context.Context) (*monitor.RunnerMetrics, error) {
	snap, err := a.client.GetSnapshot(ctx)
	if err != nil {
		return nil, err
	}
	if snap != nil && snap.Telemetry != nil {
		return snap.Telemetry, nil
	}
	return nil, nil
}

// serviceDaemonController adapts internal/service to satisfy web.DaemonController.
type serviceDaemonController struct{}

var _ web.DaemonController = (*serviceDaemonController)(nil)

func (s *serviceDaemonController) Status() (string, error) {
	return service.Status()
}

func (s *serviceDaemonController) Start() error {
	return service.Start()
}

func (s *serviceDaemonController) Stop() error {
	return service.Stop()
}

// demoSecretItem holds a demo secret entry.
type demoSecretItem struct {
	Key   string
	Value string
	Scope string
}

// demoSecretManager satisfies web.SecretManager with mock secrets.
type demoSecretManager struct {
	mu      sync.Mutex
	secrets map[string]demoSecretItem
}

var _ web.SecretManager = (*demoSecretManager)(nil)

func newDemoSecretManager() *demoSecretManager {
	return &demoSecretManager{
		secrets: map[string]demoSecretItem{
			"GITHUB_TOKEN": {
				Key:   "GITHUB_TOKEN",
				Value: "demo_mock_github_pat_value_unredacted_01",
				Scope: "repo",
			},
			"DOCKER_PASSWORD": {
				Key:   "DOCKER_PASSWORD",
				Value: "demo_mock_docker_registry_access_pass_02",
				Scope: "global",
			},
			"NPM_TOKEN": {
				Key:   "NPM_TOKEN",
				Value: "demo_mock_npm_registry_publish_token_03",
				Scope: "global",
			},
			"SLACK_WEBHOOK_URL": {
				Key:   "SLACK_WEBHOOK_URL",
				Value: "https://hooks.slack.com/services/T00/B00/SECRET",
				Scope: "alerts",
			},
		},
	}
}

func (m *demoSecretManager) listSecretsInternal() []demoSecretItem {
	m.mu.Lock()
	defer m.mu.Unlock()
	var list []demoSecretItem
	for _, s := range m.secrets {
		list = append(list, s)
	}
	return list
}

func (m *demoSecretManager) ListSecrets(ctx context.Context) ([]vault.SecretMetadata, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var list []vault.SecretMetadata
	for _, s := range m.secrets {
		list = append(list, vault.SecretMetadata{
			Key:       s.Key,
			Provider:  "keychain",
			UpdatedAt: time.Now().Add(-2 * time.Hour).Format(time.RFC3339),
		})
	}
	return list, nil
}

func (m *demoSecretManager) SetSecret(ctx context.Context, key, val, scope string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.secrets[key] = demoSecretItem{
		Key:   key,
		Value: val,
		Scope: scope,
	}
	return nil
}

func (m *demoSecretManager) DeleteSecret(ctx context.Context, key string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.secrets[key]; !ok {
		return vault.ErrSecretNotFound
	}
	delete(m.secrets, key)
	return nil
}

// demoRunnerProvider satisfies web.RunnerProvider with representative runner topology.
type demoRunnerProvider struct{}

var _ web.RunnerProvider = (*demoRunnerProvider)(nil)

func (d *demoRunnerProvider) GetRunners(ctx context.Context) ([]web.RunnerInfo, error) {
	return []web.RunnerInfo{
		{
			Repo:           "adrijshikhar/actr",
			Name:           "actr-runner-darwin-01",
			Labels:         []string{"self-hosted", "macOS", "ARM64", "actr"},
			MaxConcurrency: 4,
			ActiveSlots:    2,
			QueuedJobs:     1,
			Status:         "online",
		},
		{
			Repo:           "adrijshikhar/aim",
			Name:           "aim-runner-linux-02",
			Labels:         []string{"self-hosted", "Linux", "X64", "docker"},
			MaxConcurrency: 4,
			ActiveSlots:    1,
			QueuedJobs:     0,
			Status:         "online",
		},
	}, nil
}

// demoDaemonController satisfies web.DaemonController with interactive state toggle.
type demoDaemonController struct {
	mu     sync.Mutex
	status string
}

var _ web.DaemonController = (*demoDaemonController)(nil)

func (d *demoDaemonController) Status() (string, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.status == "" {
		d.status = "running"
	}
	return d.status, nil
}

func (d *demoDaemonController) Start() error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.status = "running"
	return nil
}

func (d *demoDaemonController) Stop() error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.status = "stopped"
	return nil
}

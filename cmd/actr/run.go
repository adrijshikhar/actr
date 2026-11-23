package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/adrijshikhar/actr/internal/config"
	"github.com/adrijshikhar/actr/internal/driver"
	"github.com/adrijshikhar/actr/internal/driver/host"
	"github.com/adrijshikhar/actr/internal/engine"
	"github.com/adrijshikhar/actr/internal/history"
	"github.com/adrijshikhar/actr/internal/logger"
	"github.com/adrijshikhar/actr/internal/monitor"
	"github.com/adrijshikhar/actr/internal/protocol"
	"github.com/adrijshikhar/actr/internal/registration"
	"github.com/adrijshikhar/actr/internal/supervisor"
	"github.com/adrijshikhar/actr/internal/telemetry"
	"github.com/adrijshikhar/actr/internal/vault"
	"github.com/spf13/cobra"
)

var runRegisterCmdFunc = func(cmd *cobra.Command) error {
	regCmd := newRegisterCmd()
	if cmd.Context() != nil {
		regCmd.SetContext(cmd.Context())
	}
	if f := cmd.Flag("config-dir"); f != nil && f.Value.String() != "" {
		_ = regCmd.Flags().Set("config-dir", f.Value.String())
	}
	_ = regCmd.Flags().Set("prompt-service", "false")
	regCmd.SetIn(cmd.InOrStdin())
	regCmd.SetOut(cmd.OutOrStdout())
	regCmd.SetErr(cmd.ErrOrStderr())
	return regCmd.RunE(regCmd, nil)
}

type JobRequestReference struct {
	RunnerRequestID string `json:"runner_request_id"`
	RunServiceURL   string `json:"run_service_url"`
	BillingOwnerID  string `json:"billing_owner_id"`
}

func (r *JobRequestReference) UnmarshalJSON(data []byte) error {
	var raw struct {
		RunnerRequestID1 string `json:"runner_request_id"`
		RunnerRequestID2 string `json:"runnerRequestId"`
		RunnerRequestID3 string `json:"RunnerRequestId"`
		RunnerRequestID4 string `json:"jobMessageId"`
		RunnerRequestID5 string `json:"job_message_id"`

		RunServiceURL1 string `json:"run_service_url"`
		RunServiceURL2 string `json:"runServiceUrl"`
		RunServiceURL3 string `json:"RunServiceUrl"`
		RunServiceURL4 string `json:"runServiceURL"`

		BillingOwnerID1 string `json:"billing_owner_id"`
		BillingOwnerID2 string `json:"billingOwnerId"`
		BillingOwnerID3 string `json:"BillingOwnerId"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	r.RunnerRequestID = raw.RunnerRequestID1
	if r.RunnerRequestID == "" {
		r.RunnerRequestID = raw.RunnerRequestID2
	}
	if r.RunnerRequestID == "" {
		r.RunnerRequestID = raw.RunnerRequestID3
	}
	if r.RunnerRequestID == "" {
		r.RunnerRequestID = raw.RunnerRequestID4
	}
	if r.RunnerRequestID == "" {
		r.RunnerRequestID = raw.RunnerRequestID5
	}

	r.RunServiceURL = raw.RunServiceURL1
	if r.RunServiceURL == "" {
		r.RunServiceURL = raw.RunServiceURL2
	}
	if r.RunServiceURL == "" {
		r.RunServiceURL = raw.RunServiceURL3
	}
	if r.RunServiceURL == "" {
		r.RunServiceURL = raw.RunServiceURL4
	}

	r.BillingOwnerID = raw.BillingOwnerID1
	if r.BillingOwnerID == "" {
		r.BillingOwnerID = raw.BillingOwnerID2
	}
	if r.BillingOwnerID == "" {
		r.BillingOwnerID = raw.BillingOwnerID3
	}
	return nil
}

func resolveConfigDir(dir string, explicitlySet bool) string {
	if explicitlySet && dir != "" {
		return dir
	}
	if env := os.Getenv("ACT_RUNNER_CONFIG_DIR"); env != "" {
		return config.DefaultConfigDir()
	}
	if _, err := os.Stat(".runner"); err == nil {
		return "."
	}
	if _, err := os.Stat("config.yaml"); err == nil {
		return "."
	}
	if _, err := os.Stat("config.yml"); err == nil {
		return "."
	}
	if dir != "" {
		return dir
	}
	return config.DefaultConfigDir()
}

// purgeLegacyJSONRuns inspects runsDir for legacy DiskStore artifacts (index.json or subdirectories containing meta.json)
// and deletes the entire directory to remove stale mock runs.
func purgeLegacyJSONRuns(runsDir string) error {
	if runsDir == "" {
		return nil
	}
	info, err := os.Stat(runsDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	if !info.IsDir() {
		return nil
	}

	var legacyPurged bool

	indexPath := filepath.Join(runsDir, "index.json")
	if _, err := os.Stat(indexPath); err == nil {
		logger.Info("Removing legacy index.json", "path", indexPath)
		_ = os.Remove(indexPath)
		legacyPurged = true
	}

	entries, err := os.ReadDir(runsDir)
	if err == nil {
		for _, entry := range entries {
			if entry.IsDir() {
				metaPath := filepath.Join(runsDir, entry.Name(), "meta.json")
				if _, err := os.Stat(metaPath); err == nil {
					subDir := filepath.Join(runsDir, entry.Name())
					logger.Info("Removing legacy JSON run directory", "path", subDir)
					_ = os.RemoveAll(subDir)
					legacyPurged = true
				}
			}
		}
	}

	// Clean up runsDir only if legacy artifacts were removed and directory is now empty
	if legacyPurged {
		_ = os.Remove(runsDir)
	}

	return nil
}

func runnerOS() string {
	switch runtime.GOOS {
	case "darwin":
		return "macOS"
	case "linux":
		return "Linux"
	case "windows":
		return "Windows"
	default:
		return runtime.GOOS
	}
}

func runnerArch() string {
	switch runtime.GOARCH {
	case "arm64":
		return "ARM64"
	case "amd64":
		return "X64"
	case "386":
		return "X86"
	case "arm":
		return "ARM"
	default:
		return strings.ToUpper(runtime.GOARCH)
	}
}

func newRunCmd() *cobra.Command {
	var (
		onceFlag         bool
		configDirFlag    string
		runsDirFlag      string
		traceFlag        bool
		limitCPUFlag     string
		limitMemoryFlag  string
		limitPidsFlag    int
		driverFlag       string
		otelEndpointFlag string
		otelProtocolFlag string
		otelHeadersFlag  string
	)

	cmd := &cobra.Command{
		Use:   "run",
		Short: "Run the runner daemon and listen for jobs",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if traceFlag {
				logger.Init(true, cmd.ErrOrStderr())
			}

			ctx := cmd.Context()
			if ctx == nil {
				ctx = context.Background()
			}

			cfgDir := resolveConfigDir(configDirFlag, cmd.Flags().Changed("config-dir"))

			// Purge legacy JSON runs directory if present
			if err := purgeLegacyJSONRuns(filepath.Join(cfgDir, "runs")); err != nil {
				logger.Warn("failed to purge legacy runs directory", "error", err)
			}
			if runsDirFlag != "" && runsDirFlag != filepath.Join(cfgDir, "runs") {
				if err := purgeLegacyJSONRuns(runsDirFlag); err != nil {
					logger.Warn("failed to purge custom legacy runs directory", "error", err)
				}
			}

			// Load YAML config if present (e.g. config.yaml or config.yml)
			var yamlCfg *config.RunnerYAMLConfig
			for _, name := range []string{"config.yaml", "config.yml"} {
				p := filepath.Join(cfgDir, name)
				if _, err := os.Stat(p); err == nil {
					loaded, err := config.LoadYAMLConfig(p)
					if err != nil {
						return fmt.Errorf("load config file %q: %w", p, err)
					}
					yamlCfg = loaded
					break
				}
			}

			ctxLimits := LimitsFromContext(ctx)
			limitsConfig, err := resolveLimits(cmd, yamlCfg, ctxLimits, limitCPUFlag, limitMemoryFlag, limitPidsFlag)
			if err != nil {
				return err
			}

			resolvedDriver, err := resolveDriver(cmd, yamlCfg, driverFlag)
			if err != nil {
				return err
			}

			if otelEndpointFlag != "" {
				_ = os.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", otelEndpointFlag)
			}
			if otelProtocolFlag != "" {
				_ = os.Setenv("OTEL_EXPORTER_OTLP_PROTOCOL", otelProtocolFlag)
			}
			if otelHeadersFlag != "" {
				_ = os.Setenv("OTEL_EXPORTER_OTLP_HEADERS", otelHeadersFlag)
			}

			// Initialize shared OpenTelemetry Manager (Traces & Metrics)
			otelMgr, otelErr := telemetry.NewManager(ctx, "actr")
			if otelErr != nil {
				logger.Warn("Failed to initialize OpenTelemetry", "error", otelErr)
			} else {
				defer func() {
					shutdownCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
					defer cancel()
					if err := otelMgr.Shutdown(shutdownCtx); err != nil {
						logger.Debug("OpenTelemetry shutdown error", "error", err)
					}
				}()
			}

			// Check for multi-repo supervisor configuration (e.g. config.yaml with version: "1" and repos:)
			var multiRepoConfigPath string
			for _, name := range []string{"config.yaml", "config.yml"} {
				p := filepath.Join(cfgDir, name)
				if config.IsMultiRepoConfig(p) {
					multiRepoConfigPath = p
					break
				}
			}

			if multiRepoConfigPath != "" {
				supCfg, err := config.LoadSupervisorConfig(multiRepoConfigPath)
				if err != nil {
					return fmt.Errorf("load multi-repo supervisor config %q: %w", multiRepoConfigPath, err)
				}
				sockPath := filepath.Join(cfgDir, "daemon.sock")
				sup, err := supervisor.NewSupervisor(supCfg, sockPath)
				if err != nil {
					return fmt.Errorf("initialize supervisor: %w", err)
				}
				if otelMgr != nil {
					sup.WithTracer(otelMgr)
				}

				// Wire SQLite history store and retention to supervisor monitor server
				dbPath := filepath.Join(cfgDir, "history.db")
				if runsDirFlag != "" {
					if strings.HasSuffix(runsDirFlag, ".db") {
						dbPath = runsDirFlag
					} else {
						dbPath = filepath.Join(runsDirFlag, "history.db")
					}
				}
				if srv := sup.MonitorServer(); srv != nil {
					if histStore, err := history.NewSQLiteStore(dbPath); err != nil {
						logger.Warn("failed to initialize sqlite history store, continuing without persistence", "error", err)
					} else {
						defer func() {
							if err := histStore.Close(); err != nil {
								logger.Error("failed to close sqlite history store", "error", err)
							}
						}()
						if n, err := histStore.RecoverInterruptedRuns(); err != nil {
							logger.Warn("failed to recover interrupted runs", "error", err)
						} else if n > 0 {
							logger.Info("recovered interrupted runs from prior crash", "count", n)
						}
						srv.WithHistoryStore(histStore)
						maxRuns := 50
						var maxDiskBytes int64 = 500 * 1024 * 1024
						if supCfg.Global.Retention.MaxRuns > 0 {
							maxRuns = supCfg.Global.Retention.MaxRuns
						}
						if supCfg.Global.Retention.MaxDiskMB > 0 {
							maxDiskBytes = supCfg.Global.Retention.MaxDiskMB * 1024 * 1024
						}
						srv.WithRetention(maxRuns, maxDiskBytes)
					}
				}

				// Wire supervisor reload on monitor POST /reload
				sup.MonitorServer().WithReloadFunc(func() error {
					logger.Info("Reloading supervisor configuration", "path", multiRepoConfigPath)
					newCfg, err := config.LoadSupervisorConfig(multiRepoConfigPath)
					if err != nil {
						return err
					}
					return sup.Reload(newCfg)
				})

				// SIGHUP listener for daemon configuration reload
				supCtx, supCancel := context.WithCancel(ctx)
				defer supCancel()

				sigCh := make(chan os.Signal, 1)
				signal.Notify(sigCh, syscall.SIGHUP)
				defer signal.Stop(sigCh)

				go func() {
					for {
						select {
						case <-supCtx.Done():
							return
						case <-sigCh:
							logger.Info("Received SIGHUP, reloading supervisor configuration...")
							newCfg, err := config.LoadSupervisorConfig(multiRepoConfigPath)
							if err != nil {
								logger.Error("Failed to reload supervisor config on SIGHUP", "error", err)
								continue
							}
							if err := sup.Reload(newCfg); err != nil {
								logger.Error("Supervisor reload failed on SIGHUP", "error", err)
							} else {
								logger.Info("Supervisor configuration reloaded successfully via SIGHUP")
							}
						}
					}
				}()

				return sup.Start(ctx)
			}

			runnerPath := filepath.Join(cfgDir, ".runner")
			if _, err := os.Stat(runnerPath); os.IsNotExist(err) {
				if isTerminalFunc(os.Stdin.Fd()) {
					reader := bufio.NewReader(cmd.InOrStdin())
					registerNow, promptErr := registration.PromptConfirm(
						reader,
						cmd.OutOrStdout(),
						"Runner is not configured yet. Would you like to register now?",
						true,
					)
					if promptErr != nil {
						return promptErr
					}
					if !registerNow {
						return fmt.Errorf("runner is not configured; run 'actr register' first")
					}
					if err := runRegisterCmdFunc(cmd); err != nil {
						return err
					}
				} else {
					return fmt.Errorf("runner is not configured; run 'actr register' first")
				}
			}

			cfg := ConfigFromContext(ctx)
			if cfg == nil {
				var err error
				cfg, err = config.LoadRunnerConfig(cfgDir)
				if err != nil {
					return fmt.Errorf("runner is not configured; run 'actr register' first: %w", err)
				}
			}

			creds, err := config.LoadCredentials(cfgDir)
			if err != nil {
				return fmt.Errorf("runner is not configured; run 'actr register' first: %w", err)
			}

			rsaPath := filepath.Join(cfgDir, ".credentials_rsaparams")
			rsaBytes, err := os.ReadFile(rsaPath)
			if err != nil {
				return fmt.Errorf("runner is not configured; run 'actr register' first: %w", err)
			}
			var rsaParams protocol.RSAParameters
			if err := json.Unmarshal(rsaBytes, &rsaParams); err != nil {
				return fmt.Errorf("unmarshal rsa parameters: %w", err)
			}
			privKey, err := protocol.ParseRSAPrivateKey(&rsaParams)
			if err != nil {
				return fmt.Errorf("parse rsa private key: %w", err)
			}

			dockerSocket := ""
			autoPull := true
			if yamlCfg != nil {
				dockerSocket = yamlCfg.Driver.Docker.Socket
				if yamlCfg.Driver.Docker.AutoPull {
					autoPull = true
				}
			}

			dockerDrv, dockerErr := DetectDockerDriver(ctx, dockerSocket, autoPull)
			if dockerErr != nil {
				logger.Debug("Docker driver not available", "error", dockerErr)
			}

			if resolvedDriver == "docker" && dockerDrv == nil {
				if dockerErr != nil {
					return fmt.Errorf("docker execution required by --driver docker, but Docker daemon is unreachable: %w", dockerErr)
				}
				return fmt.Errorf("docker execution required by --driver docker, but Docker daemon is unreachable")
			}

			d := DriverFromContext(ctx)
			if d == nil {
				if resolvedDriver == "docker" && dockerDrv != nil {
					d = dockerDrv
				} else {
					d = host.NewDriver()
				}
			}

			brokerURL := cfg.ServerURL
			if cfg.UseV2Flow && cfg.ServerURLV2 != "" {
				brokerURL = cfg.ServerURLV2
			}

			brokerClient := BrokerClientFromContext(ctx)
			if brokerClient == nil {
				brokerClient = protocol.NewBrokerClient(nil, brokerURL, creds.AuthorizationURL, creds.ClientID, privKey)
			}

			runServiceClient := RunServiceClientFromContext(ctx)
			if runServiceClient == nil {
				runServiceClient = protocol.NewRunServiceClient(nil)
			}

			coord := CoordinatorFromContext(ctx)
			if coord == nil {
				var protoRunClient *protocol.RunServiceClient
				if rc, ok := runServiceClient.(*protocol.RunServiceClient); ok {
					protoRunClient = rc
				}
				alias := cfg.GitHubURL
				if alias != "" {
					alias = strings.TrimSuffix(alias, ".git")
					if idx := strings.LastIndex(alias, "/"); idx >= 0 {
						alias = alias[idx+1:]
					}
				}
				coordEngine := engine.NewCoordinator(d, protoRunClient, cfg.WorkFolder).
					WithRunnerName(cfg.AgentName).
					WithRepoAlias(alias).
					WithRepositoryURL(cfg.GitHubURL).
					WithOutput(cmd.OutOrStdout(), cmd.ErrOrStderr())
				if dockerDrv != nil {
					coordEngine = coordEngine.WithDockerDriver(dockerDrv)
				}
				if limitsConfig != nil {
					coordEngine = coordEngine.WithLimits(limitsConfig)
				}
				if otelMgr != nil {
					coordEngine = coordEngine.WithTracer(otelMgr)
				}
				var vCfg *config.VaultYAMLConfig
				if yamlCfg != nil {
					vCfg = &yamlCfg.Vault
				}
				if vaultProv, err := vault.NewProviderFromConfig(vCfg, cfgDir); err == nil && vaultProv != nil {
					coordEngine = coordEngine.WithVault(vaultProv)
				}
				coord = coordEngine
			} else {
				if c, ok := coord.(*engine.Coordinator); ok {
					if dockerDrv != nil {
						c.WithDockerDriver(dockerDrv)
					}
					if limitsConfig != nil {
						c.WithLimits(limitsConfig)
					}
					if otelMgr != nil {
						c.WithTracer(otelMgr)
					}
					var vCfg *config.VaultYAMLConfig
					if yamlCfg != nil {
						vCfg = &yamlCfg.Vault
					}
					if vaultProv, err := vault.NewProviderFromConfig(vCfg, cfgDir); err == nil && vaultProv != nil {
						c.WithVault(vaultProv)
					}
				} else {
					type limitsSetter interface {
						SetLimits(*config.LimitsConfig)
					}
					type dockerDriverSetter interface {
						SetDockerDriver(driver.Driver)
					}
					if s, ok := coord.(limitsSetter); ok && limitsConfig != nil {
						s.SetLimits(limitsConfig)
					}
					if s, ok := coord.(dockerDriverSetter); ok && dockerDrv != nil {
						s.SetDockerDriver(dockerDrv)
					}
				}
			}

			monServer := monitor.NewServer(filepath.Join(cfgDir, "daemon.sock"), cfg.AgentName)
			dbPath := filepath.Join(cfgDir, "history.db")
			if runsDirFlag != "" {
				if strings.HasSuffix(runsDirFlag, ".db") {
					dbPath = runsDirFlag
				} else {
					dbPath = filepath.Join(runsDirFlag, "history.db")
				}
			}
			if histStore, err := history.NewSQLiteStore(dbPath); err != nil {
				logger.Warn("failed to initialize sqlite history store, continuing without persistence", "error", err)
			} else {
				defer func() {
					if err := histStore.Close(); err != nil {
						logger.Error("failed to close sqlite history store", "error", err)
					}
				}()
				if n, err := histStore.RecoverInterruptedRuns(); err != nil {
					logger.Warn("failed to recover interrupted runs", "error", err)
				} else if n > 0 {
					logger.Info("recovered interrupted runs from prior crash", "count", n)
				}
				monServer.WithHistoryStore(histStore)
				maxRuns := 50
				var maxDiskBytes int64 = 200 * 1024 * 1024
				if yamlCfg != nil {
					if yamlCfg.Retention.MaxRuns > 0 {
						maxRuns = yamlCfg.Retention.MaxRuns
					}
					if yamlCfg.Retention.MaxDiskMB > 0 {
						maxDiskBytes = yamlCfg.Retention.MaxDiskMB * 1024 * 1024
					}
				}
				monServer.WithRetention(maxRuns, maxDiskBytes)
			}

			if err := monServer.Start(); err != nil {
				logger.Warn("failed to start monitor server, continuing without monitor", "error", err)
			} else {
				defer func() {
					if err := monServer.Stop(); err != nil {
						logger.Error("failed to stop monitor server", "error", err)
					}
				}()
				if setter, ok := coord.(interface{ SetMonitorServer(*monitor.Server) }); ok {
					setter.SetMonitorServer(monServer)
				}
			}

			if err := brokerClient.CreateSession(ctx, cfg.AgentName, cfg.AgentID, cfg.PoolID); err != nil {
				return fmt.Errorf("create broker session: %w", err)
			}
			defer func() {
				cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
				defer cancel()
				if err := brokerClient.DeleteSession(cleanupCtx); err != nil {
					logger.Error("failed to delete broker session", "error", err)
				}
			}()

			fmt.Fprintln(cmd.OutOrStdout(), "Listening for Jobs")
			logger.Info("Listening for Jobs")

			// GitHub Actions broker requires runner protocol compatibility version (3.0.0),
			// not the internal application semver.
			runnerVersion := "3.0.0"
			osName := runnerOS()
			archName := runnerArch()

			token := creds.IDToken
			if bt, ok := brokerClient.(interface{ Token() string }); ok {
				if t := bt.Token(); t != "" {
					token = t
				}
			}

			for {
				select {
				case <-ctx.Done():
					logger.Info("shutting down runner daemon")
					return nil
				default:
				}

				msg, err := brokerClient.GetMessage(ctx, runnerVersion, osName, archName)
				if err != nil {
					if ctx.Err() != nil {
						logger.Info("shutting down runner daemon")
						return nil
					}
					if strings.Contains(err.Error(), "RunnerSessionInvalid") || strings.Contains(err.Error(), "Runner session is invalid") {
						logger.Warn("broker session invalidated; recreating broker session", "agentName", cfg.AgentName)
						if sessErr := brokerClient.CreateSession(ctx, cfg.AgentName, cfg.AgentID, cfg.PoolID); sessErr != nil {
							logger.Error("failed to recreate broker session", "error", sessErr)
							select {
							case <-ctx.Done():
								return nil
							case <-time.After(2 * time.Second):
							}
						} else {
							if bt, ok := brokerClient.(interface{ Token() string }); ok {
								if t := bt.Token(); t != "" {
									token = t
								}
							}
						}
						continue
					}
					logger.Error("failed to get message", "error", err)
					select {
					case <-ctx.Done():
						return nil
					case <-time.After(1 * time.Second):
					}
					continue
				}

				if msg == nil {
					continue
				}

				if !strings.EqualFold(msg.MessageType, "RunnerJobRequest") && !strings.EqualFold(msg.MessageType, "PipelineAgentJobRequest") {
					logger.Debug("ignoring non-job message", "type", msg.MessageType, "id", msg.MessageID)
					continue
				}

				logger.Info("received job request message", "type", msg.MessageType, "id", msg.MessageID)

				var reqRef JobRequestReference
				if msg.Body != "" {
					if err := json.Unmarshal([]byte(msg.Body), &reqRef); err != nil {
						logger.Warn("failed to parse job request reference from body", "error", err)
					}
				}
				if reqRef.RunnerRequestID == "" {
					reqRef.RunnerRequestID = strconv.FormatInt(msg.MessageID, 10)
				}

				if err := brokerClient.AcknowledgeMessage(ctx, reqRef.RunnerRequestID); err != nil {
					logger.Error("failed to acknowledge message", "error", err)
				}

				runServiceURL := reqRef.RunServiceURL
				if runServiceURL == "" {
					runServiceURL = cfg.ServerURL
				}

				if bt, ok := brokerClient.(interface{ Token() string }); ok {
					if t := bt.Token(); t != "" {
						token = t
					}
				}

				jobSpec, err := runServiceClient.AcquireJob(ctx, runServiceURL, reqRef.RunnerRequestID, reqRef.BillingOwnerID, token)
				if err != nil {
					logger.Error("failed to acquire job", "error", err)
					if onceFlag {
						return fmt.Errorf("acquire job: %w", err)
					}
					continue
				}

				logger.Info("acquired job", "jobId", jobSpec.JobID, "jobName", jobSpec.JobName)

				jobToken := token
				if jobSpec.JobToken != "" {
					jobToken = jobSpec.JobToken
				} else if jt, ok := jobSpec.Variables["system.accessToken"]; ok && jt != "" {
					jobToken = jt
				} else if jt, ok := jobSpec.Variables["ACTIONS_RUNTIME_TOKEN"]; ok && jt != "" {
					jobToken = jt
				}

				completeReq, execErr := coord.ExecuteJob(ctx, jobSpec, runServiceURL, jobToken)
				if execErr != nil {
					logger.Error("job execution encountered error", "error", execErr)
				}

				if completeReq == nil && jobSpec != nil {
					completeReq = &protocol.CompleteJobRequest{
						PlanID:      jobSpec.PlanID,
						JobID:       jobSpec.JobID,
						Conclusion:  "failure",
						Outputs:     map[string]string{},
						StepResults: []protocol.StepResult{},
					}
				}

				if completeReq != nil {
					completeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
					if completeErr := runServiceClient.CompleteJob(completeCtx, runServiceURL, jobToken, completeReq); completeErr != nil {
						logger.Error("failed to report job completion", "error", completeErr)
						if execErr == nil {
							execErr = fmt.Errorf("complete job: %w", completeErr)
						}
					}
					cancel()
				}

				if onceFlag {
					logger.Info("once flag set; exiting after single job execution")
					return execErr
				}
			}
		},
	}

	cmd.Flags().BoolVar(&onceFlag, "once", false, "run a single job and exit cleanly")
	cmd.Flags().StringVar(&configDirFlag, "config-dir", config.DefaultConfigDir(), "path to directory containing runner configuration and credentials")
	cmd.Flags().StringVar(&runsDirFlag, "runs-dir", "", "path to runs history directory (defaults to <config-dir>/runs)")
	cmd.Flags().BoolVar(&traceFlag, "trace", false, "enable verbose trace logging")
	cmd.Flags().StringVar(&limitCPUFlag, "limit-cpu", "", "max CPU quota (e.g. 2.0, 500m)")
	cmd.Flags().StringVar(&limitMemoryFlag, "limit-memory", "", "max memory (e.g. 4GiB, 512MB)")
	cmd.Flags().IntVar(&limitPidsFlag, "limit-pids", 0, "max process/thread count")
	cmd.Flags().StringVar(&driverFlag, "driver", "", "default driver (host or docker)")
	cmd.Flags().StringVar(&otelEndpointFlag, "otel-endpoint", "", "OpenTelemetry OTLP exporter endpoint (e.g. http://localhost:4318, for InfluxDB / Prometheus / Datadog)")
	cmd.Flags().StringVar(&otelProtocolFlag, "otel-protocol", "", "OpenTelemetry OTLP protocol (grpc or http/protobuf)")
	cmd.Flags().StringVar(&otelHeadersFlag, "otel-headers", "", "OpenTelemetry OTLP exporter headers (e.g. Authorization=Token <token>)")

	_ = cmd.MarkFlagDirname("config-dir")
	_ = cmd.MarkFlagDirname("runs-dir")

	return cmd
}

func resolveLimits(cmd *cobra.Command, yamlCfg *config.RunnerYAMLConfig, ctxLimits *config.LimitsConfig, cpuFlag, memoryFlag string, pidsFlag int) (*config.LimitsConfig, error) {
	var cpu float64
	var cpuSet bool

	if cmd.Flags().Changed("limit-cpu") {
		val, err := config.ParseCPU(cpuFlag)
		if err != nil {
			return nil, fmt.Errorf("invalid --limit-cpu: %w", err)
		}
		cpu = val
		cpuSet = true
	} else if env := os.Getenv("ACTR_LIMIT_CPU"); env != "" {
		val, err := config.ParseCPU(env)
		if err != nil {
			return nil, fmt.Errorf("invalid ACTR_LIMIT_CPU: %w", err)
		}
		cpu = val
		cpuSet = true
	} else if ctxLimits != nil && ctxLimits.CPU > 0 {
		cpu = ctxLimits.CPU
		cpuSet = true
	} else if yamlCfg != nil && strings.TrimSpace(yamlCfg.Limits.CPU) != "" {
		val, err := config.ParseCPU(yamlCfg.Limits.CPU)
		if err != nil {
			return nil, fmt.Errorf("invalid limits.cpu in config: %w", err)
		}
		cpu = val
		cpuSet = true
	}

	var mem int64
	var memSet bool

	if cmd.Flags().Changed("limit-memory") {
		val, err := config.ParseMemory(memoryFlag)
		if err != nil {
			return nil, fmt.Errorf("invalid --limit-memory: %w", err)
		}
		mem = val
		memSet = true
	} else if env := os.Getenv("ACTR_LIMIT_MEMORY"); env != "" {
		val, err := config.ParseMemory(env)
		if err != nil {
			return nil, fmt.Errorf("invalid ACTR_LIMIT_MEMORY: %w", err)
		}
		mem = val
		memSet = true
	} else if ctxLimits != nil && ctxLimits.Memory > 0 {
		mem = ctxLimits.Memory
		memSet = true
	} else if yamlCfg != nil && strings.TrimSpace(yamlCfg.Limits.Memory) != "" {
		val, err := config.ParseMemory(yamlCfg.Limits.Memory)
		if err != nil {
			return nil, fmt.Errorf("invalid limits.memory in config: %w", err)
		}
		mem = val
		memSet = true
	}

	var pids int64
	var pidsSet bool

	if cmd.Flags().Changed("limit-pids") {
		if pidsFlag < 0 {
			return nil, fmt.Errorf("invalid --limit-pids: cannot be negative (%d)", pidsFlag)
		}
		pids = int64(pidsFlag)
		pidsSet = true
	} else if env := os.Getenv("ACTR_LIMIT_PIDS"); env != "" {
		val, err := strconv.ParseInt(env, 10, 64)
		if err != nil || val < 0 {
			return nil, fmt.Errorf("invalid ACTR_LIMIT_PIDS %q: must be non-negative integer", env)
		}
		pids = val
		pidsSet = true
	} else if ctxLimits != nil && ctxLimits.Pids > 0 {
		pids = ctxLimits.Pids
		pidsSet = true
	} else if yamlCfg != nil && yamlCfg.Limits.Pids > 0 {
		pids = yamlCfg.Limits.Pids
		pidsSet = true
	}

	var timeout time.Duration
	if ctxLimits != nil && ctxLimits.Timeout > 0 {
		timeout = ctxLimits.Timeout
	} else if yamlCfg != nil && strings.TrimSpace(yamlCfg.Limits.Timeout) != "" {
		t, err := time.ParseDuration(yamlCfg.Limits.Timeout)
		if err != nil {
			return nil, fmt.Errorf("invalid limits.timeout in config: %w", err)
		}
		timeout = t
	}

	if cpuSet || memSet || pidsSet || timeout > 0 {
		return &config.LimitsConfig{
			CPU:     cpu,
			Memory:  mem,
			Pids:    pids,
			Timeout: timeout,
		}, nil
	}
	return nil, nil
}

func resolveDriver(cmd *cobra.Command, yamlCfg *config.RunnerYAMLConfig, driverFlag string) (string, error) {
	val := "host"
	if cmd.Flags().Changed("driver") {
		val = driverFlag
	} else if env := os.Getenv("ACTR_DRIVER"); env != "" {
		val = env
	} else if yamlCfg != nil && strings.TrimSpace(yamlCfg.Driver.Default) != "" {
		val = yamlCfg.Driver.Default
	}

	cleaned := strings.ToLower(strings.TrimSpace(val))
	if cleaned != "host" && cleaned != "docker" {
		return "", fmt.Errorf("invalid driver %q: must be \"host\" or \"docker\"", val)
	}
	return cleaned, nil
}

package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/adrijshikhar/actr/internal/config"
	"github.com/adrijshikhar/actr/internal/protocol"
	"github.com/adrijshikhar/actr/internal/registration"
	"github.com/adrijshikhar/actr/internal/service"
	"github.com/spf13/cobra"
)

var (
	isTerminalFunc   = registration.IsTerminal
	detectRemoteFunc = func(ctx context.Context, dir string) (*registration.TargetInfo, error) {
		return registration.DetectGitRemote(ctx, dir, nil)
	}
	fetchGHTokenFunc = func(ctx context.Context, target *registration.TargetInfo) (string, error) {
		return registration.FetchRegistrationTokenGH(ctx, target, nil)
	}
	openBrowserFunc = func(url string) error {
		return registration.OpenBrowser(url, nil)
	}
	serviceInstallFunc = func(binPath string, configDirs ...string) error {
		return service.Install(binPath, configDirs...)
	}
	serviceStartFunc = service.Start
)

func newRegisterCmd() *cobra.Command {
	var (
		url           string
		token         string
		name          string
		repoAlias     string
		tokenStdin    bool
		limitCPU      string
		limitMemory   string
		configDir     string
		workFolder    string
		force         bool
		promptService bool
		labels        []string
	)

	cmd := &cobra.Command{
		Use:   "register",
		Short: "Register this runner with a GitHub repository or organization",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			if ctx == nil {
				ctx = context.Background()
			}

			// Pre-validate resource limit flags if provided
			if limitCPU != "" {
				if _, err := config.ParseCPU(limitCPU); err != nil {
					return fmt.Errorf("invalid --limit-cpu: %w", err)
				}
			}
			if limitMemory != "" {
				if _, err := config.ParseMemory(limitMemory); err != nil {
					return fmt.Errorf("invalid --limit-memory: %w", err)
				}
			}

			// Resolve --token-stdin
			if tokenStdin {
				scanner := bufio.NewScanner(cmd.InOrStdin())
				if scanner.Scan() {
					token = strings.TrimSpace(scanner.Text())
				}
				if err := scanner.Err(); err != nil {
					return fmt.Errorf("read token from stdin: %w", err)
				}
			}

			// Resolve config directory precedence (matches run.go resolveConfigDir)
			configDir = resolveConfigDir(configDir, cmd.Flags().Changed("config-dir"))

			// Resolve environment variables (Tier 2 precedence: RUNNER_URL, RUNNER_TOKEN)
			if url == "" {
				if envURL := os.Getenv("RUNNER_URL"); envURL != "" {
					url = strings.TrimSpace(envURL)
				}
			}
			if token == "" {
				if envToken := os.Getenv("RUNNER_TOKEN"); envToken != "" {
					token = strings.TrimSpace(envToken)
				}
			}

			// Interactive detection:
			// If --url and --token are provided, run headless (100% backward compatible).
			// Otherwise, if attached to a terminal, run interactive wizard.
			isInteractive := isTerminalFunc(os.Stdin.Fd())
			if url != "" && token != "" {
				isInteractive = false
			}

			if !isInteractive && (url == "" || token == "") {
				return fmt.Errorf("--url and --token are required")
			}

			reader := bufio.NewReader(cmd.InOrStdin())

			// Overwrite guard: check if targetDir is known early (single-tenant or explicit --repo-alias)
			isMultiRepoDir := config.IsMultiRepoConfig(filepath.Join(configDir, "config.yaml"))
			var checkedTargetDir string
			if repoAlias != "" {
				checkedTargetDir = filepath.Join(configDir, "repos", repoAlias)
			} else if !isMultiRepoDir && limitCPU == "" && limitMemory == "" {
				checkedTargetDir = configDir
			}

			if checkedTargetDir != "" {
				runnerPath := filepath.Join(checkedTargetDir, ".runner")
				if _, err := os.Stat(runnerPath); err == nil {
					if !force {
						if isInteractive {
							overwrite, err := registration.PromptConfirm(
								reader,
								cmd.OutOrStdout(),
								fmt.Sprintf("Runner is already registered in %s. Overwrite?", checkedTargetDir),
								false,
							)
							if err != nil {
								return err
							}
							if !overwrite {
								return nil
							}
						} else {
							return fmt.Errorf("runner is already registered in %s; use --force to overwrite", checkedTargetDir)
						}
					}
				}
			}

			var target *registration.TargetInfo

			// Resolve URL
			if isInteractive && url == "" {
				detectedTarget, err := detectRemoteFunc(ctx, ".")
				if err == nil && detectedTarget != nil && detectedTarget.WebURL != "" {
					_, _ = fmt.Fprintf(cmd.OutOrStdout(), "Detected repository: %s\n", detectedTarget.WebURL)
					promptedURL, err := registration.Prompt(
						reader,
						cmd.OutOrStdout(),
						"Enter repository or organization URL",
						detectedTarget.WebURL,
					)
					if err != nil {
						return err
					}
					url = strings.TrimSpace(promptedURL)
					if url == "" {
						url = detectedTarget.WebURL
					}
				} else {
					promptedURL, err := registration.Prompt(
						reader,
						cmd.OutOrStdout(),
						"Enter repository or organization URL",
						"",
					)
					if err != nil {
						return err
					}
					url = strings.TrimSpace(promptedURL)
					if url == "" {
						return fmt.Errorf("repository or organization URL is required")
					}
				}
			}

			// Parse target for token fetching and browser settings
			if url != "" {
				parsedTarget, err := registration.ParseTargetURL(url)
				if err == nil {
					target = parsedTarget
					if strings.HasPrefix(url, "git@") || strings.HasPrefix(url, "ssh://") {
						url = target.WebURL
					}
				}
			}

			// Resolve alias for multi-repo
			var alias string
			if repoAlias != "" {
				alias = repoAlias
			} else if isMultiRepoDir || limitCPU != "" || limitMemory != "" {
				if cmd.Flags().Changed("name") && name != "actr" {
					alias = name
				} else if target != nil && target.Repo != "" {
					alias = target.Repo
				}
				if isInteractive && alias == "" {
					defaultAlias := ""
					if target != nil && target.Repo != "" {
						defaultAlias = target.Repo
					}
					promptedAlias, err := registration.Prompt(
						reader,
						cmd.OutOrStdout(),
						"Repository alias",
						defaultAlias,
					)
					if err == nil && strings.TrimSpace(promptedAlias) != "" {
						alias = strings.TrimSpace(promptedAlias)
					}
				}
				if alias == "" {
					alias = name
				}
			}

			targetDir := configDir
			if alias != "" {
				targetDir = filepath.Join(configDir, "repos", alias)
			}

			// Overwrite guard: check if .runner exists (if not already verified earlier)
			if checkedTargetDir == "" {
				runnerPath := filepath.Join(targetDir, ".runner")
				if _, err := os.Stat(runnerPath); err == nil {
					if !force {
						if isInteractive {
							overwrite, err := registration.PromptConfirm(
								reader,
								cmd.OutOrStdout(),
								fmt.Sprintf("Runner is already registered in %s. Overwrite?", targetDir),
								false,
							)
							if err != nil {
								return err
							}
							if !overwrite {
								return nil
							}
						} else {
							return fmt.Errorf("runner is already registered in %s; use --force to overwrite", targetDir)
						}
					}
				}
			}

			// Resolve Token
			if isInteractive && token == "" {
				// 1. gh CLI token retrieval
				if target != nil {
					ghToken, err := fetchGHTokenFunc(ctx, target)
					if err == nil && strings.TrimSpace(ghToken) != "" {
						token = strings.TrimSpace(ghToken)
						_, _ = fmt.Fprintln(cmd.OutOrStdout(), "✓ Successfully retrieved runner registration token via gh CLI")
					}
				}

				// 2. Browser fallback
				if token == "" {
					settingsURL := ""
					if target != nil {
						settingsURL = registration.GetRunnerSettingsURL(target)
					}
					if settingsURL != "" {
						browserErr := openBrowserFunc(settingsURL)
						if browserErr == nil {
							_, _ = fmt.Fprintln(cmd.OutOrStdout(), "Opening browser to GitHub runner settings... Paste registration token below:")
						} else {
							_, _ = fmt.Fprintf(cmd.OutOrStdout(), "Please open in your browser: %s\nPaste registration token below:\n", settingsURL)
						}
					}
					promptedToken, err := registration.PromptPassword(
						reader,
						cmd.OutOrStdout(),
						"Enter runner registration token",
					)
					if err != nil {
						return err
					}
					token = strings.TrimSpace(promptedToken)
					if token == "" {
						return fmt.Errorf("runner registration token is required")
					}
				}
			}

			if url == "" || token == "" {
				return fmt.Errorf("--url and --token are required")
			}

			httpClient := HTTPClientFromContext(ctx)
			if httpClient == nil {
				httpClient = http.DefaultClient
			}

			runnerRegName := name
			if alias != "" && !cmd.Flags().Changed("name") {
				runnerRegName = alias + "-runner"
			}

			cfg, creds, rsaParams, err := protocol.RegisterRunner(ctx, httpClient, url, token, runnerRegName, workFolder, labels...)
			if err != nil {
				return fmt.Errorf("register runner: %w", err)
			}

			if err := os.MkdirAll(targetDir, 0700); err != nil {
				return fmt.Errorf("create config dir: %w", err)
			}

			if err := config.SaveRunnerConfig(targetDir, cfg); err != nil {
				return fmt.Errorf("save runner config: %w", err)
			}
			if err := os.Chmod(filepath.Join(targetDir, ".runner"), 0600); err != nil {
				return fmt.Errorf("chmod runner config: %w", err)
			}

			if err := config.SaveCredentials(targetDir, creds); err != nil {
				return fmt.Errorf("save credentials: %w", err)
			}

			rsaData, err := json.MarshalIndent(rsaParams, "", "  ")
			if err != nil {
				return fmt.Errorf("marshal rsa parameters: %w", err)
			}

			rsaPath := filepath.Join(targetDir, ".credentials_rsaparams")
			if err := os.WriteFile(rsaPath, rsaData, 0600); err != nil {
				return fmt.Errorf("write rsa parameters: %w", err)
			}
			if err := os.Chmod(rsaPath, 0600); err != nil {
				return fmt.Errorf("chmod rsa parameters: %w", err)
			}

			if alias != "" {
				cfgPath := filepath.Join(configDir, "config.yaml")
				var supCfg *config.SupervisorConfig
				if config.IsMultiRepoConfig(cfgPath) {
					loaded, err := config.LoadSupervisorConfig(cfgPath)
					if err != nil {
						return fmt.Errorf("load supervisor config: %w", err)
					}
					supCfg = loaded
				} else {
					supCfg = &config.SupervisorConfig{
						Version: "1",
						Global: config.GlobalConfig{
							MaxConcurrentJobs: 2,
						},
						Repos: make(map[string]config.RepoConfig),
					}
				}
				if supCfg.Repos == nil {
					supCfg.Repos = make(map[string]config.RepoConfig)
				}
				repoCfg := supCfg.Repos[alias]
				repoCfg.URL = url
				if repoCfg.MaxConcurrentJobs <= 0 {
					repoCfg.MaxConcurrentJobs = 1
				}
				if limitCPU != "" {
					repoCfg.Limits.CPU = limitCPU
				}
				if limitMemory != "" {
					repoCfg.Limits.Memory = limitMemory
				}
				supCfg.Repos[alias] = repoCfg
				if err := config.SaveSupervisorConfig(cfgPath, supCfg); err != nil {
					return fmt.Errorf("save supervisor config: %w", err)
				}
				fmt.Fprintf(cmd.OutOrStdout(), "Successfully registered runner '%s' (ID %d) for %s (alias: %s)\nConfiguration saved to %s\nSupervisor config updated: %s\n", cfg.AgentName, cfg.AgentID, url, alias, targetDir, cfgPath)
			} else {
				fmt.Fprintf(cmd.OutOrStdout(), "Successfully registered runner '%s' (ID %d) for %s\nConfiguration saved to %s\n", cfg.AgentName, cfg.AgentID, url, configDir)
			}

			// Post-registration in interactive TTY: prompt to start service
			if isInteractive && promptService {
				startService, err := registration.PromptConfirm(
					reader,
					cmd.OutOrStdout(),
					"Start background runner service now?",
					true,
				)
				if err == nil && startService {
					if err := serviceInstallFunc("", configDir); err != nil {
						fmt.Fprintf(cmd.ErrOrStderr(), "Warning: runner registered successfully, but service install failed: %v\nRun 'actr service install' manually.\n", err)
					} else if err := serviceStartFunc(); err != nil {
						fmt.Fprintf(cmd.ErrOrStderr(), "Warning: runner registered successfully, but service start failed: %v\nRun 'actr service start' manually.\n", err)
					} else {
						_, _ = fmt.Fprintln(cmd.OutOrStdout(), "✓ Runner service started! Run 'actr monitor' to inspect live jobs.")
					}
				}
			}

			return nil
		},
	}

	cmd.Flags().StringVar(&url, "url", "", "Repository or Organization URL")
	cmd.Flags().StringVar(&token, "token", "", "Runner registration token")
	cmd.Flags().BoolVar(&tokenStdin, "token-stdin", false, "Read registration token from stdin")
	cmd.Flags().StringVar(&name, "name", "actr", "Runner name")
	cmd.Flags().StringVar(&repoAlias, "repo-alias", "", "Repository alias for multi-repo supervisor (defaults to --name or repo name)")
	cmd.Flags().StringVar(&limitCPU, "limit-cpu", "", "Max CPU quota for repository (e.g. 2.0, 500m)")
	cmd.Flags().StringVar(&limitMemory, "limit-memory", "", "Max memory limit for repository (e.g. 4GiB, 512MB)")
	cmd.Flags().StringSliceVar(&labels, "labels", nil, "custom runner labels (optional)")
	cmd.Flags().StringVar(&configDir, "config-dir", config.DefaultConfigDir(), "path to directory containing runner configuration and credentials")
	cmd.Flags().StringVar(&workFolder, "work-folder", "_work", "path to runner work folder")
	cmd.Flags().BoolVar(&force, "force", false, "force overwrite of existing configuration")
	cmd.Flags().BoolVar(&promptService, "prompt-service", true, "prompt to start background service after registration")
	_ = cmd.Flags().MarkHidden("prompt-service")

	_ = cmd.MarkFlagDirname("config-dir")
	_ = cmd.MarkFlagDirname("work-folder")

	return cmd
}

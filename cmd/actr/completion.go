package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/adrijshikhar/actr/internal/config"
	"github.com/spf13/cobra"
)

func newCompletionCmd(rootCmd *cobra.Command) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "completion <bash|zsh|fish|powershell>",
		Short: "Generate shell completion script for the specified shell",
		Long: `Generate shell completion script for actr.
Supported shells: bash, zsh, fish, powershell.

To load completions in the current shell session:
  zsh:        source <(actr completion zsh)
  bash:       source <(actr completion bash)
  fish:       actr completion fish | source
  powershell: actr completion powershell | Out-String | Invoke-Expression

To install permanently, use 'actr completion install'.`,
		ValidArgs: []string{"bash", "zsh", "fish", "powershell"},
		Args:      cobra.MatchAll(cobra.ExactArgs(1), cobra.OnlyValidArgs),
		RunE: func(cmd *cobra.Command, args []string) error {
			shell := args[0]
			out := cmd.OutOrStdout()
			return generateCompletion(rootCmd, shell, out)
		},
	}

	cmd.AddCommand(newCompletionInstallCmd(rootCmd))

	return cmd
}

var (
	completionTargetResolver = func(shell string) (string, error) {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("lookup user home directory: %w", err)
		}
		brewPrefix := detectBrewPrefix()
		return resolveInstallPath(shell, home, brewPrefix)
	}
)

func detectBrewPrefix() string {
	if p := os.Getenv("HOMEBREW_PREFIX"); p != "" {
		return p
	}
	for _, standard := range []string{"/opt/homebrew", "/usr/local"} {
		if fi, err := os.Stat(filepath.Join(standard, "share", "zsh", "site-functions")); err == nil && fi.IsDir() {
			return standard
		}
	}
	return ""
}

func resolveInstallPath(shell string, homeDir string, brewPrefix string) (string, error) {
	shell = strings.ToLower(shell)
	switch shell {
	case "zsh":
		if brewPrefix != "" {
			siteFunctions := filepath.Join(brewPrefix, "share", "zsh", "site-functions")
			if fi, err := os.Stat(siteFunctions); err == nil && fi.IsDir() {
				return filepath.Join(siteFunctions, "_actr"), nil
			}
		}
		return filepath.Join(homeDir, ".zsh", "completions", "_actr"), nil
	case "fish":
		return filepath.Join(homeDir, ".config", "fish", "completions", "actr.fish"), nil
	case "bash":
		return filepath.Join(homeDir, ".local", "share", "bash-completion", "completions", "actr"), nil
	default:
		return "", fmt.Errorf("unsupported shell for automatic installation: %q (supported: zsh, fish, bash)", shell)
	}
}

func newCompletionInstallCmd(rootCmd *cobra.Command) *cobra.Command {
	return &cobra.Command{
		Use:       "install [bash|zsh|fish]",
		Short:     "Install completion script into your shell configuration directory",
		Args:      cobra.MaximumNArgs(1),
		ValidArgs: []string{"bash", "zsh", "fish"},
		RunE: func(cmd *cobra.Command, args []string) error {
			shell := ""
			if len(args) > 0 {
				shell = strings.ToLower(args[0])
			} else {
				envShell := os.Getenv("SHELL")
				if envShell != "" {
					shell = strings.ToLower(filepath.Base(envShell))
				}
			}
			if shell == "" {
				return fmt.Errorf("unable to detect shell from $SHELL; please specify explicitly (zsh, bash, fish)")
			}

			targetPath, err := completionTargetResolver(shell)
			if err != nil {
				return err
			}

			if err := os.MkdirAll(filepath.Dir(targetPath), 0755); err != nil {
				return fmt.Errorf("create directory %s: %w", filepath.Dir(targetPath), err)
			}

			f, err := os.OpenFile(targetPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0644)
			if err != nil {
				return fmt.Errorf("write completion file %s: %w", targetPath, err)
			}

			genErr := generateCompletion(rootCmd, shell, f)
			_ = f.Close()
			if genErr != nil {
				_ = os.Remove(targetPath)
				return genErr
			}

			fmt.Fprintf(cmd.OutOrStdout(), "✓ Installed %s completion to %s\n", shell, targetPath)
			if shell == "zsh" {
				fmt.Fprintln(cmd.OutOrStdout(), "Restart your shell or run:")
				fmt.Fprintln(cmd.OutOrStdout(), "  autoload -Uz compinit && compinit")
			} else {
				fmt.Fprintf(cmd.OutOrStdout(), "Restart your shell or run: source %s\n", targetPath)
			}
			return nil
		},
	}
}

func generateCompletion(rootCmd *cobra.Command, shell string, out io.Writer) error {
	shell = strings.ToLower(shell)
	switch shell {
	case "bash":
		return rootCmd.GenBashCompletionV2(out, true)
	case "zsh":
		return rootCmd.GenZshCompletion(out)
	case "fish":
		return rootCmd.GenFishCompletion(out, true)
	case "powershell":
		return rootCmd.GenPowerShellCompletionWithDesc(out)
	default:
		return fmt.Errorf("unsupported shell %q (supported: bash, zsh, fish, powershell)", shell)
	}
}

func completeLabels(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	labels := []string{
		"self-hosted\tDefault self-hosted runner label",
		"macOS\tmacOS operating system label",
		"ARM64\tApple Silicon / ARM64 architecture label",
		"linux\tLinux operating system label",
		"x64\tx86_64 architecture label",
	}

	prefix := ""
	token := toComplete
	if idx := strings.LastIndex(toComplete, ","); idx != -1 {
		prefix = toComplete[:idx+1]
		token = toComplete[idx+1:]
	}

	tokenLower := strings.ToLower(token)
	var matches []string
	for _, l := range labels {
		parts := strings.SplitN(l, "\t", 2)
		name := parts[0]
		if strings.HasPrefix(strings.ToLower(name), tokenLower) {
			if len(parts) == 2 {
				matches = append(matches, fmt.Sprintf("%s%s\t%s", prefix, name, parts[1]))
			} else {
				matches = append(matches, prefix+name)
			}
		}
	}
	slices.Sort(matches)
	return matches, cobra.ShellCompDirectiveNoFileComp
}

func completeRunnerNames(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	configDir := config.DefaultConfigDir()
	var changed bool
	if f := cmd.Flag("config-dir"); f != nil {
		configDir = f.Value.String()
		changed = f.Changed
	}
	cfgDir := resolveConfigDir(configDir, changed)
	var names []string

	// Check multi-repo configs
	multiCfgPath := filepath.Join(cfgDir, "config.yaml")
	if _, err := os.Stat(multiCfgPath); os.IsNotExist(err) {
		multiCfgPath = filepath.Join(cfgDir, "config.yml")
	}
	if multiCfg, err := config.LoadSupervisorConfig(multiCfgPath); err == nil && multiCfg != nil {
		for rName := range multiCfg.Repos {
			names = append(names, fmt.Sprintf("%s\tRepository runner worker", rName))
		}
	}

	// Check single-runner config
	if rCfg, err := config.LoadRunnerConfig(cfgDir); err == nil && rCfg != nil && rCfg.AgentName != "" {
		names = append(names, fmt.Sprintf("%s\tConfigured runner instance", rCfg.AgentName))
	}

	var matches []string
	prefix := strings.ToLower(toComplete)
	for _, n := range names {
		if strings.HasPrefix(strings.ToLower(n), prefix) {
			matches = append(matches, n)
		}
	}
	slices.Sort(matches)
	return matches, cobra.ShellCompDirectiveNoFileComp
}

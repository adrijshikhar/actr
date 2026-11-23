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
		Args:      cobra.ExactValidArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			shell := args[0]
			out := cmd.OutOrStdout()
			return generateCompletion(rootCmd, shell, out)
		},
	}

	return cmd
}

func generateCompletion(rootCmd *cobra.Command, shell string, out io.Writer) error {
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
	var matches []string
	prefix := strings.ToLower(toComplete)
	for _, l := range labels {
		if strings.HasPrefix(strings.ToLower(l), prefix) {
			matches = append(matches, l)
		}
	}
	slices.Sort(matches)
	return matches, cobra.ShellCompDirectiveNoFileComp
}

func completeRunnerNames(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	cfgDir := config.DefaultConfigDir()
	if f := cmd.Flag("config-dir"); f != nil && f.Value.String() != "" {
		cfgDir = f.Value.String()
	}
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

package main

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"text/tabwriter"

	"github.com/adrijshikhar/actr/internal/config"
	"github.com/adrijshikhar/actr/internal/vault"
	"github.com/spf13/cobra"
	"golang.org/x/term"
)

var (
	isTerminalReaderFunc = func(r io.Reader) bool {
		if f, ok := r.(*os.File); ok {
			return isTerminalFunc(f.Fd())
		}
		return false
	}
	readPasswordFunc = func(fd int) ([]byte, error) {
		return term.ReadPassword(fd)
	}
	vaultProviderResolver = resolveVaultProvider
)

func newSecretsCommand() *cobra.Command {
	var (
		configDirFlag   string
		secretValueFlag string
	)

	cmd := &cobra.Command{
		Use:   "secrets",
		Short: "Manage runner-local credentials in the secret vault",
	}

	cmd.PersistentFlags().StringVar(&configDirFlag, "config-dir", config.DefaultConfigDir(), "path to configuration directory")
	_ = cmd.MarkFlagDirname("config-dir")

	setCmd := &cobra.Command{
		Use:   "set <KEY>",
		Short: "Store or update a secret in the vault",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			key := args[0]
			var val string

			if cmd.Flags().Changed("value") {
				val = secretValueFlag
			} else {
				in := cmd.InOrStdin()
				if isTerminalReaderFunc(in) {
					fmt.Fprintf(cmd.OutOrStdout(), "Enter secret value for %s: ", key)
					fd := int(os.Stdin.Fd())
					if f, ok := in.(*os.File); ok {
						fd = int(f.Fd())
					}
					pass, err := readPasswordFunc(fd)
					fmt.Fprintln(cmd.OutOrStdout())
					if err != nil {
						return fmt.Errorf("read secret value: %w", err)
					}
					val = string(pass)
				} else {
					reader := bufio.NewReader(in)
					line, err := reader.ReadString('\n')
					if err != nil && err != io.EOF {
						return fmt.Errorf("read secret value: %w", err)
					}
					val = strings.TrimRight(line, "\r\n")
				}
			}

			v, err := vaultProviderResolver(cmd)
			if err != nil {
				return err
			}
			if err := v.Set(cmd.Context(), key, val); err != nil {
				return fmt.Errorf("set secret %s: %w", key, err)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "✓ Stored %s in %s\n", key, v.Name())
			return nil
		},
	}
	setCmd.Flags().StringVar(&secretValueFlag, "value", "", "Secret value (bypasses masked prompt)")

	getCmd := &cobra.Command{
		Use:   "get <KEY>",
		Short: "Retrieve and print a decrypted secret from the vault",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			key := args[0]
			v, err := vaultProviderResolver(cmd)
			if err != nil {
				return err
			}
			val, err := v.Get(cmd.Context(), key)
			if err != nil {
				return err
			}
			fmt.Fprintln(cmd.OutOrStdout(), val)
			return nil
		},
	}

	listCmd := &cobra.Command{
		Use:   "list",
		Short: "List all secret keys available in the vault",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			v, err := vaultProviderResolver(cmd)
			if err != nil {
				return err
			}
			list, err := v.List(cmd.Context())
			if err != nil {
				return err
			}
			w := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 3, ' ', 0)
			fmt.Fprintln(w, "KEY\tPROVIDER\tUPDATED")
			for _, m := range list {
				fmt.Fprintf(w, "%s\t%s\t%s\n", m.Key, m.Provider, m.UpdatedAt)
			}
			return w.Flush()
		},
	}

	deleteCmd := &cobra.Command{
		Use:   "delete <KEY>",
		Short: "Remove a secret key from the vault",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			key := args[0]
			v, err := vaultProviderResolver(cmd)
			if err != nil {
				return err
			}
			if err := v.Delete(cmd.Context(), key); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "✓ Deleted %s\n", key)
			return nil
		},
	}

	cmd.AddCommand(setCmd, getCmd, listCmd, deleteCmd)
	return cmd
}

func resolveVaultProvider(cmd *cobra.Command) (vault.Provider, error) {
	flags := cmd.Flags()
	configDir := config.DefaultConfigDir()
	var changed bool
	if f := flags.Lookup("config-dir"); f != nil {
		configDir = f.Value.String()
		changed = f.Changed
	}
	cfgDir := resolveConfigDir(configDir, changed)

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

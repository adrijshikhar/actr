package main

import (
	"io"
	"sync"

	"github.com/adrijshikhar/actr/internal/logger"
	"github.com/spf13/cobra"
)

var (
	versionMu sync.RWMutex
	Version   = "v0.2.0"
	Commit    = "none"
	Date      = "unknown"

	debugFlag bool
)

func GetVersion() string {
	versionMu.RLock()
	defer versionMu.RUnlock()
	return Version
}

func SetVersion(v string) {
	versionMu.Lock()
	defer versionMu.Unlock()
	Version = v
}

func newRootCmd(out, errOut io.Writer) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "actr",
		Short:   "Native, high-performance GitHub Actions self-hosted runner daemon",
		Long:    "actr is a lightweight, single-binary self-hosted runner daemon written in Go.",
		Version: GetVersion(),
		PersistentPreRun: func(cmd *cobra.Command, args []string) {
			logger.Init(debugFlag, errOut)
		},
	}

	cmd.SetOut(out)
	cmd.SetErr(errOut)
	cmd.PersistentFlags().BoolVar(&debugFlag, "debug", false, "enable debug logging")

	cmd.AddCommand(newDoctorCmd())
	cmd.AddCommand(newServiceCmd())
	cmd.AddCommand(newRegisterCmd())
	cmd.AddCommand(newRunCmd())
	cmd.AddCommand(newMonitorCmd())
	cmd.AddCommand(newSecretsCommand())

	return cmd
}

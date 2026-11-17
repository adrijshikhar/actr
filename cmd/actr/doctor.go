package main

import (
	"fmt"

	"github.com/adrijshikhar/actr/internal/diagnostics"
	"github.com/spf13/cobra"
)

func newDoctorCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "doctor",
		Short: "Check system prerequisites and runner dependencies",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			results := diagnostics.RunAllChecks()
			for _, res := range results {
				status := "✓"
				if !res.Passed {
					status = "✗"
				}
				fmt.Fprintf(cmd.OutOrStdout(), "[%s] %s: %s\n", status, res.Name, res.Message)
			}
			return nil
		},
	}
}

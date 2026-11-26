package main

import (
	"context"
	"io"
	"os"
	"os/signal"
	"syscall"
)

func dispatchWithContext(ctx context.Context, args []string, out, errOut io.Writer) int {
	return dispatchWithContextAndIn(ctx, os.Stdin, args, out, errOut)
}

func dispatchWithContextAndIn(ctx context.Context, in io.Reader, args []string, out, errOut io.Writer) int {
	rootCmd := newRootCmd(out, errOut)
	if in != nil {
		rootCmd.SetIn(in)
	}
	rootCmd.SetArgs(args)

	if err := rootCmd.ExecuteContext(ctx); err != nil {
		return 1
	}
	return 0
}

func runCLI() int {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	return dispatchWithContext(ctx, os.Args[1:], os.Stdout, os.Stderr)
}

func main() {
	os.Exit(runCLI())
}

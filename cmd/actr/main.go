package main

import (
	"context"
	"io"
	"os"
	"os/signal"
	"syscall"

	"github.com/adrijshikhar/actr/internal/logger"
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

	logger.Init(debugFlag, errOut)

	if err := rootCmd.ExecuteContext(ctx); err != nil {
		return 1
	}
	return 0
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	code := dispatchWithContext(ctx, os.Args[1:], os.Stdout, os.Stderr)
	os.Exit(code)
}

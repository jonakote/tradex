package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"tradex/internal/mcp"
)

func main() {
	workspace := os.Getenv("TRADEX_WORKSPACE")
	specRoot := os.Getenv("HERMES_SPEC_ROOT")
	server, err := mcp.NewServerWithSpecRoot(workspace, specRoot)
	if err != nil {
		fmt.Fprintln(os.Stderr, "tradex-mcp:", err)
		os.Exit(1)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := server.Serve(ctx, os.Stdin, os.Stdout); err != nil && ctx.Err() == nil {
		fmt.Fprintln(os.Stderr, "tradex-mcp:", err)
		os.Exit(1)
	}
}

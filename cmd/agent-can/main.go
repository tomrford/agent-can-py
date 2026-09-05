package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/tomrford/agent-can/internal/server"
	"github.com/tomrford/agent-can/internal/session"
)

// Release builds inject the package version using -ldflags.
var version = "dev"

func run() error {
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "--version":
			fmt.Println("agent-can " + version)
			return nil
		case "--help", "-h":
			fmt.Println("Usage: agent-can [--version]\n\nRun the CAN session server over stdio MCP. Closing stdin stops the session.")
			return nil
		default:
			return fmt.Errorf("unknown argument %q; use --help", os.Args[1])
		}
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	if err := watchLauncher(ctx, cancel); err != nil {
		return err
	}
	sessions := session.New()
	err := server.New(sessions, version).Run(ctx, &mcp.StdioTransport{})
	if errors.Is(err, context.Canceled) {
		err = nil
	}
	return errors.Join(err, sessions.Close())
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "agent-can:", err)
		os.Exit(1)
	}
}

//go:build !windows

package main

import "context"

// On POSIX the launcher execs Go, so the MCP client already owns this process.
func watchLauncher(context.Context, context.CancelFunc) error { return nil }

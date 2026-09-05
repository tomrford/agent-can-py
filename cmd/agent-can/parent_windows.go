//go:build windows

package main

import (
	"context"
	"fmt"
	"os"
	"strconv"

	"golang.org/x/sys/windows"
)

// A Windows launcher cannot exec. Stop the session if that launcher is killed,
// even when the MCP client still holds its stdin pipe open.
func watchLauncher(ctx context.Context, cancel context.CancelFunc) error {
	text := os.Getenv("AGENT_CAN_LAUNCHER_PID")
	if text == "" {
		return nil
	}
	pid, err := strconv.ParseUint(text, 10, 32)
	if err != nil || pid == 0 {
		return fmt.Errorf("invalid launcher PID %q", text)
	}
	handle, err := windows.OpenProcess(windows.SYNCHRONIZE, false, uint32(pid))
	if err != nil {
		return fmt.Errorf("watch launcher: %w", err)
	}
	go func() {
		defer windows.CloseHandle(handle)
		for ctx.Err() == nil {
			state, err := windows.WaitForSingleObject(handle, 250)
			if err != nil || state != uint32(windows.WAIT_TIMEOUT) {
				cancel()
				return
			}
		}
	}()
	return nil
}

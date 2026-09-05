package server

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/tomrford/agent-can/internal/session"
)

func TestMCPDiscoveryValidationAndSession(t *testing.T) {
	ctx := context.Background()
	manager := session.New()
	defer manager.Close()
	clientTransport, serverTransport := mcp.NewInMemoryTransports()
	serverSession, err := New(manager, "test").Connect(ctx, serverTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer serverSession.Close()
	client := mcp.NewClient(&mcp.Implementation{Name: "test"}, nil)
	clientSession, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer clientSession.Close()
	listed, err := clientSession.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]bool{}
	for _, tool := range listed.Tools {
		names[tool.Name] = true
	}
	for _, name := range []string{"buses_list", "connect", "disconnect", "status", "schema", "message_list", "message_read", "frame_send", "message_send", "message_stop", "trace_start", "trace_stop"} {
		if !names[name] {
			t.Errorf("missing tool %s", name)
		}
	}
	call := func(name string, args map[string]any, wantError bool) *mcp.CallToolResult {
		t.Helper()
		result, err := clientSession.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: args})
		if err != nil {
			t.Fatal(err)
		}
		if result.IsError != wantError {
			t.Fatalf("%s result: %+v", name, result)
		}
		return result
	}
	path, err := filepath.Abs("../../examples/demo.dbc")
	if err != nil {
		t.Fatal(err)
	}
	call("connect", map[string]any{"channel": "virtual:agent-can", "dbcs": []map[string]string{{"alias": "demo", "path": path}}}, false)
	call("frame_send", map[string]any{"target": "0x123", "data": "AA", "unknown_flag": true}, true)
	call("frame_send", map[string]any{"target": "0x123", "data": map[string]int{"counter": 1}}, true)
	call("message_send", map[string]any{"target": "demo.Heartbeat", "signals": "AA"}, true)
	call("message_send", map[string]any{"target": "demo.Heartbeat", "signals": map[string]int{"counter": 1, "mode": 2}, "fd": true}, true)
	call("message_read", map[string]any{"select": "demo.Heartbeat", "direction": "tx"}, true)
	call("message_send", map[string]any{"target": "demo.Heartbeat", "signals": map[string]int{"counter": 1, "mode": 2}, "periodicity_ms": 10}, false)
	call("message_stop", map[string]any{"target": "demo.Heartbeat"}, false)
	call("frame_send", map[string]any{"target": "0x123", "data": strings.Repeat("AB", 12), "extended": true, "fd": true}, false)
	result := call("message_read", map[string]any{"select": "0x123", "direction": "tx", "extended": true}, false)
	encoded, err := json.Marshal(result.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(encoded), `"len":12`) || !strings.Contains(string(encoded), `"extended":true`) {
		t.Fatalf("wire flags lost: %s", encoded)
	}
	call("disconnect", map[string]any{}, false)
	call("message_read", map[string]any{"select": "0x123"}, true)
}

package server

import (
	"bytes"
	"context"
	"encoding/json"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/tomrford/agent-can/internal/session"
)

func New(sessions *session.Manager, version string) *mcp.Server {
	s := mcp.NewServer(&mcp.Implementation{Name: "agent-can", Version: version}, nil)
	add(s, "buses_list", "Discover CAN channels and driver capabilities; use the returned identifiers in connect.", session.Buses)
	add(s, "connect", "Open the one CAN session owned by this process. Load and validate all DBCs before opening hardware. DBC overlays are immutable until disconnect.", sessions.Connect)
	add(s, "disconnect", "Stop periodic sends, close the bus, finalise recording and release the session.", sessions.Disconnect)
	add(s, "status", "Connection, DBCs, schedule health, trace state, capture retention and supported trace formats.", sessions.Status)
	add(s, "schema", "Discover the connect-time DBC catalog. Filter by raw ID, case-insensitive partial name or glob.", sessions.Schema)
	add(s, "message_list", "Inventory of received traffic in the last 60 seconds, one row per raw frame identity with matching DBC names attached. Filter by raw ID, case-insensitive partial name or glob.", sessions.List)
	add(s, "message_read", "Read one exact frame identity in reverse capture order. Raw reads default to standard CAN; use extended for 29-bit frames. Defaults to RX; select tx for accepted transmissions. Semantic values include units and choice labels; signal_errors reports undecodable or inactive signals.", sessions.Read)
	add(s, "frame_send", "Send a raw hex payload. Periodic sends belong to the session and survive this call. CAN FD lengths must be 0-8, 12, 16, 20, 24, 32, 48 or 64 bytes.", sessions.SendFrame)
	add(s, "message_send", "Encode and send every active signal of an exact DBC message. Frame flags come from the DBC. Periodic sends belong to the session and survive this call.", sessions.Send)
	add(s, "message_stop", "Stop a periodic schedule by exact target. For raw targets, extended selects the 29-bit schedule.", sessions.Stop)
	add(s, "trace_start", "Start ASC recording of future RX, accepted TX and controller events. Omit path for an automatically named file. Existing files are never overwritten.", sessions.TraceStart)
	add(s, "trace_stop", "Flush and close the current trace; report any recording failure.", sessions.TraceStop)
	return s
}

func add[In any](server *mcp.Server, name, description string, call func(context.Context, In) (session.Result, error)) {
	mcp.AddTool(server, &mcp.Tool{Name: name, Description: description},
		func(ctx context.Context, request *mcp.CallToolRequest, _ In) (*mcp.CallToolResult, any, error) {
			// Typed SDK validation passes numbers through float64. Read the
			// original arguments so 64-bit signal values retain every bit.
			var input In
			decoder := json.NewDecoder(bytes.NewReader(request.Params.Arguments))
			decoder.UseNumber()
			if len(request.Params.Arguments) > 0 {
				if err := decoder.Decode(&input); err != nil {
					return nil, nil, err
				}
			}
			result, err := call(ctx, input)
			return nil, result, err
		})
}

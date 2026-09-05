# agent-can

Agent-facing CAN session server built in Go on [gocan](https://github.com/tomrford/gocan).
One stdio MCP process owns one bus, its raw capture, connect-time DBC overlays,
periodic transmissions and trace recording. Closing stdin or stopping the process
stops transmission, closes the bus and finalises the trace.

## Run

PyPI wheels bundle the native executable with a small Python launcher. Go and
Python CAN libraries are unnecessary when installing a supported wheel:

```json
{
  "mcpServers": {
    "agent-can": {"command": "uvx", "args": ["agent-can"]}
  }
}
```

Wheels target Windows x64, Linux glibc x64/arm64, and macOS 12+ x64/arm64.
Vendor hardware still requires the host's drivers. Source installations require
Go 1.25 or newer. The executable also runs directly; `agent-can --version` reports
the packaged version. There is currently no web UI or persistent daemon.

## Connect

Call `buses_list`, then pass its exact `channel` value to `connect`.
The channel also identifies its driver.
Discovery reports available channels even if another vendor's discovery fails.
The virtual channel is an in-process loopback for development:

```json
{"channel": "virtual:agent-can"}
```

SocketCAN uses timing already configured by Linux; omit timing fields. PCAN and
Vector classical CAN connections take `bitrate`. CAN FD on programmable hardware
takes exact `fd_timing` instead:

```json
{
  "channel": "pcan:0x51",
  "fd_timing": {
    "clock_hz": 80000000,
    "nominal": {"brp": 1, "tseg1": 127, "tseg2": 32, "sjw": 32},
    "data": {"brp": 1, "tseg1": 31, "tseg2": 8, "sjw": 8}
  }
}
```

Use a channel actually returned by discovery and timing appropriate for your
network. The example timing gives 500 kbit/s arbitration and 2 Mbit/s data.
See [gocan driver support](https://github.com/tomrford/gocan#driver-support) for
the current hardware/platform matrix and restrictions.

DBCs are optional `dbcs: [{"alias": "vehicle", "path": "/absolute/file.dbc"}]`
connect arguments. Every file is validated before hardware opens. Aliases must be
unique and cannot contain dots, whitespace or wildcard characters. Files must be
regular files of at most 32 MiB; resolved paths and parser diagnostics appear in
status. Disconnect and reconnect to add, replace or remove an overlay. Raw capture
remains the source of truth.

## Tools

| Tool | Behaviour |
| --- | --- |
| `buses_list` | Discover channels, CAN FD support and externally configured timing |
| `connect`, `disconnect` | Open or close the process-owned session |
| `status` | Connection, DBCs, schedule health, trace state and retention |
| `schema` | DBC messages and signals; optional partial, glob or raw-ID filter |
| `message_list` | Received-traffic inventory; one row per raw identity with matching DBC `names` |
| `message_read` | Latest observations for one raw ID or exact semantic name |
| `frame_send` | Send a raw hex payload once or periodically |
| `message_send` | Send a complete DBC signal map once or periodically |
| `message_stop` | Cancel a periodic raw or semantic transmission |
| `trace_start`, `trace_stop` | ASC recording with final flush and explicit error reporting |

## Read and send

`frame_send` takes a raw hex payload. Standard classical CAN is the default:

```json
{"target": "0x123", "data": "01020304"}
```

`extended`, `fd` and `bitrate_switch` select raw frame flags. CAN FD payload lengths
must be 0–8, 12, 16, 20, 24, 32, 48 or 64 bytes; payloads are never padded silently.
`len` is the byte length; `dlc` is the wire length code.

`message_send` takes an exact `alias.Message` and every active signal:

```json
{
  "target": "vehicle.PowertrainStatus",
  "signals": {"vehicle_speed": 12.3, "engine_rpm": 1200, "throttle": 20, "coolant_temp": 82}
}
```

Semantic frame flags come from the DBC. Choice labels are accepted as signal values.
Missing, unknown and inactive signal inputs fail before sending. Large integer
inputs retain all 64 bits; use integer
notation for values at or above 2^53 rather than decimal or exponent notation.

Add `periodicity_ms` (1 through 86400000) for recurring sends. Each target has one
schedule; raw standard and extended targets are separate. A successful replacement
starts immediately and stops the previous task; the previous task can finish an
in-flight send during replacement. A failed replacement preserves the previous
schedule. A one-shot send leaves an existing schedule running. Failed schedules
remain visible in `status`. `message_stop` accepts the same target and, for raw
extended schedules, `extended: true`.

`message_read` takes `select`, optional `count` (1–4096, default 1), and optional
`direction` (`rx` by default, or `tx` for accepted transmissions). Results follow
reverse capture order, including when timestamps tie or regress. Raw reads select
standard CAN by default; use `extended: true` for 29-bit frames. Semantic names
determine their own frame format. Semantic observations contain a signal-name map of values and units;
enumerations decode to their choice label. `signal_errors` reports inactive,
malformed or non-finite signals while retaining the raw frame and other signals.

Inventory always includes unmatched raw traffic. Each row identifies an arbitration
ID and standard/extended format, with all matching DBC names in `names` (empty for
unmatched traffic). Filters select rows by raw ID or DBC name without dropping the
row's other names. `schema` separately lists the DBC catalog, including unseen
messages.

Reads and inventory cover the last 60 seconds. Capture storage is pruned in whole
gocan chunks, so allocated/retained storage can exceed that logical window. A
recorder that falls behind pruned history fails explicitly. Inventory counts cover
observations since that identity was last added; cycle estimates use acquisition
timestamps. Accepted TX records indicate driver acceptance, not confirmation from
an ECU.

## Trace export

ASC is the currently supported format, exposed by `status.trace_formats`.
`trace_start({"format": "asc"})` creates a unique file in the user cache directory
under `agent-can/traces` and returns its absolute path and format. An explicit
`path` must be absolute, end in `.asc`, and have an existing parent directory.
Existing files are never overwritten. Stop an active trace before starting another.

Recording includes future RX frames, accepted transmissions and controller events.
`trace_stop` flushes and closes the writer; failures also appear in status.

## Development

```sh
go run ./cmd/agent-can
go test -race ./...
go vet ./...
gofmt -l cmd internal
sfw uv run ruff check .
sfw uv run pytest
sfw uv build --no-sources
```

After editing Go files, `sfw uv sync --reinstall-package agent-can` rebuilds the
executable used by the editable Python launcher. CI tests the runtime and an
installed wheel on every packaged platform. Published GitHub releases rebuild and
test all wheels before PyPI trusted publishing. Keep `pyproject.toml`, the launcher
version and release tag aligned; the build injects that version into Go.

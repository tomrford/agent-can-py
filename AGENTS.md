# agent-can

Agent-facing CAN session server: a Go executable using `gocan`, exposed over stdio
MCP with the official Go SDK. PyPI wheels contain the executable and a small Python
launcher. There is no web UI or daemon.

## Build and check

Before handoff, run the full gate unless the task is explicitly read-only or a
host dependency is missing:

```sh
go test -race ./...
go vet ./...
gofmt -l cmd internal
sfw uv run ruff check .
sfw uv run pytest
sfw uv build --no-sources
```

Rebuild the editable launcher after Go changes with
`sfw uv sync --reinstall-package agent-can`. The Python tests exercise the installed
executable over real stdio, including shutdown while transmitting and recording.

## Design

- Raw-first: `gocan.Capture` owns raw traffic; DBCs are a semantic overlay.
- One live bus per MCP process. Sessions end with stdin EOF or process shutdown.
- DBCs are immutable connect-time inputs, all validated before hardware opens.
- Every active signal is required for semantic sends. Never silently pad raw FD
  payloads or round large JSON integers through float64.
- Acquisition is driver-owned. MCP and future frontends call the same session
  operations; keep transport concerns out of the runtime.
- Retention is 60 seconds logically, with gocan's chunk-granular storage pruning.
- Use gocan's codecs, scheduling and recorder rather than duplicating them here.

## Release

CI checks Go, the launcher, source builds and installed native wheels on Linux,
macOS and Windows. Published non-prerelease GitHub releases trigger PyPI trusted
publishing after the same matrix passes. Keep `pyproject.toml`,
`src/agent_can/__init__.py` and the release tag aligned. The wheel build injects the
package version into the Go executable. Build wheels natively; do not mislabel
cross-compiled binaries with host wheel tags.

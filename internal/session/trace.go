package session

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/tomrford/gocan/asc"
	"github.com/tomrford/gocan/recorder"
)

type traceState struct {
	path     string
	recorder *recorder.Recorder
}

func (t *traceState) status() Result {
	if t == nil {
		return Result{"state": "stopped"}
	}
	result := Result{"path": t.path, "format": "asc", "state": "active"}
	select {
	case <-t.recorder.Done():
		result["state"] = "stopped"
		if err := t.recorder.Err(); err != nil {
			result["state"], result["error"] = "failed", err.Error()
		}
	default:
	}
	return result
}

type traceWriter struct {
	*asc.Writer
	file *os.File
}

func (w *traceWriter) Close() error { return errors.Join(w.Writer.Close(), w.file.Close()) }

func traceFile(request TraceRequest) (*os.File, error) {
	if request.Format != "" && request.Format != "asc" {
		return nil, fmt.Errorf("unsupported trace format %q; supported formats: asc", request.Format)
	}
	if request.Path == "" {
		cache, err := os.UserCacheDir()
		if err != nil {
			return nil, err
		}
		dir := filepath.Join(cache, "agent-can", "traces")
		if err := os.MkdirAll(dir, 0700); err != nil {
			return nil, err
		}
		return os.CreateTemp(dir, time.Now().Format("20060102-150405-")+"*.asc")
	}
	if !filepath.IsAbs(request.Path) {
		return nil, errors.New("trace path must be absolute")
	}
	if strings.ToLower(filepath.Ext(request.Path)) != ".asc" {
		return nil, errors.New("trace path must have an .asc suffix")
	}
	parent, err := filepath.EvalSymlinks(filepath.Dir(request.Path))
	if err != nil {
		return nil, fmt.Errorf("trace parent: %w", err)
	}
	return os.OpenFile(filepath.Join(parent, filepath.Base(request.Path)), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
}

func (m *Manager) TraceStart(ctx context.Context, request TraceRequest) (Result, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	e := m.engine
	if e == nil {
		return nil, errors.New("no active session; connect first")
	}
	if e.trace != nil && e.trace.status()["state"] == "active" {
		return nil, errors.New("trace already active; trace_stop first")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	file, err := traceFile(request)
	if err != nil {
		return nil, err
	}
	writer := &traceWriter{asc.NewWriter(file), file}
	// Recording must outlive cancellation of the send tasks and include the
	// bus's final records. Close stops it explicitly after closing the bus.
	r, err := recorder.Start(context.Background(), e.capture, writer, e.capture.End(), 20*time.Millisecond)
	if r != nil {
		e.trace = &traceState{path: file.Name(), recorder: r}
	}
	if err != nil {
		return nil, err
	}
	return e.trace.status(), nil
}

func (e *engine) stopTrace() (Result, error) {
	if e.trace == nil {
		return Result{"state": "stopped"}, nil
	}
	e.trace.recorder.Stop()
	return e.trace.status(), e.trace.recorder.Err()
}

func (m *Manager) TraceStop(context.Context, Empty) (Result, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.engine == nil {
		return nil, errors.New("no active session; connect first")
	}
	return m.engine.stopTrace()
}

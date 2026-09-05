package session

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"sync"
	"time"

	"github.com/tomrford/gocan"
	"github.com/tomrford/gocan/cyclic"
)

const retention = 60 * time.Second
const maxReadCount = 4096

// Manager serialises session operations. Drivers acquire traffic independently;
// neither MCP calls nor snapshot maintenance owns a receive loop.
type Manager struct {
	mu     sync.Mutex
	engine *engine
	open   func(context.Context, *gocan.Capture, ConnectRequest) (gocan.Bus, error)
}

type engine struct {
	request         ConnectRequest
	registry        *registry
	bus             gocan.Bus
	capture         *gocan.Capture
	ctx             context.Context
	cancel          context.CancelFunc
	done            chan struct{}
	schedules       map[string]*schedule
	trace           *traceState
	latest          map[gocan.FrameKey]observation
	cursor          gocan.Cursor
	marks           []checkpoint
	projectionError error
}

type checkpoint struct {
	at     time.Time
	cursor gocan.Cursor
}
type observation struct {
	event   gocan.FrameEvent
	count   uint64
	cycleMS *float64
}
type schedule struct {
	target string
	frame  gocan.Frame
	period int
	task   *cyclic.Task
}

func New() *Manager { return &Manager{open: openBus} }

func (m *Manager) Connect(ctx context.Context, request ConnectRequest) (Result, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.engine != nil {
		if !reflect.DeepEqual(request, m.engine.request) {
			return nil, errors.New("session already connected; disconnect first")
		}
		return Result{"created": false, "already_connected": true, "status": m.engine.status()}, nil
	}
	r, err := loadDBCs(request.DBCs)
	if err != nil {
		return nil, err
	}
	capture := gocan.NewCapture()
	bus, err := m.open(ctx, capture, request)
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		_ = bus.Close()
		return nil, err
	}
	lifetime, cancel := context.WithCancel(context.Background())
	e := &engine{request: request, registry: r, bus: bus, capture: capture, ctx: lifetime, cancel: cancel,
		done: make(chan struct{}), schedules: map[string]*schedule{}, latest: map[gocan.FrameKey]observation{}}
	m.engine = e
	go m.maintain(e)
	return Result{"created": true, "already_connected": false, "status": e.status()}, nil
}

func (m *Manager) maintain(e *engine) {
	defer close(e.done)
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-e.ctx.Done():
			return
		case now := <-ticker.C:
			m.mu.Lock()
			if m.engine == e {
				e.refresh(now)
				e.marks = append(e.marks, checkpoint{now, e.cursor})
				for len(e.marks) > 0 && now.Sub(e.marks[0].at) >= retention {
					mark := e.marks[0]
					e.marks = e.marks[1:]
					// Slow consumers get an explicit out-of-range error on their
					// next read, rather than retaining capture history indefinitely.
					if err := e.capture.Prune(mark.cursor); err != nil {
						e.projectionError = err
					}
				}
			}
			m.mu.Unlock()
		}
	}
}

func (m *Manager) Close() error {
	m.mu.Lock()
	e := m.engine
	if e == nil {
		m.mu.Unlock()
		return nil
	}
	e.cancel()
	for _, s := range e.schedules {
		s.task.Stop()
	}
	busErr := e.bus.Close()
	_, traceErr := e.stopTrace()
	m.engine = nil
	m.mu.Unlock()
	<-e.done
	return errors.Join(busErr, traceErr)
}

func (m *Manager) Disconnect(context.Context, Empty) (Result, error) {
	err := m.Close()
	return Result{"disconnected": true}, err
}

func (m *Manager) Status(context.Context, Empty) (Result, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.engine == nil {
		return Result{"connection_state": "disconnected", "trace_formats": []string{"asc"}}, nil
	}
	return m.engine.status(), nil
}

func (e *engine) status() Result {
	state, backendError := "connected", ""
	select {
	case <-e.bus.Done():
		state = "stopped"
		if err := e.bus.Err(); err != nil {
			state, backendError = "failed", err.Error()
		}
	default:
	}
	schedules := []Result{}
	for _, s := range e.schedules {
		row := frameResult(s.frame)
		row["target"], row["periodicity_ms"], row["state"] = s.target, s.period, "active"
		select {
		case <-s.task.Done():
			row["state"] = "stopped"
			if err := s.task.Err(); err != nil {
				row["state"], row["error"] = "failed", err.Error()
			}
		default:
		}
		schedules = append(schedules, row)
	}
	sort.Slice(schedules, func(i, j int) bool {
		return fmt.Sprint(schedules[i]["target"], schedules[i]["extended"]) < fmt.Sprint(schedules[j]["target"], schedules[j]["extended"])
	})
	result := Result{"connection_state": state, "channel": e.request.Channel,
		"bitrate": e.request.Bitrate, "fd_timing": e.request.FDTiming, "dbcs": e.registry.specs,
		"dbc_diagnostics": e.registry.diagnostics, "periodic_schedules": schedules, "backend_error": backendError,
		"retention_window_secs": int(retention.Seconds()), "retained_records": e.capture.Len(),
		"max_read_count": maxReadCount, "trace_formats": []string{"asc"}, "trace": e.trace.status()}
	if e.projectionError != nil {
		result["capture_error"] = e.projectionError.Error()
	}
	return result
}

func (e *engine) refresh(now time.Time) {
	cursor, err := e.capture.WriteRecordsSince(e.cursor, e)
	if err != nil {
		e.projectionError = err
		return
	}
	e.cursor = cursor
	for key, value := range e.latest {
		if now.Sub(value.event.Timestamp) > retention {
			delete(e.latest, key)
		}
	}
}

func (e *engine) WriteFrame(event gocan.FrameEvent) error {
	key := event.Key()
	old := e.latest[key]
	var cycle *float64
	if old.count > 0 {
		ms := event.Timestamp.Sub(old.event.Timestamp).Seconds() * 1000
		if ms >= 0 {
			cycle = &ms
		}
	}
	e.latest[key] = observation{event: event, count: old.count + 1, cycleMS: cycle}
	return nil
}

func (*engine) WriteEvent(gocan.Event) error { return nil }

func (m *Manager) Schema(_ context.Context, request SchemaRequest) (Result, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.engine == nil {
		return nil, errors.New("no active session; connect first")
	}
	return m.engine.registry.schema(request.Filter), nil
}

func (m *Manager) List(_ context.Context, request ListRequest) (Result, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	e := m.engine
	if e == nil {
		return nil, errors.New("no active session; connect first")
	}
	e.refresh(time.Now())
	rows := []Result{}
	for key, observed := range e.latest {
		if key.Direction != gocan.DirectionReceive {
			continue
		}
		names := []string{}
		matched := matches(request.Filter, key.ID)
		for _, def := range e.registry.messages {
			if !def.matches(observed.event.Frame) {
				continue
			}
			names = append(names, def.name)
			matched = matched || matches(request.Filter, key.ID, def.name, def.alias, def.message.Name)
		}
		if matched {
			row := frameResult(observed.event.Frame)
			delete(row, "payload_hex")
			row["names"], row["observed_count"], row["cycle_time_ms"] = names, observed.count, observed.cycleMS
			rows = append(rows, row)
		}
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i]["arb_id"] != rows[j]["arb_id"] {
			return rows[i]["arb_id"].(uint32) < rows[j]["arb_id"].(uint32)
		}
		return !rows[i]["extended"].(bool) && rows[j]["extended"].(bool)
	})
	return Result{"messages": rows}, nil
}

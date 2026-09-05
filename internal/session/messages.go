package session

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/tomrford/gocan"
	"github.com/tomrford/gocan/cyclic"
	"github.com/tomrford/gocan/dbc"
)

func frameResult(frame gocan.Frame) Result {
	return Result{"arb_id": frame.ID, "extended": frame.Flags.Has(gocan.FrameExtended),
		"fd": frame.Flags.Has(gocan.FrameFD), "bitrate_switch": frame.Flags.Has(gocan.FrameBitRateSwitch),
		"remote": frame.Flags.Has(gocan.FrameRemote), "error_state_indicator": frame.Flags.Has(gocan.FrameErrorStateIndicator),
		"dlc": frame.DLC, "len": frame.DataLength(), "payload_hex": strings.ToUpper(hex.EncodeToString(frame.Data[:frame.DataLength()]))}
}

func (m *Manager) Read(_ context.Context, request ReadRequest) (Result, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	e := m.engine
	if e == nil {
		return nil, errors.New("no active session; connect first")
	}
	count := 1
	if request.Count != nil {
		count = *request.Count
	}
	if count < 1 || count > maxReadCount {
		return nil, fmt.Errorf("count must be between 1 and %d", maxReadCount)
	}
	direction := gocan.DirectionReceive
	if request.Direction == "tx" {
		direction = gocan.DirectionTransmit
	} else if request.Direction != "" && request.Direction != "rx" {
		return nil, errors.New("direction must be rx or tx")
	}
	id, raw, err := rawID(request.Select)
	if err != nil {
		return nil, err
	}
	var def messageDef
	extended := request.Extended
	if !raw {
		if request.Extended {
			return nil, errors.New("extended is only valid for raw targets")
		}
		def, err = e.registry.resolve(request.Select)
		if err != nil {
			return nil, err
		}
		id, extended = def.message.ID, def.message.Extended
	}
	frames := []gocan.FrameEvent{}
	key := gocan.FrameKey{ID: id, Bus: e.bus.ID(), Direction: direction, Extended: extended}
	if count == 1 {
		if event, ok := e.capture.Latest(key); ok {
			frames = append(frames, event)
		}
	} else {
		frames = e.capture.Series(key)
	}
	observations := []Result{}
	cutoff := time.Now().Add(-retention)
	// Capture order remains meaningful when acquisition timestamps tie or
	// regress. Series returns append order, so walk it backwards.
	for index := len(frames) - 1; index >= 0; index-- {
		event := frames[index]
		if event.Timestamp.Before(cutoff) {
			continue
		}
		row := frameResult(event.Frame)
		row["unix_ms"], row["direction"] = event.Timestamp.UnixMilli(), "rx"
		if direction == gocan.DirectionTransmit {
			row["direction"] = "tx"
		}
		if !raw {
			row["qualified_name"] = def.name
			values, problems := decode(def, event.Frame)
			row["signals"] = values
			if len(problems) > 0 {
				row["signal_errors"] = problems
			}
		}
		observations = append(observations, row)
		if len(observations) == count {
			break
		}
	}
	if len(observations) == 0 {
		return nil, fmt.Errorf("selector %q matched no retained %s traffic", request.Select, map[gocan.Direction]string{gocan.DirectionReceive: "rx", gocan.DirectionTransmit: "tx"}[direction])
	}
	return Result{"selector": request.Select, "count": len(observations), "observations": observations}, nil
}

func scheduleKey(target string, extended bool) (string, error) {
	id, raw, err := rawID(target)
	if err != nil {
		return "", err
	}
	if raw {
		return fmt.Sprintf("raw:%X:%t", id, extended), nil
	}
	if extended {
		return "", errors.New("extended is only valid for raw targets")
	}
	return strings.TrimSpace(target), nil
}

func (m *Manager) SendFrame(ctx context.Context, request FrameSendRequest) (Result, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.engine == nil {
		return nil, errors.New("no active session; connect first")
	}
	id, raw, err := rawID(request.Target)
	if err != nil {
		return nil, err
	}
	if !raw {
		return nil, errors.New("target must be a raw hex arbitration ID; use message_send for DBC messages")
	}
	payload, err := hex.DecodeString(strings.Join(strings.Fields(request.Data), ""))
	if err != nil {
		return nil, fmt.Errorf("invalid raw hex payload: %w", err)
	}
	var flags gocan.FrameFlags
	if request.Extended {
		flags |= gocan.FrameExtended
	}
	if request.FD {
		flags |= gocan.FrameFD
	}
	if request.BitRateSwitch {
		flags |= gocan.FrameBitRateSwitch
	}
	frame, err := gocan.NewFrame(id, payload, flags)
	if err != nil {
		return nil, err
	}
	return m.engine.send(ctx, request.Target, request.Extended, frame, request.PeriodicityMS)
}

func (m *Manager) Send(ctx context.Context, request SendRequest) (Result, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	e := m.engine
	if e == nil {
		return nil, errors.New("no active session; connect first")
	}
	def, err := e.registry.resolve(request.Target)
	if err != nil {
		return nil, err
	}
	values := make(dbc.Values, len(request.Signals))
	for name, value := range request.Signals {
		if number, ok := value.(json.Number); ok {
			value, err = signalNumber(number)
			if err != nil {
				return nil, fmt.Errorf("signal %q: %w", name, err)
			}
		}
		values[name] = value
	}
	frame, err := def.message.Encode(values)
	if err != nil {
		return nil, err
	}
	return e.send(ctx, def.name, false, frame, request.PeriodicityMS)
}

func (e *engine) send(ctx context.Context, target string, extended bool, frame gocan.Frame, period *int) (Result, error) {
	if period != nil && (*period < 1 || *period > 86400000) {
		return nil, errors.New("periodicity_ms must be between 1 and 86400000")
	}
	key, err := scheduleKey(target, extended)
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if period == nil {
		sendCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		if err := e.bus.Send(sendCtx, frame); err != nil {
			return nil, err
		}
	} else {
		// The schedule belongs to the session, not the initiating MCP request.
		task, err := cyclic.Start(e.ctx, e.bus, frame, time.Duration(*period)*time.Millisecond)
		if err != nil {
			return nil, err
		}
		if existing := e.schedules[key]; existing != nil {
			existing.task.Stop()
		}
		e.schedules[key] = &schedule{target: strings.TrimSpace(target), frame: frame, period: *period, task: task}
	}
	result := frameResult(frame)
	result["target"], result["periodicity_ms"] = strings.TrimSpace(target), period
	return result, nil
}

func signalNumber(number json.Number) (any, error) {
	if !strings.ContainsAny(number.String(), ".eE") {
		if value, err := number.Int64(); err == nil {
			return value, nil
		}
		if value, err := strconv.ParseUint(number.String(), 10, 64); err == nil {
			return value, nil
		}
		return nil, errors.New("integer is outside the 64-bit range")
	}
	value, err := number.Float64()
	if err != nil || math.IsNaN(value) || math.IsInf(value, 0) {
		return nil, errors.New("value must be finite")
	}
	if math.Abs(value) >= 1<<53 {
		return nil, errors.New("large values must use integer notation to preserve precision")
	}
	return value, nil
}

func (m *Manager) Stop(_ context.Context, request StopRequest) (Result, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.engine == nil {
		return nil, errors.New("no active session; connect first")
	}
	key, err := scheduleKey(request.Target, request.Extended)
	if err != nil {
		return nil, err
	}
	s, ok := m.engine.schedules[key]
	if ok {
		s.task.Stop()
		delete(m.engine.schedules, key)
	}
	return Result{"target": request.Target, "stopped": ok}, nil
}

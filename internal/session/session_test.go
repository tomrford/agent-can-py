package session

import (
	"context"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/tomrford/gocan"
)

func connected(t *testing.T, withDBC bool) *Manager {
	t.Helper()
	m := New()
	t.Cleanup(func() {
		if err := m.Close(); err != nil {
			t.Error(err)
		}
	})
	r := ConnectRequest{Channel: "virtual:agent-can"}
	if withDBC {
		p, err := filepath.Abs("../../examples/demo.dbc")
		if err != nil {
			t.Fatal(err)
		}
		r.DBCs = []DBCSpec{{Alias: "demo", Path: p}}
	}
	if _, err := m.Connect(context.Background(), r); err != nil {
		t.Fatal(err)
	}
	return m
}

func eventually(t *testing.T, check func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if check() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("condition did not become true")
}

func TestSemanticSendAndReadAgainstKnownPayload(t *testing.T) {
	m := connected(t, true)
	ctx := context.Background()
	result, err := m.Send(ctx, SendRequest{Target: "demo.PowertrainStatus", Signals: map[string]any{
		"vehicle_speed": 12.3, "engine_rpm": 1500, "throttle": 20, "coolant_temp": 90,
	}})
	if err != nil {
		t.Fatal(err)
	}
	if result["payload_hex"] != "7B00DC0528820000" {
		t.Fatalf("unexpected encoded frame: %v", result)
	}
	eventually(t, func() bool { _, err := m.Read(ctx, ReadRequest{Select: "demo.PowertrainStatus"}); return err == nil })
	read, err := m.Read(ctx, ReadRequest{Select: "demo.PowertrainStatus"})
	if err != nil {
		t.Fatal(err)
	}
	row := read["observations"].([]Result)[0]
	signals := row["signals"].(Result)
	if signals["vehicle_speed"].(Result)["unit"] != "km/h" || signals["engine_rpm"].(Result)["value"] != uint64(1500) {
		t.Fatalf("incorrect decoded values: %v", signals)
	}
	for _, filter := range []string{"PowerTrain", "demo", "*.Power*", "0x120"} {
		list, err := m.List(ctx, ListRequest{Filter: filter})
		if err != nil {
			t.Fatal(err)
		}
		rows := list["messages"].([]Result)
		if len(rows) != 1 || len(rows[0]["names"].([]string)) != 1 || rows[0]["names"].([]string)[0] != "demo.PowertrainStatus" {
			t.Fatalf("filter %q: %v", filter, rows)
		}
	}
	if _, err := m.Read(ctx, ReadRequest{Select: "PowerTrain"}); err == nil {
		t.Fatal("partial read target accepted")
	}
}

func TestRejectInvalidSendsBeforeTransmission(t *testing.T) {
	m := connected(t, true)
	ctx := context.Background()
	zero := 0
	requests := []FrameSendRequest{
		{Target: "0x800", Data: "00"},
		{Target: "0x20000000", Data: "00", Extended: true},
		{Target: "0x123", Data: strings.Repeat("AA", 9)},
		{Target: "0x123", Data: strings.Repeat("AA", 9), FD: true},
		{Target: "0x123", Data: "F"},
		{Target: "0x123", Data: "00", BitRateSwitch: true},
		{Target: "0x123", Data: "00", PeriodicityMS: &zero},
		{Target: "demo.Heartbeat", Data: "00"},
	}
	for _, request := range requests {
		if _, err := m.SendFrame(ctx, request); err == nil {
			t.Errorf("accepted invalid send: %+v", request)
		}
	}
	for _, request := range []SendRequest{
		{Target: "0x123", Signals: map[string]any{}},
		{Target: "demo.PowertrainStatus", Signals: map[string]any{"engine_rpm": 1500}},
		{Target: "demo.Heartbeat", Signals: map[string]any{"counter": 1, "mode": 1, "surprise": 0}},
		{Target: "demo.Heartbeat", Signals: map[string]any{"counter": 1, "mode": 1}, PeriodicityMS: &zero},
	} {
		if _, err := m.Send(ctx, request); err == nil {
			t.Errorf("accepted invalid semantic send: %+v", request)
		}
	}
	if m.engine.capture.Len() != 0 {
		t.Fatal("invalid input transmitted traffic")
	}
	for _, count := range []int{0, -1, 4097} {
		if _, err := m.Read(ctx, ReadRequest{Select: "0x123", Count: &count}); err == nil {
			t.Fatal("invalid count accepted")
		}
	}
}

func TestInventoryKeepsRawIdentitiesWithAllDBCNames(t *testing.T) {
	m := connected(t, true)
	ctx := context.Background()
	path := m.engine.registry.specs[0].Path
	if err := m.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Connect(ctx, ConnectRequest{Channel: "virtual:agent-can", DBCs: []DBCSpec{
		{Alias: "demo", Path: path}, {Alias: "other", Path: path},
	}}); err != nil {
		t.Fatal(err)
	}
	for _, event := range []gocan.FrameEvent{
		{Frame: gocan.Frame{ID: 0x120}, Direction: gocan.DirectionReceive},
		{Frame: gocan.Frame{ID: 0x120, Flags: gocan.FrameExtended}, Direction: gocan.DirectionReceive},
		{Frame: gocan.Frame{ID: 0x123}, Direction: gocan.DirectionReceive},
		{Frame: gocan.Frame{ID: 0x456}, Direction: gocan.DirectionTransmit},
	} {
		event.Bus, event.Timestamp = 1, time.Now()
		if err := m.engine.capture.Append(event); err != nil {
			t.Fatal(err)
		}
	}
	list, err := m.List(ctx, ListRequest{})
	if err != nil {
		t.Fatal(err)
	}
	rows := list["messages"].([]Result)
	if len(rows) != 3 || rows[0]["arb_id"] != uint32(0x120) || rows[0]["extended"] != false ||
		rows[1]["arb_id"] != uint32(0x120) || rows[1]["extended"] != true || rows[2]["arb_id"] != uint32(0x123) {
		t.Fatalf("raw identities lost, duplicated or mixed with TX: %v", rows)
	}
	wantNames := []string{"demo.PowertrainStatus", "other.PowertrainStatus"}
	if !slices.Equal(rows[0]["names"].([]string), wantNames) || len(rows[1]["names"].([]string)) != 0 || len(rows[2]["names"].([]string)) != 0 {
		t.Fatalf("incorrect DBC overlays: %v", rows)
	}
	for filter, count := range map[string]int{"other.Power*": 1, "0x120": 2, "0x123": 1, "Heartbeat": 0} {
		list, err := m.List(ctx, ListRequest{Filter: filter})
		if err != nil || len(list["messages"].([]Result)) != count {
			t.Fatalf("filter %q: %v %v", filter, list, err)
		}
		if filter == "other.Power*" && !slices.Equal(list["messages"].([]Result)[0]["names"].([]string), wantNames) {
			t.Fatalf("filter discarded matching identity's other names: %v", list)
		}
	}
}

func TestFDIdentityAndRXTXSeparation(t *testing.T) {
	m := connected(t, false)
	ctx := context.Background()
	if _, err := m.SendFrame(ctx, FrameSendRequest{Target: "0x123", Data: "AA"}); err != nil {
		t.Fatal(err)
	}
	result, err := m.SendFrame(ctx, FrameSendRequest{Target: "0x123", Data: strings.Repeat("BB", 12), Extended: true, FD: true, BitRateSwitch: true})
	if err != nil {
		t.Fatal(err)
	}
	if result["len"] != 12 || result["dlc"] != uint8(9) {
		t.Fatalf("DLC confused with length: %v", result)
	}
	count := 2
	for _, extended := range []bool{false, true} {
		read, err := m.Read(ctx, ReadRequest{Select: "0x123", Extended: extended, Count: &count, Direction: "tx"})
		if err != nil {
			t.Fatal(err)
		}
		rows := read["observations"].([]Result)
		if len(rows) != 1 || rows[0]["extended"] != extended || rows[0]["direction"] != "tx" {
			t.Fatalf("identity/direction mismatch: %v", rows)
		}
	}
}

func TestReadUsesCaptureOrderWhenTimestampsTieOrRegress(t *testing.T) {
	m := connected(t, false)
	base := time.Now().Add(-time.Second)
	for index, stamp := range []time.Time{base, base, base.Add(-time.Millisecond)} {
		frame, err := gocan.NewFrame(0x123, []byte{byte(index + 1)}, 0)
		if err != nil {
			t.Fatal(err)
		}
		if err := m.engine.capture.Append(gocan.FrameEvent{Bus: 1, Timestamp: stamp, Direction: gocan.DirectionReceive, Frame: frame}); err != nil {
			t.Fatal(err)
		}
	}
	for _, count := range []int{1, 2, 3} {
		read, err := m.Read(context.Background(), ReadRequest{Select: "0x123", Count: &count})
		if err != nil {
			t.Fatal(err)
		}
		rows := read["observations"].([]Result)
		want := []string{"03", "02", "01"}
		if len(rows) != count {
			t.Fatalf("count %d: %v", count, rows)
		}
		for i, row := range rows {
			if row["payload_hex"] != want[i] {
				t.Fatalf("count %d: %v", count, rows)
			}
		}
	}
}

func TestSessionOwnedSchedulesAndCanonicalStop(t *testing.T) {
	m := connected(t, false)
	ctx, cancel := context.WithCancel(context.Background())
	period := 5
	if _, err := m.SendFrame(ctx, FrameSendRequest{Target: "0x0123", Data: "01", PeriodicityMS: &period}); err != nil {
		t.Fatal(err)
	}
	cancel()
	key := gocan.FrameKey{ID: 0x123, Bus: 1, Direction: gocan.DirectionTransmit}
	eventually(t, func() bool { return len(m.engine.capture.Series(key)) >= 3 })
	if _, err := m.SendFrame(context.Background(), FrameSendRequest{Target: "0X123", Data: "02", PeriodicityMS: &period}); err != nil {
		t.Fatal(err)
	}
	status, _ := m.Status(context.Background(), Empty{})
	if len(status["periodic_schedules"].([]Result)) != 1 {
		t.Fatalf("duplicate canonical schedule: %v", status)
	}
	stop, err := m.Stop(context.Background(), StopRequest{Target: "0x000123"})
	if err != nil || stop["stopped"] != true {
		t.Fatalf("stop: %v %v", stop, err)
	}
	before := len(m.engine.capture.Series(key))
	time.Sleep(20 * time.Millisecond)
	if after := len(m.engine.capture.Series(key)); after != before {
		t.Fatal("transmissions continued after stop returned")
	}
}

func TestFailedScheduleRemainsObservable(t *testing.T) {
	m := connected(t, false)
	period := 5
	if _, err := m.SendFrame(context.Background(), FrameSendRequest{Target: "0x123", Data: "01", PeriodicityMS: &period}); err != nil {
		t.Fatal(err)
	}
	if err := m.engine.bus.Close(); err != nil {
		t.Fatal(err)
	}
	eventually(t, func() bool {
		status, _ := m.Status(context.Background(), Empty{})
		return status["periodic_schedules"].([]Result)[0]["state"] == "failed"
	})
}

// Model a connection which accepts classical CAN but rejects valid FD frames.
type classicalBus struct{ gocan.Bus }

func (b classicalBus) Send(ctx context.Context, frame gocan.Frame) error {
	if frame.Flags.Has(gocan.FrameFD) {
		return errors.New("connection is classical CAN")
	}
	return b.Bus.Send(ctx, frame)
}

func TestFailedReplacementPreservesWorkingSchedule(t *testing.T) {
	m := New()
	m.open = func(ctx context.Context, capture *gocan.Capture, request ConnectRequest) (gocan.Bus, error) {
		bus, err := openBus(ctx, capture, request)
		return classicalBus{bus}, err
	}
	t.Cleanup(func() {
		if err := m.Close(); err != nil {
			t.Error(err)
		}
	})
	ctx := context.Background()
	if _, err := m.Connect(ctx, ConnectRequest{Channel: "virtual:agent-can"}); err != nil {
		t.Fatal(err)
	}
	period := 5
	if _, err := m.SendFrame(ctx, FrameSendRequest{Target: "0x123", Data: "AA", PeriodicityMS: &period}); err != nil {
		t.Fatal(err)
	}
	if _, err := m.SendFrame(ctx, FrameSendRequest{Target: "0x123", Data: strings.Repeat("BB", 12), FD: true, PeriodicityMS: &period}); err == nil {
		t.Fatal("FD send accepted")
	}
	key := gocan.FrameKey{ID: 0x123, Bus: 1, Direction: gocan.DirectionTransmit}
	before := len(m.engine.capture.Series(key))
	eventually(t, func() bool { return len(m.engine.capture.Series(key)) > before })
	status, _ := m.Status(ctx, Empty{})
	rows := status["periodic_schedules"].([]Result)
	if len(rows) != 1 || rows[0]["payload_hex"] != "AA" || rows[0]["state"] != "active" {
		t.Fatalf("working schedule lost: %v", rows)
	}
}

func TestTraceShutdownAndNoOverwrite(t *testing.T) {
	m := connected(t, false)
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "capture.asc")
	if _, err := m.TraceStart(ctx, TraceRequest{Path: path}); err != nil {
		t.Fatal(err)
	}
	if _, err := m.SendFrame(ctx, FrameSendRequest{Target: "0x321", Data: "DEADBEEF"}); err != nil {
		t.Fatal(err)
	}
	if _, err := m.TraceStart(ctx, TraceRequest{Path: filepath.Join(t.TempDir(), "other.asc")}); err == nil {
		t.Fatal("active trace replaced")
	}
	if err := m.Close(); err != nil {
		t.Fatalf("normal trace shutdown: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "321 Tx d 4 DE AD BE EF") || !strings.HasSuffix(string(data), "End TriggerBlock\n") {
		t.Fatalf("trace lost tail records or footer:\n%s", data)
	}
	if _, err := traceFile(TraceRequest{Path: path}); err == nil {
		t.Fatal("existing trace overwritten")
	}
	again, _ := os.ReadFile(path)
	if string(again) != string(data) {
		t.Fatal("existing file changed")
	}
	if _, err := traceFile(TraceRequest{Path: filepath.Join(t.TempDir(), "missing", "trace.asc")}); err == nil {
		t.Fatal("missing parent accepted")
	}
	if _, err := traceFile(TraceRequest{Path: filepath.Join(t.TempDir(), "trace.blf")}); err == nil {
		t.Fatal("unsupported suffix accepted")
	}
}

func TestDBCValidationBeforeBusOpen(t *testing.T) {
	m := New()
	opened := false
	m.open = func(context.Context, *gocan.Capture, ConnectRequest) (gocan.Bus, error) {
		opened = true
		t.Fatal("opened hardware before validating DBC")
		return nil, nil
	}
	for _, specs := range [][]DBCSpec{
		{{Alias: "x", Path: "relative.dbc"}},
		{{Alias: "bad.alias", Path: filepath.Join(t.TempDir(), "missing.dbc")}},
		{{Alias: "x", Path: filepath.Join(t.TempDir(), "missing.dbc")}},
	} {
		if _, err := m.Connect(context.Background(), ConnectRequest{DBCs: specs}); err == nil {
			t.Fatal("bad DBC accepted")
		}
	}
	if opened || m.engine != nil {
		t.Fatal("failed connect mutated state")
	}
}

func TestChoicesMultiplexingAndDecodeErrors(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mux.dbc")
	const source = `VERSION ""
NS_ :
BS_:
BU_: ECU
BO_ 512 State: 2 ECU
 SG_ mode M : 0|8@1+ (1,0) [0|1] "" ECU
 SG_ speed m0 : 8|8@1+ (1,0) [0|255] "km/h" ECU
 SG_ temp m1 : 8|8@1+ (1,-40) [-40|215] "degC" ECU
VAL_ 512 mode 0 "Driving" 1 "Heating";
`
	if err := os.WriteFile(path, []byte(source), 0600); err != nil {
		t.Fatal(err)
	}
	r, err := loadDBCs([]DBCSpec{{Alias: "test", Path: path}})
	if err != nil {
		t.Fatal(err)
	}
	def, _ := r.resolve("test.State")
	frame, _ := gocan.NewFrame(512, []byte{0, 42}, 0)
	values, problems := decode(def, frame)
	if values["mode"].(Result)["value"] != "Driving" || values["speed"].(Result)["value"] != uint64(42) {
		t.Fatalf("decode: %v", values)
	}
	if _, ok := values["temp"]; ok || problems["temp"] == nil {
		t.Fatalf("inactive signal invented: %v %v", values, problems)
	}
	frame, _ = gocan.NewFrame(512, []byte{0}, 0)
	values, problems = decode(def, frame)
	if len(values) != 0 || len(problems) != 3 {
		t.Fatalf("short frame silently decoded: %v %v", values, problems)
	}
}

func TestRetentionAndCycleTimeUseObservedTimestamps(t *testing.T) {
	m := connected(t, false)
	ctx := context.Background()
	base := time.Now().Add(-time.Second)
	payload, _ := hex.DecodeString("AB")
	frame, _ := gocan.NewFrame(0x123, payload, 0)
	for _, stamp := range []time.Time{base, base.Add(10 * time.Millisecond)} {
		if err := m.engine.capture.Append(gocan.FrameEvent{Bus: 1, Timestamp: stamp, Direction: gocan.DirectionReceive, Frame: frame}); err != nil {
			t.Fatal(err)
		}
	}
	list, _ := m.List(ctx, ListRequest{})
	row := list["messages"].([]Result)[0]
	if *row["cycle_time_ms"].(*float64) != 10 {
		t.Fatalf("cycle estimate: %v", row)
	}
	m.mu.Lock()
	m.engine.refresh(base.Add(61 * time.Second))
	if len(m.engine.latest) != 0 {
		t.Error("stale inventory retained")
	}
	m.mu.Unlock()
	oldFrame, _ := gocan.NewFrame(0x456, []byte{0}, 0)
	if err := m.engine.capture.Append(gocan.FrameEvent{Bus: 1, Timestamp: time.Now().Add(-61 * time.Second), Direction: gocan.DirectionReceive, Frame: oldFrame}); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Read(ctx, ReadRequest{Select: "0x456"}); err == nil {
		t.Fatal("stale capture exposed in read")
	}
}

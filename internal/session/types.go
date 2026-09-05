package session

import "github.com/tomrford/gocan/drivers"

type Empty struct{}

type DBCSpec struct {
	Alias string `json:"alias" jsonschema:"Unique semantic namespace, without dots or wildcard characters"`
	Path  string `json:"path" jsonschema:"Absolute path to an existing DBC file"`
}

type ConnectRequest struct {
	Channel  string    `json:"channel" jsonschema:"Exact channel identifier returned by buses_list"`
	Bitrate  uint32    `json:"bitrate,omitempty" jsonschema:"Classical CAN bitrate for programmable hardware; omit for SocketCAN and virtual"`
	FDTiming *FDTiming `json:"fd_timing,omitempty" jsonschema:"Exact CAN FD timing for programmable hardware; omit for SocketCAN and virtual"`
	DBCs     []DBCSpec `json:"dbcs,omitempty" jsonschema:"Immutable connect-time DBC overlays; disconnect to change them"`
}

type BitTiming struct {
	BRP   uint32 `json:"brp"`
	TSEG1 uint32 `json:"tseg1"`
	TSEG2 uint32 `json:"tseg2"`
	SJW   uint32 `json:"sjw"`
}

type FDTiming struct {
	ClockHz uint32    `json:"clock_hz"`
	Nominal BitTiming `json:"nominal"`
	Data    BitTiming `json:"data"`
}

func (t FDTiming) native() drivers.FDTiming {
	return drivers.FDTiming{ClockHz: t.ClockHz,
		Nominal: drivers.BitTiming(t.Nominal), Data: drivers.BitTiming(t.Data)}
}

type SchemaRequest struct {
	Filter string `json:"filter,omitempty"`
}
type ListRequest struct {
	Filter string `json:"filter,omitempty"`
}
type ReadRequest struct {
	Select    string `json:"select" jsonschema:"Raw hex arbitration ID or exact alias.Message name"`
	Extended  bool   `json:"extended,omitempty" jsonschema:"Select 29-bit raw frames; semantic names determine their own format"`
	Count     *int   `json:"count,omitempty" jsonschema:"Newest observations to return, from 1 to 4096; default 1"`
	Direction string `json:"direction,omitempty" jsonschema:"rx (default) or tx; accepted sends are separate from received traffic"`
}
type FrameSendRequest struct {
	Target        string `json:"target" jsonschema:"Raw hex arbitration ID"`
	Data          string `json:"data" jsonschema:"Hex payload"`
	Extended      bool   `json:"extended,omitempty"`
	FD            bool   `json:"fd,omitempty"`
	BitRateSwitch bool   `json:"bitrate_switch,omitempty"`
	PeriodicityMS *int   `json:"periodicity_ms,omitempty" jsonschema:"Omit for one send; otherwise 1 through 86400000 milliseconds"`
}
type SendRequest struct {
	Target        string         `json:"target" jsonschema:"Exact alias.Message name"`
	Signals       map[string]any `json:"signals" jsonschema:"Every active signal, as numeric values or choice labels"`
	PeriodicityMS *int           `json:"periodicity_ms,omitempty" jsonschema:"Omit for one send; otherwise 1 through 86400000 milliseconds"`
}
type StopRequest struct {
	Target   string `json:"target"`
	Extended bool   `json:"extended,omitempty" jsonschema:"Select the extended raw schedule; semantic targets determine their own format"`
}
type TraceRequest struct {
	Path   string `json:"path,omitempty" jsonschema:"Absolute .asc path in an existing directory; existing files are never overwritten"`
	Format string `json:"format,omitempty" jsonschema:"asc (default); omit path to create a trace in the user cache directory"`
}

type Result = map[string]any

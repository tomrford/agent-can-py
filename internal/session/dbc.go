package session

import (
	"fmt"
	"math"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/tomrford/gocan"
	"github.com/tomrford/gocan/dbc"
)

type messageDef struct {
	name    string
	alias   string
	message *dbc.Message
}

type registry struct {
	specs       []DBCSpec
	messages    []messageDef
	byName      map[string]messageDef
	diagnostics map[string][]dbc.Diagnostic
}

func loadDBCs(specs []DBCSpec) (*registry, error) {
	r := &registry{specs: []DBCSpec{}, byName: map[string]messageDef{}, diagnostics: map[string][]dbc.Diagnostic{}}
	aliases := map[string]bool{}
	for _, spec := range specs {
		if spec.Alias == "" || strings.ContainsAny(spec.Alias, ".*?[]/\\ \t\r\n") || strings.HasPrefix(strings.ToLower(spec.Alias), "0x") {
			return nil, fmt.Errorf("invalid DBC alias %q", spec.Alias)
		}
		if aliases[spec.Alias] {
			return nil, fmt.Errorf("duplicate DBC alias %q", spec.Alias)
		}
		aliases[spec.Alias] = true
		if !filepath.IsAbs(spec.Path) {
			return nil, fmt.Errorf("DBC path %q must be absolute", spec.Path)
		}
		resolved, err := filepath.EvalSymlinks(spec.Path)
		if err != nil {
			return nil, fmt.Errorf("DBC %q: %w", spec.Alias, err)
		}
		info, err := os.Stat(resolved)
		if err != nil {
			return nil, err
		}
		if !info.Mode().IsRegular() || info.Size() > 32<<20 {
			return nil, fmt.Errorf("DBC %q must be a regular file of at most 32 MiB", spec.Alias)
		}
		db, err := dbc.ParseFile(resolved)
		if err != nil {
			return nil, err
		}
		spec.Path = resolved
		r.specs = append(r.specs, spec)
		if len(db.Diagnostics) > 0 {
			r.diagnostics[spec.Alias] = db.Diagnostics
		}
		for i := range db.Messages {
			m := messageDef{spec.Alias + "." + db.Messages[i].Name, spec.Alias, &db.Messages[i]}
			if _, exists := r.byName[m.name]; exists {
				return nil, fmt.Errorf("duplicate DBC message %q", m.name)
			}
			r.messages = append(r.messages, m)
			r.byName[m.name] = m
		}
	}
	sort.Slice(r.messages, func(i, j int) bool { return r.messages[i].name < r.messages[j].name })
	return r, nil
}

func rawID(target string) (uint32, bool, error) {
	target = strings.TrimSpace(target)
	if target == "" {
		return 0, false, fmt.Errorf("selector must not be empty")
	}
	if !strings.HasPrefix(strings.ToLower(target), "0x") {
		return 0, false, nil
	}
	id, err := strconv.ParseUint(target[2:], 16, 29)
	if err != nil {
		return 0, true, fmt.Errorf("invalid CAN arbitration ID %q", target)
	}
	return uint32(id), true, nil
}

func (r *registry) resolve(target string) (messageDef, error) {
	m, ok := r.byName[strings.TrimSpace(target)]
	if !ok {
		return m, fmt.Errorf("unknown DBC message %q; use schema to find an exact alias.Message name", target)
	}
	return m, nil
}

func matches(filter string, id uint32, names ...string) bool {
	if filter == "" {
		return true
	}
	if selected, raw, err := rawID(filter); raw {
		return err == nil && selected == id
	}
	filter = strings.ToLower(strings.TrimSpace(filter))
	for _, name := range names {
		name = strings.ToLower(name)
		glob, _ := path.Match(filter, name)
		if glob || strings.Contains(name, filter) {
			return true
		}
	}
	return false
}

func (m messageDef) matches(f gocan.Frame) bool {
	return m.message.ID == f.ID && m.message.Extended == f.Flags.Has(gocan.FrameExtended)
}

func (r *registry) schema(filter string) Result {
	messages := []Result{}
	for _, m := range r.messages {
		if !matches(filter, m.message.ID, m.name, m.alias, m.message.Name) {
			continue
		}
		signals := []Result{}
		for _, s := range m.message.Signals {
			signals = append(signals, Result{"name": s.Name, "unit": s.Unit, "min": s.Minimum, "max": s.Maximum,
				"factor": s.Factor, "offset": s.Offset, "start_bit": s.StartBit, "bit_len": s.BitLength,
				"value_descriptions": s.Values, "multiplex": s.Multiplex})
		}
		messages = append(messages, Result{"qualified_name": m.name, "alias": m.alias, "message": m.message.Name,
			"arb_id": m.message.ID, "extended": m.message.Extended, "len": m.message.Length,
			"fd":      m.message.Length > 8 || m.message.Format == dbc.FrameFormatStandardCANFD || m.message.Format == dbc.FrameFormatExtendedCANFD,
			"signals": signals})
	}
	return Result{"messages": messages, "diagnostics": r.diagnostics}
}

func decode(m messageDef, frame gocan.Frame) (Result, Result) {
	values, problems := Result{}, Result{}
	for _, signal := range m.message.Signals {
		value, err := m.message.Decode(frame, signal.Name)
		if err != nil {
			problems[signal.Name] = err.Error()
			continue
		}
		if number, ok := value.(float64); ok && (math.IsNaN(number) || math.IsInf(number, 0)) {
			problems[signal.Name] = "signal contains a non-finite floating-point value"
			continue
		}
		values[signal.Name] = Result{"value": value, "unit": signal.Unit}
	}
	return values, problems
}

// Package sunspec reads SunSpec devices over Modbus: solar inverters,
// meters, batteries and other distributed energy resources that publish
// their data as a chain of SunSpec information models in holding
// registers.
//
// What it follows (SunSpec Device Information Model Specification v1.2,
// section 6, and the public model definitions at
// https://github.com/sunspec/models; our own words):
//
//   - The map starts with the two registers 0x5375 0x6E53 ("SunS") at
//     protocol address 40000, 50000 or 0 (zero-based; datasheets counting
//     from one write 40001). The base addresses are tried in that order.
//   - Models follow the marker back to back: an ID register, a length
//     register L counting the registers after it, then L registers of
//     data. The chain ends with ID 0xFFFF (L 0), or where the device
//     refuses the next read.
//   - Each model's points sit at fixed offsets (models.json, derived from
//     the published definitions). Values are big-endian, the most
//     significant register first. A value of type sunssf is a power of ten
//     that scales the points naming it. Each type has a "not implemented"
//     value (0x8000 for int16, 0xFFFF for uint16 and enums, 0 for
//     accumulators, all zero for strings …); such a point is left out.
//   - Groups follow the fixed part, once or repeating (model 160's
//     modules, model 126's curves), their count given by the definition,
//     by a point or by L; groups nest inside groups to any depth (model
//     705's curves each carry their points), each occurrence holding its
//     own points and then its nested groups (Layout).
//
// The tables hold every model of the SunSpec Alliance's definitions at a
// pinned commit (112 models). Unknown models are kept as their ID and
// length (a vendor model, a newer SunSpec model), never refused.
package sunspec

import (
	_ "embed"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/netip"
	"slices"
	"sort"
	"strings"
	"sync"
)

// Marker is the two registers that open a SunSpec map ("SunS").
var Marker = [2]uint16{0x5375, 0x6E53}

// Bases are the protocol addresses a map may start at, in the order they
// are tried.
var Bases = []uint16{40000, 50000, 0}

// EndID marks the end of the model chain.
const EndID = 0xFFFF

//go:embed models.json
var modelsJSON []byte

// Point is one point of a model, as described in models.json.
type Point struct {
	// Name is the point's name in the model definition ("W", "PhVphA").
	Name string `json:"n"`
	// Offset counts registers from the model's first data register (or
	// from the start of its group, for a group point).
	Offset int `json:"o"`
	// Type is the SunSpec type ("int16", "uint32", "sunssf", "enum16",
	// "bitfield32", "string", "acc32", "float32", "pad" ...).
	Type string `json:"t"`
	// Size is the number of registers.
	Size int `json:"s"`
	// SF is the scale factor: the name of a sunssf point (string), or a
	// fixed power of ten (float64); nil when the point is not scaled.
	SF any `json:"sf,omitempty"`
	// Units is the unit as the definition writes it ("W", "Pct").
	Units string `json:"u,omitempty"`
	// RW is true for a writable point.
	RW bool `json:"rw,omitempty"`
	// M is true for a mandatory point.
	M bool `json:"m,omitempty"`
	// Static is true for a point that does not change while the device
	// runs (nameplate values, identity).
	Static bool `json:"st,omitempty"`
	// Symbols names the values of an enum, or the bits of a bitfield
	// (bit numbers counting from 0).
	Symbols map[string]int64 `json:"sym,omitempty"`
	// Label is the definition's short label.
	Label string `json:"l,omitempty"`
}

// Group is a block of points after the fixed part of a model, or after
// the points of another group (nested), occurring once or repeating.
type Group struct {
	// Name is the group's name ("module", "PFWInj", "Crv", "Pt").
	Name string `json:"name"`
	// Offset is where the first occurrence starts: from the model's first
	// data register for a top-level group, from the start of the parent
	// occurrence for a nested one. Repeating groups before it are counted
	// as absent; Layout gives the real places.
	Offset int `json:"off"`
	// Len is the length of one occurrence in registers, nested repeating
	// groups counted as absent (so the whole length for a group without
	// them).
	Len int `json:"len"`
	// Count is a fixed number of occurrences (1 for a group that occurs
	// once); 0 when CountPoint or the model length gives it.
	Count int `json:"count,omitempty"`
	// CountPoint names the point that holds the number of occurrences: a
	// point of an enclosing group or of the model's top level (in the
	// SunSpec definitions always the latter).
	CountPoint string `json:"countPoint,omitempty"`
	// FromLength is true when the occurrences fill the model's length.
	FromLength bool `json:"fromLength,omitempty"`
	// Points are the group's points, their offsets counted from the start
	// of an occurrence.
	Points []Point `json:"points"`
	// Groups are the groups nested in each occurrence, after its points
	// (model 705's curve points, the trip curves of models 707 to 710).
	Groups []Group `json:"groups,omitempty"`
}

// Model is one SunSpec model definition.
type Model struct {
	// ID is the model number (1, 103, 704).
	ID int `json:"id"`
	// Name is the definition's name ("common", "inverter_three_phase").
	Name string `json:"name"`
	// Label is the definition's short label.
	Label string `json:"l"`
	// Len is the fixed length, when the model has one.
	Len int `json:"len,omitempty"`
	// Fixed is the length of the fixed part of a variable-length model.
	Fixed int `json:"fixed,omitempty"`
	// Points are the points of the fixed part.
	Points []Point `json:"points"`
	// Groups are the groups after the fixed part.
	Groups []Group `json:"groups,omitempty"`
}

var (
	loadOnce sync.Once
	modelsMu sync.RWMutex
	models   map[int]*Model
	loadErr  error
)

func load() {
	var doc struct {
		Models []*Model `json:"models"`
	}
	if loadErr = json.Unmarshal(modelsJSON, &doc); loadErr != nil {
		return
	}
	models = map[int]*Model{}
	for _, m := range doc.Models {
		models[m.ID] = m
	}
}

// Lookup returns a model definition, or nil for a model this package does
// not describe.
func Lookup(id int) *Model {
	loadOnce.Do(load)
	modelsMu.RLock()
	defer modelsMu.RUnlock()
	return models[id]
}

// Known lists the IDs of the described models.
func Known() []int {
	loadOnce.Do(load)
	modelsMu.RLock()
	defer modelsMu.RUnlock()
	out := make([]int, 0, len(models))
	for id := range models {
		out = append(out, id)
	}
	sort.Ints(out)
	return out
}

// Reader reads holding registers (function code 3).
type Reader interface {
	ReadHolding(addr, count uint16) ([]uint16, error)
}

// Refuser may be implemented by a Reader to say which of its errors are
// the device's answer (a Modbus exception, such as illegal data address)
// rather than silence or a broken connection. Find uses it to tell a
// Modbus device without a SunSpec map (ErrNotSunSpec) from one that did
// not answer (the read error). A Reader without it is taken to report
// only silence. mbtcp.Reader implements it.
type Refuser interface {
	Refused(err error) bool
}

func refused(r Reader, err error) bool {
	rf, ok := r.(Refuser)
	return ok && rf.Refused(err)
}

// Instance is one model found in a device's chain.
type Instance struct {
	// ID is the model number.
	ID int `json:"id"`
	// Addr is the address of the model's first data register (after ID
	// and L); L its length.
	Addr uint16 `json:"addr"`
	L    int    `json:"len"`
	// Def is the definition, nil for an unknown model.
	Def *Model `json:"-"`
	// Name is the model's name, with ".2", ".3" … when the device has
	// it more than once.
	Name string `json:"name"`
}

// Map is a device's SunSpec map.
type Map struct {
	// Base is the address of the marker.
	Base uint16 `json:"base"`
	// Models are the models in the order of the chain.
	Models []Instance `json:"models"`
}

// ErrNotSunSpec is returned when no base address holds the marker.
var ErrNotSunSpec = errors.New("no SunSpec map: no \"SunS\" marker at 40000, 50000, 0 or the addresses next to them")

// Find looks for the marker at the base addresses and walks the model
// chain. A read that fails at one base is not fatal. When none of Bases
// holds the marker, Find tries the addresses next to each base the device
// answered at (40001, 39999, 50001, 49999, 1): a map placed by someone
// who counted registers from one, or read from a device that does. When
// no address holds the marker, Find returns ErrNotSunSpec if the device
// answered at any (with registers, or with an error its Refuser calls a
// refusal), else the error of the last read.
func Find(r Reader) (*Map, error) {
	p := find(r, Bases)
	if p.found {
		return p.m, p.err
	}
	var next []uint16
	for _, b := range p.answeredAt {
		for _, n := range []int{int(b) + 1, int(b) - 1} {
			if n >= 0 && n <= 0xFFFD && !slices.Contains(Bases, uint16(n)) && !slices.Contains(next, uint16(n)) {
				next = append(next, uint16(n))
			}
		}
	}
	if len(next) == 0 {
		return nil, p.notFound()
	}
	q := find(r, next)
	if q.found {
		return q.m, q.err
	}
	return nil, p.notFound() // the device answered at the bases
}

// FindAt looks for the marker at the given addresses only, in order, and
// walks the chain from the first that holds it; the errors are Find's.
func FindAt(r Reader, bases ...uint16) (*Map, error) {
	p := find(r, bases)
	if p.found {
		return p.m, p.err
	}
	return nil, p.notFound()
}

type probe struct {
	m          *Map
	err        error // the walk's error when found, else the last read error
	found      bool
	answeredAt []uint16
}

func (p probe) notFound() error {
	if len(p.answeredAt) > 0 || p.err == nil {
		return ErrNotSunSpec
	}
	return p.err
}

// find tries the bases in order and walks the chain from the first that
// holds the marker.
func find(r Reader, bases []uint16) probe {
	var p probe
	for _, base := range bases {
		regs, err := r.ReadHolding(base, 2)
		if err != nil {
			p.err = err
			if refused(r, err) {
				p.answeredAt = append(p.answeredAt, base)
			}
			continue
		}
		p.answeredAt = append(p.answeredAt, base)
		if len(regs) == 2 && regs[0] == Marker[0] && regs[1] == Marker[1] {
			m, err := Walk(r, base)
			return probe{m: m, err: err, found: true}
		}
	}
	return p
}

// maxModels bounds a walk (a device with a broken chain).
const maxModels = 256

// Walk reads the model chain after the marker at base.
func Walk(r Reader, base uint16) (*Map, error) {
	m := &Map{Base: base}
	addr := int(base) + 2
	seen := map[string]int{}
	for range maxModels {
		if addr+2 > 0xFFFF {
			break
		}
		hdr, err := r.ReadHolding(uint16(addr), 2)
		if err != nil {
			if len(m.Models) == 0 {
				return nil, fmt.Errorf("reading the first model header at %d: %w", addr, err)
			}
			// A device that refuses to read past its last model ends
			// the chain there.
			break
		}
		id, l := int(hdr[0]), int(hdr[1])
		if id == EndID || (id == 0 && l == 0) {
			break
		}
		inst := Instance{ID: id, Addr: uint16(addr + 2), L: l, Def: Lookup(id)}
		name := ShortName(id)
		seen[name]++
		if seen[name] > 1 {
			name = fmt.Sprintf("%s.%d", name, seen[name])
		}
		inst.Name = name
		m.Models = append(m.Models, inst)
		addr += 2 + l
	}
	if len(m.Models) == 0 {
		return nil, errors.New("the SunSpec map lists no model")
	}
	return m, nil
}

// short names the models in point names ("inverter.W"); the published
// model names are long ("inverter_three_phase").
var short = map[int]string{
	1: "common", 2: "aggregator", 3: "secureReadRequest", 4: "secureReadResponse", 5: "secureWriteRequest",
	6: "secureWriteSequential", 7: "secureWriteResponse", 8: "getCertificate", 9: "setCertificate",
	10: "commInterface", 11: "ethernet", 12: "ipv4", 13: "ipv6", 14: "proxy", 15: "interfaceCounters",
	16: "simpleIP", 17: "serial", 18: "cellular", 19: "ppp",
	101: "inverter", 102: "inverter", 103: "inverter", 111: "inverter", 112: "inverter", 113: "inverter",
	120: "nameplate", 121: "settings", 122: "status", 123: "controls", 124: "storage", 125: "pricing", 126: "voltVar",
	127: "freqWattParam", 128: "reactiveCurrent", 129: "lvrt", 130: "hvrt", 131: "wattPF", 132: "voltWatt",
	133: "schedule", 134: "freqWatt", 135: "lfrt", 136: "hfrt", 137: "lvrtc", 138: "hvrtc", 139: "lvrtx",
	140: "hvrtx", 141: "lfrtc", 142: "hfrtc", 143: "lfrtx", 144: "hfrtx", 145: "extSettings", 160: "mppt",
	201: "meter", 202: "meter", 203: "meter", 204: "meter", 211: "meter", 212: "meter", 213: "meter", 214: "meter",
	220: "secureMeter",
	302: "irradiance", 303: "moduleTemp", 304: "inclinometer", 305: "location", 306: "refPoint", 307: "baseMet", 308: "miniMet",
	401: "stringCombiner", 402: "stringCombiner", 403: "stringCombiner", 404: "stringCombiner",
	501: "solarModule", 502: "solarModule", 601: "tracker",
	701: "derAc", 702: "derCapacity", 703: "derEnterService", 704: "derControls", 705: "derVoltVar", 706: "derVoltWatt",
	707: "derTripLV", 708: "derTripHV", 709: "derTripLF", 710: "derTripHF", 711: "derFreqDroop", 712: "derWattVar",
	713: "derStorage", 714: "derDC", 715: "derControl",
	801: "storageBase", 802: "battery", 803: "lithiumIonBank", 804: "lithiumIonString", 805: "lithiumIonModule",
	806: "flowBattery", 807: "flowBatteryString", 808: "flowBatteryModule", 809: "flowBatteryStack",
	63001: "test1", 63002: "test2",
	64001: "statusConfig", 64020: "stringMonitor", 64101: "inverterExtension", 64111: "chargeController",
	64112: "chargeControllerExt", 64410: "dcSimulator", 64411: "acSimulator", 64412: "derExploitation",
	64413: "pvCurves", 64414: "derSimControls", 64415: "csipControl",
}

// ShortName is the name a model's points are written under: a short
// name for the described models, model<id> for others.
func ShortName(id int) string {
	if s, ok := short[id]; ok {
		return s
	}
	return fmt.Sprintf("model%d", id)
}

// Dynamic reports whether a model holds measurements that change from
// poll to poll; the others (nameplate, settings, controls, curves) are
// read again only now and then.
func Dynamic(id int) bool {
	switch id {
	case 15, 101, 102, 103, 111, 112, 113, 122, 160, 201, 202, 203, 204, 211, 212, 213, 214, 220,
		302, 303, 304, 305, 307, 308, 401, 402, 403, 404, 501, 502, 601, 701, 713, 714,
		801, 802, 803, 804, 805, 806, 807, 808, 809, 64020, 64111, 64112:
		return true
	}
	return false
}

// Model returns the first instance of the model with the given ID.
func (m *Map) Model(id int) *Instance {
	for i := range m.Models {
		if m.Models[i].ID == id {
			return &m.Models[i]
		}
	}
	return nil
}

// Has reports whether the map has any of the models.
func (m *Map) Has(ids ...int) bool {
	for _, id := range ids {
		if m.Model(id) != nil {
			return true
		}
	}
	return false
}

// ReadModel reads a model instance's registers, in blocks of at most 120.
func ReadModel(r Reader, inst Instance) ([]uint16, error) {
	out := make([]uint16, 0, inst.L)
	for off := 0; off < inst.L; off += 120 {
		n := min(120, inst.L-off)
		regs, err := r.ReadHolding(inst.Addr+uint16(off), uint16(n))
		if err != nil {
			return nil, fmt.Errorf("reading model %d at %d: %w", inst.ID, int(inst.Addr)+off, err)
		}
		out = append(out, regs...)
	}
	return out, nil
}

// Value is one decoded point.
type Value struct {
	// Name is the point's name. A point of a group that occurs once is
	// "<group>.<point>"; a point of the model's only repeating group is
	// "<n>.<point>" (n counting from 1, so "2.DCV" in model 160); with
	// more than one repeating group it is "<group>.<n>.<point>". Nested
	// groups follow their parent's name the same way, always with the
	// group's name: "1.Pt.3.V" is the third point of model 705's first
	// curve.
	Name string
	// Path names the point with every group spelt out ("Crv.1.Pt.3.V",
	// "module.2.DCV"); Write, Locate and PlanWrite take either.
	Path string
	// Offset counts registers from the model's first data register.
	Offset int
	// Point is the point's definition.
	Point *Point
	// V is a float64 (scaled), a string, or for enums the symbol name
	// (the number when it has none) and for bitfields the names of the
	// bits set.
	V any
	// Unit is the point's unit through Unit.
	Unit string
	// Raw is the unscaled register value (for writes and read-back).
	Raw int64
}

// Decode turns a model's registers into its implemented points, nested
// groups included (Layout). Scale factor points, pads, points holding
// their "not implemented" value and points whose scale factor is not
// implemented or out of range are left out.
func Decode(inst Instance, regs []uint16) []Value {
	var out []Value
	for _, l := range Layout(inst, regs) {
		v, ok := decodeLocated(l, regs)
		if ok {
			out = append(out, v)
		}
	}
	return out
}

func decodeLocated(l Located, regs []uint16) (Value, bool) {
	v, ok := decodePoint(l.Point, regs, l.Offset)
	if !ok {
		return v, false
	}
	v.Name, v.Path, v.Offset = l.Name, l.Path, l.Offset
	if f, isNum := v.V.(float64); isNum && !strings.HasPrefix(l.Point.Type, "float") {
		sf, scaled, usable := l.Scale(regs)
		if !usable {
			return v, false
		}
		if scaled {
			f *= math.Pow10(sf)
			v.V = round(f, sf)
		}
	}
	return v, true
}

// GroupCount is how many times a top-level group occurs in an instance.
// For nested groups, and for groups after a repeating group whose
// occurrences have nested repeating groups, use Layout.
func GroupCount(inst Instance, g *Group, regs []uint16) int {
	switch {
	case g.Count > 0:
		return g.Count
	case g.CountPoint != "":
		for _, p := range inst.Def.Points {
			if p.Name == g.CountPoint && p.Offset < len(regs) {
				n := int(regs[p.Offset])
				if n == 0xFFFF {
					return 0
				}
				// Never past the registers read.
				return min(n, max(0, (len(regs)-g.Offset)/g.Len))
			}
		}
		return 0
	case g.Len > 0:
		// No count point: as many as the model's length holds.
		return max(0, (min(inst.L, len(regs))-g.Offset)/g.Len)
	}
	return 0
}

// decodePoint decodes the point at off, unscaled.
func decodePoint(p *Point, regs []uint16, off int) (Value, bool) {
	if off < 0 || p.Size < 1 || off+p.Size > len(regs) {
		return Value{}, false
	}
	w := regs[off : off+p.Size]
	v := Value{Point: p, Unit: Unit(p.Units)}
	switch p.Type {
	case "sunssf", "pad":
		return v, false
	case "string":
		s := decodeString(w)
		if s == "" {
			return v, false
		}
		v.V = s
		return v, true
	case "ipaddr":
		if w[0] == 0 && w[1] == 0 {
			return v, false
		}
		v.V = fmt.Sprintf("%d.%d.%d.%d", w[0]>>8, w[0]&0xFF, w[1]>>8, w[1]&0xFF)
		return v, true
	case "ipv6addr":
		b := make([]byte, 0, 16)
		for _, x := range w {
			b = binary.BigEndian.AppendUint16(b, x)
		}
		if len(b) != 16 || allZero(b) {
			return v, false
		}
		v.V = netip.AddrFrom16([16]byte(b)).String()
		return v, true
	case "eui48":
		b := make([]byte, 0, 8)
		for _, x := range w {
			b = binary.BigEndian.AppendUint16(b, x)
		}
		b = b[len(b)-6:]
		if allFF(b) {
			return v, false
		}
		v.V = fmt.Sprintf("%02x:%02x:%02x:%02x:%02x:%02x", b[0], b[1], b[2], b[3], b[4], b[5])
		return v, true
	case "float32":
		bits := uint32(w[0])<<16 | uint32(w[1])
		f32 := math.Float32frombits(bits)
		if math.IsNaN(float64(f32)) || f32 == pysunspec2Float32 {
			return v, false
		}
		v.V = float64(f32)
		return v, true
	case "float64":
		var bits uint64
		for _, x := range w {
			bits = bits<<16 | uint64(x)
		}
		f := math.Float64frombits(bits)
		if math.IsNaN(f) || f == pysunspec2Float64 {
			return v, false
		}
		v.V = f
		return v, true
	}
	raw, ok := Raw(p.Type, w)
	if !ok {
		return v, false
	}
	v.Raw = raw
	switch {
	case strings.HasPrefix(p.Type, "enum"):
		v.V = raw
		for name, n := range p.Symbols {
			if n == raw {
				v.V = name
			}
		}
		return v, true
	case strings.HasPrefix(p.Type, "bitfield"):
		set := []string{}
		for name, bit := range p.Symbols {
			if bit >= 0 && bit < 64 && uint64(raw)&(1<<uint(bit)) != 0 {
				set = append(set, name)
			}
		}
		sort.Strings(set)
		v.V = set
		return v, true
	}
	if p.Type == "uint64" || p.Type == "acc64" {
		v.V = float64(uint64(raw))
	} else {
		v.V = float64(raw)
	}
	return v, true
}

// A float's "not implemented" value is a NaN (0x7FC00000 for float32).
// pysunspec2 (1.3.6) writes it instead as the number with that value,
// 2143289344 (0x4EFF8000 as float32; for float64 the number
// 0x7FF8000000000000), and reads that number back as not implemented, so
// devices and simulators built with it carry those numbers. They are
// taken as not implemented here as well.
const (
	pysunspec2Float32 = float32(0x7FC00000)
	pysunspec2Float64 = float64(0x7FF8000000000000)
)

// round removes the float noise of a negative power of ten (230.10000001).
func round(f float64, sf int) float64 {
	if sf >= 0 {
		return f
	}
	p := math.Pow10(-sf)
	return math.Round(f*p) / p
}

// Raw reads an integer type's value; ok is false for its "not
// implemented" value.
func Raw(typ string, w []uint16) (int64, bool) {
	var u uint64
	for _, x := range w {
		u = u<<16 | uint64(x)
	}
	switch typ {
	case "int16":
		return int64(int16(u)), u != 0x8000
	case "uint16", "enum16", "bitfield16", "count":
		return int64(u), u != 0xFFFF
	case "acc16":
		return int64(u), u != 0
	case "int32":
		return int64(int32(u)), u != 0x80000000
	case "uint32", "enum32", "bitfield32":
		return int64(u), u != 0xFFFFFFFF
	case "acc32":
		return int64(u), u != 0
	case "int64":
		return int64(u), u != 0x8000000000000000
	case "uint64":
		return int64(u), u != 0xFFFFFFFFFFFFFFFF
	case "acc64":
		return int64(u), u != 0
	case "bitfield64":
		return int64(u), u != 0xFFFFFFFFFFFFFFFF
	}
	return 0, false
}

func decodeString(w []uint16) string {
	b := make([]byte, 0, 2*len(w))
	for _, x := range w {
		b = binary.BigEndian.AppendUint16(b, x)
	}
	if i := strings.IndexByte(string(b), 0); i >= 0 {
		b = b[:i]
	}
	return strings.TrimSpace(strings.ToValidUTF8(string(b), ""))
}

func allZero(b []byte) bool {
	for _, x := range b {
		if x != 0 {
			return false
		}
	}
	return true
}

func allFF(b []byte) bool {
	for _, x := range b {
		if x != 0xFF {
			return false
		}
	}
	return true
}

// Unit maps a SunSpec unit to a UCUM-like unit: percentages of any
// reference become "%", seconds "s", degrees Celsius "Cel", reactive
// power "var", a power factor's cos() nothing; others are kept.
func Unit(u string) string {
	switch u {
	case "Pct", "% WMax", "% VArMax", "% VArAval", "%WHRtg", "%":
		return "%"
	case "Secs":
		return "s"
	case "var", "Var":
		return "var"
	case "varh":
		return "varh"
	case "cos()":
		return ""
	case "C":
		return "Cel"
	// Units of the models added in v0.2.0 (the ones above keep their
	// v0.1.0 mapping; others, such as "% VRef", stay as written).
	case "VNomPct", "WMaxPct", "VarPct", "DeptRef", "% WRef":
		return "%"
	case "Sec", "Tms":
		return "s"
	case "mSecs":
		return "ms"
	case "Tmh":
		return "h"
	case "Tmd":
		return "d"
	case "Degrees", "deg":
		return "deg"
	case "W/m^2":
		return "W/m2"
	case "HPa":
		return "hPa"
	case "kO":
		return "kOhm"
	case "kAH":
		return "kAh"
	case "mps":
		return "m/s"
	case "meters":
		return "m"
	case "PF":
		return ""
	}
	return u
}

// Identity is what model 1 (common) says about the device.
type Identity struct {
	// Manufacturer, Model, Options, Version and Serial are model 1's Mn,
	// Md, Opt, Vr and SN points.
	Manufacturer string `json:"manufacturer,omitempty"`
	Model        string `json:"model,omitempty"`
	Options      string `json:"options,omitempty"`
	Version      string `json:"version,omitempty"`
	Serial       string `json:"serial,omitempty"`
	// DeviceAddress is the Modbus unit the device says it answers on.
	DeviceAddress int `json:"deviceAddress,omitempty"`
}

// ReadIdentity reads model 1.
func ReadIdentity(r Reader, m *Map) (Identity, error) {
	inst := m.Model(1)
	if inst == nil {
		return Identity{}, errors.New("the SunSpec map has no common model (1)")
	}
	regs, err := ReadModel(r, *inst)
	if err != nil {
		return Identity{}, err
	}
	var id Identity
	for _, v := range Decode(*inst, regs) {
		switch v.Name {
		case "Mn":
			id.Manufacturer, _ = v.V.(string)
		case "Md":
			id.Model, _ = v.V.(string)
		case "Opt":
			id.Options, _ = v.V.(string)
		case "Vr":
			id.Version, _ = v.V.(string)
		case "SN":
			id.Serial, _ = v.V.(string)
		case "DA":
			if f, ok := v.V.(float64); ok {
				id.DeviceAddress = int(f)
			}
		}
	}
	return id, nil
}

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
//   - Repeating groups (model 160's modules, model 126's curves) repeat
//     after the fixed part, their count given by a point or by L.
//
// Unknown models are kept as their ID and length (a vendor model, a newer
// SunSpec model), never refused.
package sunspec

import (
	_ "embed"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"math"
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

// Group is a block of points that repeats after the fixed part.
type Group struct {
	// Name is the group's name ("module", "PFWInj").
	Name string `json:"name"`
	// Offset is where the first occurrence starts, from the model's
	// first data register.
	Offset int `json:"off"`
	// Len is the length of one occurrence in registers.
	Len int `json:"len"`
	// Count is a fixed number of occurrences (1 for a group that occurs
	// once); 0 when CountPoint or the model length gives it.
	Count int `json:"count,omitempty"`
	// CountPoint names the top-level point that holds the number of
	// occurrences.
	CountPoint string `json:"countPoint,omitempty"`
	// FromLength is true when the occurrences fill the model's length.
	FromLength bool `json:"fromLength,omitempty"`
	// Points are the group's points, their offsets counted from the start
	// of an occurrence.
	Points []Point `json:"points"`
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
	return models[id]
}

// Known lists the IDs of the described models.
func Known() []int {
	loadOnce.Do(load)
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
var ErrNotSunSpec = errors.New("no SunSpec map: none of the addresses 40000, 50000 or 0 starts with \"SunS\"")

// Find looks for the marker at the base addresses and walks the model
// chain. A read that fails at one base is not fatal. When no base holds
// the marker, Find returns ErrNotSunSpec if the device answered at any
// base (with registers, or with an error its Refuser calls a refusal),
// else the error of the last base.
func Find(r Reader) (*Map, error) {
	var lastErr error
	answered := false
	for _, base := range Bases {
		regs, err := r.ReadHolding(base, 2)
		if err != nil {
			lastErr = err
			answered = answered || refused(r, err)
			continue
		}
		answered = true
		if len(regs) == 2 && regs[0] == Marker[0] && regs[1] == Marker[1] {
			return Walk(r, base)
		}
	}
	if answered || lastErr == nil {
		return nil, ErrNotSunSpec
	}
	return nil, lastErr
}

// maxModels bounds a walk (a device with a broken chain).
const maxModels = 64

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
	1: "common", 101: "inverter", 102: "inverter", 103: "inverter", 111: "inverter", 112: "inverter", 113: "inverter",
	120: "nameplate", 121: "settings", 122: "status", 123: "controls", 124: "storage", 126: "voltVar", 160: "mppt",
	201: "meter", 202: "meter", 203: "meter", 204: "meter",
	701: "derAc", 702: "derCapacity", 703: "derEnterService", 704: "derControls", 705: "derVoltVar", 713: "derStorage", 715: "derControl",
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
	case 101, 102, 103, 111, 112, 113, 122, 160, 201, 202, 203, 204, 701, 713:
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
	// more than one repeating group it is "<group>.<n>.<point>".
	Name string
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

// Decode turns a model's registers into its implemented points. Scale
// factor points, pads and points holding their "not implemented" value
// are left out.
func Decode(inst Instance, regs []uint16) []Value {
	def := inst.Def
	if def == nil {
		return nil
	}
	var out []Value
	sfs := map[string]int64{}
	for i := range def.Points {
		p := &def.Points[i]
		if p.Type == "sunssf" && p.Offset+1 <= len(regs) {
			sfs[p.Name] = int64(int16(regs[p.Offset]))
		}
	}
	for i := range def.Points {
		p := &def.Points[i]
		if v, ok := decodePoint(p, regs, 0, sfs); ok {
			v.Name = p.Name
			out = append(out, v)
		}
	}
	repeating := 0
	for _, g := range def.Groups {
		if g.Count != 1 {
			repeating++
		}
	}
	for gi := range def.Groups {
		g := &def.Groups[gi]
		n := GroupCount(inst, g, regs)
		for k := 0; k < n; k++ {
			base := g.Offset + k*g.Len
			// Group scale factors may sit in the group itself.
			gsf := map[string]int64{}
			for s, v := range sfs {
				gsf[s] = v
			}
			for i := range g.Points {
				p := &g.Points[i]
				if p.Type == "sunssf" && base+p.Offset < len(regs) {
					gsf[p.Name] = int64(int16(regs[base+p.Offset]))
				}
			}
			for i := range g.Points {
				p := &g.Points[i]
				v, ok := decodePoint(p, regs, base, gsf)
				if !ok {
					continue
				}
				switch {
				case g.Count == 1:
					v.Name = g.Name + "." + p.Name
				case repeating == 1:
					// The model's one repeating block: "mppt.2.DCV".
					v.Name = fmt.Sprintf("%d.%s", k+1, p.Name)
				default:
					v.Name = fmt.Sprintf("%s.%d.%s", g.Name, k+1, p.Name)
				}
				out = append(out, v)
			}
		}
	}
	return out
}

// GroupCount is how many times a repeating group occurs in an instance.
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

func decodePoint(p *Point, regs []uint16, base int, sfs map[string]int64) (Value, bool) {
	off := base + p.Offset
	if off < 0 || off+p.Size > len(regs) {
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
			if raw&(1<<uint(bit)) != 0 {
				set = append(set, name)
			}
		}
		sort.Strings(set)
		v.V = set
		return v, true
	}
	f := float64(raw)
	if sf, ok := scaleOf(p, sfs); ok {
		if sf == math.MinInt16 { // the scale factor is not implemented
			return v, false
		}
		f *= math.Pow10(int(sf))
		f = round(f, int(sf))
	}
	v.V = f
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

func scaleOf(p *Point, sfs map[string]int64) (int64, bool) {
	switch s := p.SF.(type) {
	case string:
		v, ok := sfs[s]
		return v, ok
	case float64:
		return int64(s), true
	}
	return 0, false
}

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

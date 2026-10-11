// Copyright 2026 Abhinav Gautam
// SPDX-License-Identifier: Apache-2.0

package sunspec_test

import (
	"errors"
	"fmt"
	"math"
	"slices"
	"strings"
	"testing"

	"github.com/cB-Sigma/sunspec"
)

// memReader is an in-memory register map. Reads of an address it does
// not hold fail with errRefused when refuse is set (a Modbus exception),
// else with errSilent.
type memReader struct {
	regs   map[uint16]uint16
	refuse bool
	reads  [][2]uint16
}

var (
	errRefused = errors.New("exception 2")
	errSilent  = errors.New("timed out")
)

func (m *memReader) ReadHolding(addr, count uint16) ([]uint16, error) {
	m.reads = append(m.reads, [2]uint16{addr, count})
	out := make([]uint16, count)
	for i := range out {
		v, ok := m.regs[addr+uint16(i)]
		if !ok {
			if m.refuse {
				return nil, errRefused
			}
			return nil, errSilent
		}
		out[i] = v
	}
	return out, nil
}

// refusingReader adds the Refuser.
type refusingReader struct{ *memReader }

func (refusingReader) Refused(err error) bool { return errors.Is(err, errRefused) }

// layout puts the marker at base and the models (id, data) after it;
// end appends the end model.
func layout(base uint16, end bool, models ...[]uint16) map[uint16]uint16 {
	regs := map[uint16]uint16{base: 0x5375, base + 1: 0x6E53}
	a := base + 2
	for _, m := range models {
		regs[a], regs[a+1] = m[0], uint16(len(m)-1)
		for i, w := range m[1:] {
			regs[a+2+uint16(i)] = w
		}
		a += 2 + uint16(len(m)-1)
	}
	if end {
		regs[a], regs[a+1] = 0xFFFF, 0
	}
	return regs
}

func model(id uint16, data []uint16) []uint16 { return append([]uint16{id}, data...) }

func ids(m *sunspec.Map) []int {
	var out []int
	for _, inst := range m.Models {
		out = append(out, inst.ID)
	}
	return out
}

func TestFindBases(t *testing.T) {
	for _, base := range sunspec.Bases {
		r := &memReader{regs: layout(base, true, model(1, filled(66, 0)), model(64901, []uint16{7})), refuse: true}
		m, err := sunspec.Find(refusingReader{r})
		if err != nil || m.Base != base || !slices.Equal(ids(m), []int{1, 64901}) {
			t.Fatalf("base %d: %+v %v", base, m, err)
		}
		if m.Models[0].Addr != base+4 || m.Models[0].L != 66 || m.Models[1].Addr != base+4+66+2 {
			t.Errorf("base %d: addresses %+v", base, m.Models)
		}
	}
	// The order is 40000, then 50000, then 0: a map at 0 is found only
	// after the other two were tried.
	r := &memReader{regs: layout(0, true, model(1, filled(66, 0))), refuse: true}
	if _, err := sunspec.Find(refusingReader{r}); err != nil {
		t.Fatal(err)
	}
	if r.reads[0] != [2]uint16{40000, 2} || r.reads[1] != [2]uint16{50000, 2} || r.reads[2] != [2]uint16{0, 2} {
		t.Errorf("reads %v", r.reads)
	}
}

func TestFindNotSunSpec(t *testing.T) {
	// The device answers registers without the marker.
	r := &memReader{regs: map[uint16]uint16{40000: 1, 40001: 2}}
	if _, err := sunspec.Find(r); err != sunspec.ErrNotSunSpec {
		t.Errorf("registers without marker: %v", err)
	}
	// The device refuses every base with an exception.
	r = &memReader{regs: map[uint16]uint16{}, refuse: true}
	if _, err := sunspec.Find(refusingReader{r}); err != sunspec.ErrNotSunSpec {
		t.Errorf("refused: %v", err)
	}
	// Without a Refuser the same errors are silence: the read error comes
	// back.
	if _, err := sunspec.Find(r); !errors.Is(err, errRefused) {
		t.Errorf("refused without Refuser: %v", err)
	}
	r = &memReader{regs: map[uint16]uint16{}}
	if _, err := sunspec.Find(refusingReader{r}); !errors.Is(err, errSilent) {
		t.Errorf("silent: %v", err)
	}
}

func TestWalkChain(t *testing.T) {
	// The chain ends at the end model; registers after it are not read.
	regs := layout(40000, true, model(1, filled(66, 0)), model(120, filled(26, 0xFFFF)))
	regs[40000+2+68+28+2] = 1 // something after the end model
	r := &memReader{regs: regs}
	m, err := sunspec.Walk(r, 40000)
	if err != nil || !slices.Equal(ids(m), []int{1, 120}) {
		t.Fatalf("%v %v", ids(m), err)
	}
	if m.Models[1].Def == nil || m.Models[1].Name != "nameplate" {
		t.Errorf("nameplate %+v", m.Models[1])
	}
	// A chain that stops without an end model ends where the device
	// refuses the next header.
	r = &memReader{regs: layout(40000, false, model(1, filled(66, 0)), model(103, filled(50, 0xFFFF)))}
	m, err = sunspec.Walk(r, 40000)
	if err != nil || !slices.Equal(ids(m), []int{1, 103}) {
		t.Errorf("no end model: %v %v", ids(m), err)
	}
	// ID 0 with L 0 also ends it.
	regs = layout(40000, false, model(1, filled(66, 0)))
	regs[40000+2+68], regs[40000+2+69] = 0, 0
	m, err = sunspec.Walk(&memReader{regs: regs}, 40000)
	if err != nil || !slices.Equal(ids(m), []int{1}) {
		t.Errorf("0/0 end: %v %v", ids(m), err)
	}
	// A model met twice gets ".2".
	r = &memReader{regs: layout(40000, true, model(1, filled(66, 0)), model(201, filled(105, 0x8000)), model(201, filled(105, 0x8000)), model(64900, nil))}
	m, err = sunspec.Walk(r, 40000)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, inst := range m.Models {
		names = append(names, inst.Name)
	}
	if !slices.Equal(names, []string{"common", "meter", "meter.2", "model64900"}) {
		t.Errorf("names %v", names)
	}
	if m.Model(201) != &m.Models[1] || m.Model(999) != nil || !m.Has(999, 201) || m.Has(999) {
		t.Error("Model/Has")
	}
	// Nothing after the marker: an error naming the first header.
	_, err = sunspec.Walk(&memReader{regs: layout(40000, false)}, 40000)
	if err == nil || !strings.Contains(err.Error(), "first model header at 40002") {
		t.Errorf("empty chain: %v", err)
	}
	// An end model straight after the marker lists no model.
	if _, err := sunspec.Walk(&memReader{regs: layout(40000, true)}, 40000); err == nil {
		t.Error("map with no model accepted")
	}
}

func TestWalkBounded(t *testing.T) {
	// A broken chain of empty vendor models never ends by itself; the
	// walk stops at 256 models (a device holding every SunSpec model has
	// 112).
	regs := map[uint16]uint16{}
	for a := uint16(40002); a < 40002+2*400; a += 2 {
		regs[a], regs[a+1] = 64999, 0
	}
	m, err := sunspec.Walk(&memReader{regs: regs}, 40000)
	if err != nil || len(m.Models) != 256 {
		t.Errorf("walk of an endless chain: %d models, %v", len(m.Models), err)
	}
}

func TestUnknownModelKept(t *testing.T) {
	r := &memReader{regs: layout(40000, true, model(64900, []uint16{1, 2, 3}))}
	m, err := sunspec.Walk(r, 40000)
	if err != nil {
		t.Fatal(err)
	}
	inst := m.Models[0]
	if inst.Def != nil || inst.L != 3 || inst.Name != "model64900" || inst.Addr != 40004 {
		t.Errorf("%+v", inst)
	}
	regs, err := sunspec.ReadModel(r, inst)
	if err != nil || !slices.Equal(regs, []uint16{1, 2, 3}) {
		t.Errorf("read %v %v", regs, err)
	}
	if v := sunspec.Decode(inst, regs); v != nil {
		t.Errorf("unknown model decoded: %v", v)
	}
}

func TestReadModelBlocks(t *testing.T) {
	data := make([]uint16, 153) // model 701 is longer than one read
	for i := range data {
		data[i] = uint16(i)
	}
	r := &memReader{regs: layout(40000, true, model(701, data))}
	m, err := sunspec.Walk(r, 40000)
	if err != nil {
		t.Fatal(err)
	}
	r.reads = nil
	regs, err := sunspec.ReadModel(r, m.Models[0])
	if err != nil || !slices.Equal(regs, data) {
		t.Fatalf("%v", err)
	}
	if len(r.reads) != 2 || r.reads[0] != [2]uint16{40004, 120} || r.reads[1] != [2]uint16{40124, 33} {
		t.Errorf("reads %v", r.reads)
	}
	r.regs[40130] = 0
	delete(r.regs, 40130)
	if _, err := sunspec.ReadModel(r, m.Models[0]); err == nil || !strings.Contains(err.Error(), "model 701 at 40124") {
		t.Errorf("failed read: %v", err)
	}
}

func TestRawNotImplemented(t *testing.T) {
	for _, c := range []struct {
		typ  string
		w    []uint16
		want int64
		ok   bool
	}{
		{"int16", []uint16{0x8000}, 0, false},
		{"int16", []uint16{0xFFFF}, -1, true},
		{"uint16", []uint16{0xFFFF}, 0, false},
		{"uint16", []uint16{0xFFFE}, 0xFFFE, true},
		{"enum16", []uint16{0xFFFF}, 0, false},
		{"bitfield16", []uint16{0xFFFF}, 0, false},
		{"count", []uint16{0xFFFF}, 0, false},
		{"acc16", []uint16{0}, 0, false},
		{"acc16", []uint16{5}, 5, true},
		{"int32", []uint16{0x8000, 0}, 0, false},
		{"int32", []uint16{0xFFFF, 0xFFFE}, -2, true},
		{"uint32", []uint16{0xFFFF, 0xFFFF}, 0, false},
		{"enum32", []uint16{0xFFFF, 0xFFFF}, 0, false},
		{"bitfield32", []uint16{0xFFFF, 0xFFFF}, 0, false},
		{"acc32", []uint16{0, 0}, 0, false},
		{"acc32", []uint16{1, 0x86A0}, 100000, true},
		{"int64", []uint16{0x8000, 0, 0, 0}, 0, false},
		{"int64", []uint16{0xFFFF, 0xFFFF, 0xFFFF, 0xFFFF}, -1, true},
		{"uint64", []uint16{0xFFFF, 0xFFFF, 0xFFFF, 0xFFFF}, 0, false},
		{"acc64", []uint16{0, 0, 0, 0}, 0, false},
		{"acc64", []uint16{0, 0, 1, 0}, 65536, true},
		{"bitfield64", []uint16{0xFFFF, 0xFFFF, 0xFFFF, 0xFFFF}, 0, false},
		{"string", []uint16{0x4142}, 0, false}, // not an integer type
	} {
		got, ok := sunspec.Raw(c.typ, c.w)
		if ok != c.ok || ok && got != c.want {
			t.Errorf("Raw(%s, %#x) = %d %v, want %d %v", c.typ, c.w, got, ok, c.want, c.ok)
		}
	}
}

// decoded decodes regs with a made-up definition and returns name → V.
func decoded(def *sunspec.Model, regs []uint16) map[string]any {
	out := map[string]any{}
	for _, v := range sunspec.Decode(sunspec.Instance{ID: def.ID, L: len(regs), Def: def}, regs) {
		out[v.Name] = v.V
	}
	return out
}

func TestDecodeScaleFactors(t *testing.T) {
	def := &sunspec.Model{ID: 64000, Points: []sunspec.Point{
		{Name: "V", Offset: 0, Type: "uint16", Size: 1, SF: "V_SF", Units: "V"},
		{Name: "V_SF", Offset: 1, Type: "sunssf", Size: 1},
		{Name: "W", Offset: 2, Type: "int16", Size: 1, SF: "W_SF", Units: "W"},
		{Name: "W_SF", Offset: 3, Type: "sunssf", Size: 1},
		{Name: "Fixed", Offset: 4, Type: "uint16", Size: 1, SF: float64(-2)},
		{Name: "NoSF", Offset: 5, Type: "uint16", Size: 1, SF: "Missing_SF"},
		{Name: "Gone", Offset: 6, Type: "uint16", Size: 1, SF: "Gone_SF"},
		{Name: "Gone_SF", Offset: 7, Type: "sunssf", Size: 1},
		{Name: "Pad", Offset: 8, Type: "pad", Size: 1},
	}}
	got := decoded(def, []uint16{2301, 0xFFFF, 0xFFF6, 2, 5002, 7, 9, 0x8000, 0})
	want := map[string]any{"V": 230.1, "W": -1000.0, "Fixed": 50.02, "NoSF": 7.0}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("got %v, want %v", got, want)
	}
	// Scale factors are not points; a value whose scale factor is not
	// implemented is left out, as are pads.
	for _, k := range []string{"V_SF", "W_SF", "Gone", "Pad"} {
		if _, ok := got[k]; ok {
			t.Errorf("%s decoded", k)
		}
	}
	// No float noise from negative powers of ten.
	got = decoded(def, []uint16{3, 0xFFFD, 0, 0, 0xFFFE, 0, 0, 0, 0})
	if got["V"] != 0.003 || got["Fixed"] != 655.34 {
		t.Errorf("rounding %v", got)
	}
}

func TestDecodeEnumsAndBitfields(t *testing.T) {
	def := &sunspec.Model{ID: 64001, Points: []sunspec.Point{
		{Name: "St", Offset: 0, Type: "enum16", Size: 1, Symbols: map[string]int64{"OFF": 1, "MPPT": 4}},
		{Name: "Evt", Offset: 1, Type: "bitfield32", Size: 2, Symbols: map[string]int64{"GROUND_FAULT": 0, "AC_DISCONNECT": 2, "GRID_DISCONNECT": 4}},
		{Name: "Flags", Offset: 3, Type: "bitfield16", Size: 1, Symbols: map[string]int64{"A": 0}},
	}}
	got := decoded(def, []uint16{4, 0, 0b10100, 0})
	if got["St"] != "MPPT" || fmt.Sprint(got["Evt"]) != "[AC_DISCONNECT GRID_DISCONNECT]" || fmt.Sprint(got["Flags"]) != "[]" {
		t.Errorf("%v", got)
	}
	// A value without a symbol is kept as its number; bits without a name
	// are not listed.
	got = decoded(def, []uint16{9, 0x8000, 1, 0xFFFF})
	if got["St"] != int64(9) || fmt.Sprint(got["Evt"]) != "[GROUND_FAULT]" {
		t.Errorf("%v", got)
	}
	if _, ok := got["Flags"]; ok {
		t.Error("bitfield16 0xFFFF is not implemented")
	}
	// Raw keeps the register value for writes and read-back.
	for _, v := range sunspec.Decode(sunspec.Instance{ID: 64001, L: 4, Def: def}, []uint16{4, 0, 0b10100, 0}) {
		if v.Name == "St" && v.Raw != 4 {
			t.Errorf("raw %d", v.Raw)
		}
	}
}

func TestDecodeOtherTypes(t *testing.T) {
	def := &sunspec.Model{ID: 64002, Points: []sunspec.Point{
		{Name: "Name", Offset: 0, Type: "string", Size: 4},
		{Name: "IP", Offset: 4, Type: "ipaddr", Size: 2},
		{Name: "MAC", Offset: 6, Type: "eui48", Size: 4},
		{Name: "F", Offset: 10, Type: "float32", Size: 2, Units: "C"},
		{Name: "D", Offset: 12, Type: "float64", Size: 4},
	}}
	f32 := math.Float32bits(21.5)
	f64 := math.Float64bits(-0.25)
	regs := []uint16{0x2050, 0x5631, 0, 0x4142, 0xC0A8, 0x0102, 0, 0x0011, 0x2233, 0x4455,
		uint16(f32 >> 16), uint16(f32), uint16(f64 >> 48), uint16(f64 >> 32), uint16(f64 >> 16), uint16(f64)}
	got := decoded(def, regs)
	want := map[string]any{"Name": "PV1", "IP": "192.168.1.2", "MAC": "00:11:22:33:44:55", "F": 21.5, "D": -0.25}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("got %v, want %v", got, want)
	}
	nan32 := math.Float32bits(float32(math.NaN()))
	nan64 := math.Float64bits(math.NaN())
	empty := []uint16{0, 0, 0, 0, 0, 0, 0xFFFF, 0xFFFF, 0xFFFF, 0xFFFF,
		uint16(nan32 >> 16), uint16(nan32), uint16(nan64 >> 48), uint16(nan64 >> 32), uint16(nan64 >> 16), uint16(nan64)}
	if got := decoded(def, empty); len(got) != 0 {
		t.Errorf("not implemented values decoded: %v", got)
	}
	// Short registers: points past the end are left out.
	if got := decoded(def, regs[:5]); fmt.Sprint(got) != "map[Name:PV1]" {
		t.Errorf("short: %v", got)
	}
	for _, v := range sunspec.Decode(sunspec.Instance{ID: 64002, L: len(regs), Def: def}, regs) {
		if v.Name == "F" && v.Unit != "Cel" {
			t.Errorf("unit %q", v.Unit)
		}
	}
}

// pysunspec2 1.3.6 encodes a float's "not implemented" as the number
// 0x7FC00000 (float32 bits 0x4EFF8000) or 0x7FF8000000000000 rather than
// the NaN; found reading a pysunspec2-built device (interop). Those
// numbers are not implemented here too; their neighbours still decode.
func TestDecodePysunspec2FloatNotImplemented(t *testing.T) {
	def := &sunspec.Model{ID: 64003, Points: []sunspec.Point{
		{Name: "F", Offset: 0, Type: "float32", Size: 2},
		{Name: "D", Offset: 2, Type: "float64", Size: 4},
		{Name: "G", Offset: 6, Type: "float32", Size: 2},
	}}
	d := math.Float64bits(float64(0x7FF8000000000000))
	g := math.Float32bits(2143289216) // the next float32 below
	regs := []uint16{0x4EFF, 0x8000, uint16(d >> 48), uint16(d >> 32), uint16(d >> 16), uint16(d), uint16(g >> 16), uint16(g)}
	if got := decoded(def, regs); fmt.Sprint(got) != "map[G:2.143289216e+09]" {
		t.Errorf("got %v", got)
	}
}

// Model 111's St carries its symbol names as in models 101 to 113 (the
// source definitions prefix them with "gg"); found against pysunspec2.
func TestModel111StateNames(t *testing.T) {
	for _, id := range []int{101, 102, 103, 111, 112, 113} {
		def := sunspec.Lookup(id)
		regs := make([]uint16, def.Len)
		for i := range regs {
			regs[i] = 0xFFFF
		}
		for _, p := range def.Points {
			if p.Name == "St" {
				regs[p.Offset] = 4
			}
		}
		if got := decoded(def, regs)["St"]; got != "MPPT" {
			t.Errorf("model %d: St = %v", id, got)
		}
	}
}

func TestRepeatingGroups(t *testing.T) {
	// Model 160: the definition gives the modules no count, so they fill
	// L (N is a point like any other).
	def := sunspec.Lookup(160)
	mppt := filled(8+3*20, 0xFFFF)
	mppt[0], mppt[1], mppt[2] = 0xFFFE, 0xFFFF, 0 // DCA_SF -2, DCV_SF -1, DCW_SF 0
	mppt[4], mppt[5], mppt[6] = 0, 0, 3           // N 3
	for k := 0; k < 3; k++ {
		o := 8 + 20*k
		mppt[o], mppt[o+9], mppt[o+10] = uint16(k+1), uint16(100*(k+1)), 6000
	}
	inst := sunspec.Instance{ID: 160, L: len(mppt), Def: def, Name: "mppt"}
	got := map[string]any{}
	for _, v := range sunspec.Decode(inst, mppt) {
		got[v.Name] = v.V
	}
	if got["1.DCA"] != 1.0 || got["2.DCA"] != 2.0 || got["3.DCV"] != 600.0 || got["2.ID"] != 2.0 || got["N"] != 3.0 {
		t.Errorf("%v", got)
	}
	g := &def.Groups[0]
	if n := sunspec.GroupCount(inst, g, mppt); n != 3 {
		t.Errorf("count %d", n)
	}
	// Fewer registers read than L: only whole modules read are counted.
	if n := sunspec.GroupCount(inst, g, mppt[:8+2*20+5]); n != 2 {
		t.Errorf("count of a short read: %d", n)
	}

	// A group counted by a point: the count, cut to the registers read;
	// a count that is not implemented means none.
	counted := &sunspec.Model{ID: 64004, Points: []sunspec.Point{{Name: "N", Offset: 0, Type: "count", Size: 1}}, Groups: []sunspec.Group{
		{Name: "m", Offset: 1, Len: 2, CountPoint: "N", Points: []sunspec.Point{{Name: "v", Offset: 1, Type: "uint16", Size: 1}}},
	}}
	cregs := []uint16{2, 0, 10, 0, 11, 0, 12}
	cinst := sunspec.Instance{ID: 64004, L: len(cregs), Def: counted}
	if got := decoded(counted, cregs); fmt.Sprint(got) != "map[1.v:10 2.v:11 N:2]" {
		t.Errorf("counted %v", got)
	}
	cregs[0] = 9
	if n := sunspec.GroupCount(cinst, &counted.Groups[0], cregs); n != 3 {
		t.Errorf("count past the registers: %d", n)
	}
	cregs[0] = 0xFFFF
	if n := sunspec.GroupCount(cinst, &counted.Groups[0], cregs); n != 0 {
		t.Errorf("count not implemented: %d", n)
	}

	// Model 126: no count point; the curves fill L.
	vv := sunspec.Lookup(126)
	regs := filled(10+2*54, 0xFFFF)
	regs[7] = 0xFFFF // V_SF -1
	regs[8] = 0      // DeptRef_SF 0
	regs[10+2], regs[10+54+2] = 2300, 2400
	inst = sunspec.Instance{ID: 126, L: len(regs), Def: vv}
	got = map[string]any{}
	for _, v := range sunspec.Decode(inst, regs) {
		got[v.Name] = v.V
	}
	if got["1.V1"] != 230.0 || got["2.V1"] != 240.0 || sunspec.GroupCount(inst, &vv.Groups[0], regs) != 2 {
		t.Errorf("volt-var %v", got)
	}

	// Model 704: groups that occur once are named "<group>.<point>".
	der := filled(65, 0xFFFF)
	der[51] = 0xFFFD
	der[57], der[58], der[59] = 950, 1, 900
	got = map[string]any{}
	for _, v := range sunspec.Decode(sunspec.Instance{ID: 704, L: 65, Def: sunspec.Lookup(704)}, der) {
		got[v.Name] = v.V
	}
	if got["PFWInj.PF"] != 0.95 || got["PFWInj.Ext"] != "UNDER_EXCITED" || got["PFWInjRvrt.PF"] != 0.9 {
		t.Errorf("704 groups %v", got)
	}

	// Groups of a made-up model with two repeating groups carry the
	// group name and the occurrence.
	two := &sunspec.Model{ID: 64003, Points: []sunspec.Point{{Name: "N", Offset: 0, Type: "uint16", Size: 1}}, Groups: []sunspec.Group{
		{Name: "a", Offset: 1, Len: 1, CountPoint: "N", Points: []sunspec.Point{{Name: "x", Offset: 0, Type: "uint16", Size: 1}}},
		{Name: "b", Offset: 3, Len: 1, Count: 2, Points: []sunspec.Point{{Name: "y", Offset: 0, Type: "uint16", Size: 1}}},
	}}
	if got := decoded(two, []uint16{2, 10, 11, 20, 21}); fmt.Sprint(got) != "map[N:2 a.1.x:10 a.2.x:11 b.1.y:20 b.2.y:21]" {
		t.Errorf("two groups %v", got)
	}
}

func TestModelTables(t *testing.T) {
	known := slices.DeleteFunc(sunspec.Known(), func(id int) bool { return id == 64950 }) // registered by TestRegisterDefinition
	// Every model of github.com/sunspec/models at the pinned commit.
	want := []int{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16, 17, 18, 19,
		101, 102, 103, 111, 112, 113, 120, 121, 122, 123, 124, 125, 126, 127, 128, 129, 130, 131, 132, 133, 134,
		135, 136, 137, 138, 139, 140, 141, 142, 143, 144, 145, 160, 201, 202, 203, 204, 211, 212, 213, 214, 220,
		302, 303, 304, 305, 306, 307, 308, 401, 402, 403, 404, 501, 502, 601,
		701, 702, 703, 704, 705, 706, 707, 708, 709, 710, 711, 712, 713, 714, 715,
		801, 802, 803, 804, 805, 806, 807, 808, 809, 63001, 63002,
		64001, 64020, 64101, 64111, 64112, 64410, 64411, 64412, 64413, 64414, 64415}
	if !slices.Equal(known, want) {
		t.Errorf("Known() = %v", known)
	}
	for _, id := range known {
		m := sunspec.Lookup(id)
		if m == nil || m.ID != id || len(m.Points)+len(m.Groups) == 0 {
			t.Fatalf("model %d: %+v", id, m)
		}
		repeats := false
		var walk func(gs []sunspec.Group)
		walk = func(gs []sunspec.Group) {
			for _, g := range gs {
				repeats = repeats || g.Count != 1
				walk(g.Groups)
			}
		}
		walk(m.Groups)
		if repeats == (m.Len > 0) {
			t.Errorf("model %d: len %d with repeating groups %v", id, m.Len, repeats)
		}
		// Every point lies inside its model or group; every named scale
		// factor and count point is in scope (the point's group, an
		// enclosing one or the top level).
		var check func(where string, pts []sunspec.Point, gs []sunspec.Group, size int, scope []map[string]bool)
		check = func(where string, pts []sunspec.Point, gs []sunspec.Group, size int, scope []map[string]bool) {
			names := map[string]bool{}
			for _, p := range pts {
				names[p.Name] = true
			}
			scope = append([]map[string]bool{names}, scope...)
			inScope := func(n string) bool {
				for _, s := range scope {
					if s[n] {
						return true
					}
				}
				return false
			}
			end := 0
			for _, p := range pts {
				if p.Size < 1 || p.Offset != end || (size > 0 && p.Offset+p.Size > size) {
					t.Errorf("%s point %s at %d+%d is misplaced (size %d)", where, p.Name, p.Offset, p.Size, size)
				}
				end = p.Offset + p.Size
				if s, ok := p.SF.(string); ok && !inScope(s) {
					t.Errorf("%s point %s names a missing scale factor %s", where, p.Name, s)
				}
			}
			for _, g := range gs {
				if g.CountPoint != "" && !inScope(g.CountPoint) {
					t.Errorf("%s group %s counts by a missing point %s", where, g.Name, g.CountPoint)
				}
				if g.Count == 0 && g.CountPoint == "" && !g.FromLength {
					t.Errorf("%s group %s has no count", where, g.Name)
				}
				check(where+"."+g.Name, g.Points, g.Groups, g.Len, scope)
			}
		}
		check(fmt.Sprint(id), m.Points, m.Groups, max(m.Len, m.Fixed), nil)
		if sunspec.ShortName(id) == fmt.Sprintf("model%d", id) {
			t.Errorf("model %d has no short name", id)
		}
	}
	if sunspec.Lookup(64900) != nil || sunspec.ShortName(64900) != "model64900" {
		t.Error("unknown model")
	}
	if !sunspec.Dynamic(103) || !sunspec.Dynamic(160) || sunspec.Dynamic(120) || sunspec.Dynamic(704) {
		t.Error("Dynamic")
	}
	for in, out := range map[string]string{"Pct": "%", "% WMax": "%", "Secs": "s", "Var": "var", "varh": "varh", "cos()": "", "C": "Cel", "W": "W", "Hz": "Hz", "VNomPct": "%", "DeptRef": "%", "% VRef": "% VRef", "Tms": "s", "mSecs": "ms", "PF": ""} {
		if got := sunspec.Unit(in); got != out {
			t.Errorf("Unit(%q) = %q, want %q", in, got, out)
		}
	}
}

func TestReadIdentity(t *testing.T) {
	r := &memReader{regs: layout(40000, true, model(120, filled(26, 0xFFFF)))}
	m, err := sunspec.Walk(r, 40000)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := sunspec.ReadIdentity(r, m); err == nil || !strings.Contains(err.Error(), "no common model") {
		t.Errorf("no model 1: %v", err)
	}
	common := append(append(append(append(append(str("Maker", 16), str("Model X", 16)...), str("opt", 8)...), str("2.0", 8)...), str("SN-1 ", 16)...), 3, 0x8000)
	r = &memReader{regs: layout(40000, true, model(1, common))}
	m, _ = sunspec.Walk(r, 40000)
	id, err := sunspec.ReadIdentity(r, m)
	if err != nil || id != (sunspec.Identity{Manufacturer: "Maker", Model: "Model X", Options: "opt", Version: "2.0", Serial: "SN-1", DeviceAddress: 3}) {
		t.Errorf("%+v %v", id, err)
	}
}

// str encodes text into n registers (as sim.Str).
func str(s string, n int) []uint16 {
	b := make([]byte, 2*n)
	copy(b, s)
	out := make([]uint16, n)
	for i := range out {
		out[i] = uint16(b[2*i])<<8 | uint16(b[2*i+1])
	}
	return out
}

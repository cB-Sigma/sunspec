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

// voltVar lays out model 705 by hand from its definition: 13 fixed
// registers, then nCrv curves of 10 registers each followed by nPt
// points (V, Var). Curve k, point j: V = 90+j+k/100 % (V_SF -2), Var =
// -10·j·k % (DeptRef_SF -1).
func voltVar(nCrv, nPt int) []uint16 {
	regs := filled(13+nCrv*(10+2*nPt), 0xFFFF)
	regs[0], regs[1], regs[2] = 1, 0, 1 // Ena ENABLED, AdptCrvReq 0, AdptCrvRslt COMPLETED
	regs[3], regs[4] = uint16(nPt), uint16(nCrv)
	regs[10], regs[11], regs[12] = 0xFFFE, 0xFFFF, 0 // V_SF -2, DeptRef_SF -1, RspTms_SF 0
	for k := 1; k <= nCrv; k++ {
		c := 13 + (k-1)*(10+2*nPt)
		regs[c], regs[c+1], regs[c+9] = uint16(nPt), 1, 0 // ActPt, DeptRef VAR_MAX_PCT, ReadOnly RW
		regs[c+7], regs[c+8] = 0, uint16(5*k)             // RspTms 5k s
		if k == 1 {
			regs[c+9] = 1 // the first curve is read-only
		}
		for j := 1; j <= nPt; j++ {
			o := c + 10 + 2*(j-1)
			regs[o] = uint16(9000 + 100*j + k)
			regs[o+1] = uint16(int16(-100 * j * k))
		}
	}
	return regs
}

func byName(vals []sunspec.Value) map[string]sunspec.Value {
	out := map[string]sunspec.Value{}
	for _, v := range vals {
		out[v.Name] = v
	}
	return out
}

// Model 705: the stored curves (count NCrv) each carry their points
// (count NPt) nested after their own points; every point lands at its
// register, with the scale factors of the model's top level.
func TestNestedGroupsVoltVar(t *testing.T) {
	regs := voltVar(3, 4)
	inst := sunspec.Instance{ID: 705, L: len(regs), Def: sunspec.Lookup(705), Name: "derVoltVar"}
	got := byName(sunspec.Decode(inst, regs))
	for k := 1; k <= 3; k++ {
		c := 13 + (k-1)*18
		if v := got[fmt.Sprintf("%d.RspTms", k)]; v.V != float64(5*k) || v.Offset != c+7 || v.Path != fmt.Sprintf("Crv.%d.RspTms", k) {
			t.Errorf("curve %d RspTms %+v", k, v)
		}
		for j := 1; j <= 4; j++ {
			name := fmt.Sprintf("%d.Pt.%d.V", k, j)
			v := got[name]
			want := math.Round((90+float64(j)+float64(k)/100)*100) / 100
			if v.V != want || v.Offset != c+10+2*(j-1) || v.Path != fmt.Sprintf("Crv.%d.Pt.%d.V", k, j) {
				t.Errorf("%s = %+v, want %v at %d", name, v, want, c+10+2*(j-1))
			}
			if v := got[fmt.Sprintf("%d.Pt.%d.Var", k, j)]; v.V != float64(-10*j*k) {
				t.Errorf("curve %d point %d Var = %v", k, j, v.V)
			}
		}
	}
	if got["1.ReadOnly"].V != "R" || got["2.ReadOnly"].V != "RW" || got["2.DeptRef"].V != "VAR_MAX_PCT" || got["NCrv"].V != 3.0 {
		t.Errorf("curve fields %v %v %v", got["1.ReadOnly"].V, got["2.ReadOnly"].V, got["2.DeptRef"].V)
	}
	// 11 top-level points, 3 curves × (9 points + 4 × 2), less the three
	// scale factors and the not-implemented ones (RvrtTms, RvrtRem,
	// RvrtCrv; Pri, VRef, VRefAuto, VRefAutoEna, VRefAutoTms per curve).
	if n := len(got); n != 11-3-3+3*(9-5+8) {
		t.Errorf("%d values", n)
	}
	// Locate finds a point by name or path; past the curves there is none.
	for _, name := range []string{"Crv.3.Pt.4.Var", "3.Pt.4.Var"} {
		if l, ok := sunspec.Locate(inst, regs, name); !ok || l.Offset != 13+2*18+10+7 {
			t.Errorf("Locate(%s) = %+v %v", name, l, ok)
		}
	}
	if _, ok := sunspec.Locate(inst, regs, "Crv.4.ActPt"); ok {
		t.Error("a fourth curve was placed")
	}
}

// Model 707: depth 3, curve sets with three curves that occur once, each
// with its own nested points; a curve after a variable-length one starts
// where that one ends.
func TestNestedGroupsTrip(t *testing.T) {
	const nPt, nSet = 3, 2
	crvLen := 1 + 3*(1+3*nPt)
	regs := filled(7+nSet*crvLen, 0xFFFF)
	regs[3], regs[4], regs[5], regs[6] = nPt, nSet, 0xFFFF, 0xFFFE // NPt, NCrvSet, V_SF -1, Tms_SF -2
	for s := 1; s <= nSet; s++ {
		c := 7 + (s-1)*crvLen
		regs[c] = 0 // ReadOnly RW
		for kind := 0; kind < 3; kind++ {
			g := c + 1 + kind*(1+3*nPt)
			regs[g] = uint16(kind + 1) // ActPt
			for j := 1; j <= nPt; j++ {
				o := g + 1 + 3*(j-1)
				regs[o] = uint16(1000*s + 100*kind + j)       // V
				regs[o+1], regs[o+2] = 0, uint16(50*j+kind*7) // Tms
			}
		}
	}
	inst := sunspec.Instance{ID: 707, L: len(regs), Def: sunspec.Lookup(707)}
	got := byName(sunspec.Decode(inst, regs))
	for s := 1; s <= nSet; s++ {
		for kind, name := range []string{"MustTrip", "MayTrip", "MomCess"} {
			if v := got[fmt.Sprintf("%d.%s.ActPt", s, name)]; v.V != float64(kind+1) {
				t.Errorf("set %d %s ActPt %v", s, name, v.V)
			}
			for j := 1; j <= nPt; j++ {
				v := got[fmt.Sprintf("%d.%s.Pt.%d.V", s, name, j)]
				wantV := float64(1000*s+100*kind+j) / 10
				tms := got[fmt.Sprintf("%d.%s.Pt.%d.Tms", s, name, j)]
				if v.V != wantV || tms.V != float64(50*j+kind*7)/100 || v.Path != fmt.Sprintf("Crv.%d.%s.Pt.%d.V", s, name, j) {
					t.Errorf("set %d %s point %d: V %v (want %v) Tms %v path %s", s, name, j, v.V, wantV, tms.V, v.Path)
				}
			}
		}
	}
}

// Every model with nested groups lays out to the length its counts give,
// and a huge or not-implemented count places nothing past the registers.
func TestNestedGroupsBounded(t *testing.T) {
	regs := voltVar(2, 2)
	regs[3], regs[4] = 0xFFFE, 0xFFFE // NPt and NCrv claim far more than L holds
	inst := sunspec.Instance{ID: 705, L: len(regs), Def: sunspec.Lookup(705)}
	for _, l := range sunspec.Layout(inst, regs) {
		if l.Offset+l.Point.Size > len(regs) {
			t.Fatalf("%s placed at %d past %d registers", l.Path, l.Offset, len(regs))
		}
	}
	regs[4] = 0xFFFF // NCrv not implemented: no curves
	for _, v := range sunspec.Decode(inst, regs) {
		if strings.Contains(v.Path, "Crv.") {
			t.Fatalf("curve decoded with NCrv not implemented: %s", v.Path)
		}
	}
	// Without registers, only the fixed part is placed.
	if n := len(sunspec.Layout(inst, nil)); n != 11 {
		t.Errorf("layout without registers: %d points", n)
	}
}

// Writes of points of repeating and nested groups: by name or path, with
// the registers that give the counts; scale factors applied by
// PlanWrite; read back and Kept.
func TestWriteNestedPoints(t *testing.T) {
	regs := voltVar(3, 4)
	inst := sunspec.Instance{ID: 705, Addr: 1000, L: len(regs), Def: sunspec.Lookup(705)}
	w, err := sunspec.PlanWrite(inst, regs, "Crv.2.Pt.3.V", 101.5)
	if err != nil {
		t.Fatal(err)
	}
	addr, words, err := w.Encode()
	if err != nil || addr != 1000+13+18+10+4 || !slices.Equal(words, []uint16{10150}) {
		t.Errorf("Encode %d %v %v", addr, words, err)
	}
	w, err = sunspec.PlanWrite(inst, regs, "2.Pt.1.Var", -44.4)
	if err != nil {
		t.Fatal(err)
	}
	if addr, words, _ := w.Encode(); addr != 1000+13+18+11 || words[0] != uint16(0x10000-444) {
		t.Errorf("Var %d %v", addr, words)
	}
	// Without the registers the curve cannot be placed.
	if _, _, err := (sunspec.Write{Model: inst, Point: "Crv.2.Pt.3.V", Raw: 1}).Encode(); err == nil || !strings.Contains(err.Error(), "without its registers") {
		t.Errorf("no registers: %v", err)
	}
	// Enums by symbol, read-only points refused, range checked.
	if w, err := sunspec.PlanWrite(inst, regs, "Crv.3.DeptRef", "VAR_AVAL_PCT"); err != nil || w.Raw != 2 {
		t.Errorf("enum %+v %v", w, err)
	}
	if _, err := sunspec.PlanWrite(inst, regs, "Crv.3.DeptRef", "NOPE"); err == nil {
		t.Error("unknown symbol written")
	}
	if _, err := sunspec.PlanWrite(inst, regs, "Crv.2.ReadOnly", 0); err == nil || !strings.Contains(err.Error(), "read-only") {
		t.Errorf("ReadOnly: %v", err)
	}
	if _, err := sunspec.PlanWrite(inst, regs, "Crv.2.Pt.1.V", 700.0); err == nil || !strings.Contains(err.Error(), "does not fit") {
		t.Errorf("700 %% at SF -2: %v", err)
	}
	if w, err := sunspec.PlanWrite(inst, regs, "Crv.2.RspTms", 70000); err != nil || w.Raw != 70000 {
		t.Errorf("uint32 %+v %v", w, err)
	}
	// Read back after the device took it.
	w, _ = sunspec.PlanWrite(inst, regs, "Crv.2.Pt.3.V", 101.5)
	after := slices.Clone(regs)
	after[13+18+10+4] = 10150
	if got, ok := w.ReadBack(after); !ok || got != 10150 || !w.Kept(after) || w.Kept(regs) {
		t.Errorf("read back %d %v kept %v %v", got, ok, w.Kept(after), w.Kept(regs))
	}
}

// Writes of every other size and type: 64-bit integers, floats, strings,
// addresses and bitfields; a scale factor that is not implemented stops a
// scaled write.
func TestWriteTypes(t *testing.T) {
	def := &sunspec.Model{ID: 64020, Points: []sunspec.Point{
		{Name: "E", Offset: 0, Type: "acc64", Size: 4, RW: true},
		{Name: "S", Offset: 4, Type: "int64", Size: 4, RW: true},
		{Name: "F", Offset: 8, Type: "float32", Size: 2, RW: true},
		{Name: "D", Offset: 10, Type: "float64", Size: 4, RW: true},
		{Name: "Name", Offset: 14, Type: "string", Size: 4, RW: true},
		{Name: "IP", Offset: 18, Type: "ipaddr", Size: 2, RW: true},
		{Name: "IP6", Offset: 20, Type: "ipv6addr", Size: 8, RW: true},
		{Name: "MAC", Offset: 28, Type: "eui48", Size: 4, RW: true},
		{Name: "Bits", Offset: 32, Type: "bitfield32", Size: 2, RW: true, Symbols: map[string]int64{"A": 0, "B": 17}},
		{Name: "W", Offset: 34, Type: "int16", Size: 1, RW: true, SF: "W_SF"},
		{Name: "W_SF", Offset: 35, Type: "sunssf", Size: 1},
	}, Len: 36}
	inst := sunspec.Instance{ID: 64020, Addr: 100, L: 36, Def: def}
	regs := make([]uint16, 36)
	regs[35] = 2 // W_SF 2
	for _, c := range []struct {
		name  string
		v     any
		words []uint16
	}{
		{"E", 1 << 40, []uint16{0, 0x100, 0, 0}},
		{"S", -2, []uint16{0xFFFF, 0xFFFF, 0xFFFF, 0xFFFE}},
		{"F", 21.5, []uint16{0x41AC, 0}},
		{"D", -0.25, []uint16{0xBFD0, 0, 0, 0}},
		{"Name", "PV1", []uint16{0x5056, 0x3100, 0, 0}},
		{"IP", "192.168.1.2", []uint16{0xC0A8, 0x0102}},
		{"IP6", "2001:db8::1", []uint16{0x2001, 0x0db8, 0, 0, 0, 0, 0, 1}},
		{"MAC", "00:11:22:33:44:55", []uint16{0, 0x0011, 0x2233, 0x4455}},
		{"Bits", []string{"A", "B"}, []uint16{2, 1}},
		{"W", -1200, []uint16{0xFFF4}},
	} {
		w, err := sunspec.PlanWrite(inst, regs, c.name, c.v)
		if err != nil {
			t.Errorf("%s: %v", c.name, err)
			continue
		}
		addr, words, err := w.Encode()
		if err != nil || !slices.Equal(words, c.words) {
			t.Errorf("%s: %d %x %v, want %x", c.name, addr, words, err, c.words)
		}
		after := slices.Clone(regs)
		l, _ := sunspec.Locate(inst, regs, c.name)
		copy(after[l.Offset:], words)
		if !w.Kept(after) {
			t.Errorf("%s not kept", c.name)
		}
		// What was written decodes to what was planned.
		for _, v := range sunspec.Decode(inst, after) {
			if v.Name == c.name && fmt.Sprint(v.V) != fmt.Sprint(c.v) && c.name != "E" {
				t.Errorf("%s decodes as %v, planned %v", c.name, v.V, c.v)
			}
		}
	}
	if _, err := sunspec.PlanWrite(inst, regs, "Name", "too long a name"); err == nil {
		t.Error("long string written")
	}
	if _, err := sunspec.PlanWrite(inst, regs, "IP", "2001:db8::1"); err == nil {
		t.Error("IPv6 address in an ipaddr point")
	}
	regs[35] = 0x8000
	if _, err := sunspec.PlanWrite(inst, regs, "W", 100); err == nil || !strings.Contains(err.Error(), "scale factor") {
		t.Errorf("scale factor not implemented: %v", err)
	}
	regs[35] = 11
	if _, err := sunspec.PlanWrite(inst, regs, "W", 100); err == nil {
		t.Error("scale factor out of range")
	}
}

// Volt-var curve planned on model 705: points scaled, ActPt, the other
// points, the adopt request and the enable, in that order; the read-only
// first curve is refused.
func TestPlanCurve(t *testing.T) {
	regs := voltVar(3, 4)
	m := &sunspec.Map{Models: []sunspec.Instance{{ID: 705, Addr: 2000, L: len(regs), Def: sunspec.Lookup(705), Name: "derVoltVar"}}}
	read := func(sunspec.Instance) ([]uint16, error) { return regs, nil }
	on := true
	p, err := sunspec.PlanCurve(m, sunspec.Curve{Model: 705, Points: [][2]float64{{92, 30}, {98, 0}, {102, 0}, {108, -30}},
		Set: map[string]any{"DeptRef": "VAR_MAX_PCT", "RspTms": 10}, Enable: &on}, read)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, w := range p.Writes {
		addr, words, err := w.Encode()
		if err != nil {
			t.Fatal(err)
		}
		got = append(got, fmt.Sprintf("%s@%d=%v", w.Point, addr-2000, words))
	}
	want := []string{"Crv.2.Pt.1.V@41=[9200]", "Crv.2.Pt.1.Var@42=[300]", "Crv.2.Pt.2.V@43=[9800]", "Crv.2.Pt.2.Var@44=[0]",
		"Crv.2.Pt.3.V@45=[10200]", "Crv.2.Pt.3.Var@46=[0]", "Crv.2.Pt.4.V@47=[10800]", "Crv.2.Pt.4.Var@48=[65236]",
		"Crv.2.ActPt@31=[4]", "Crv.2.DeptRef@32=[1]", "Crv.2.RspTms@38=[0 10]", "AdptCrvReq@1=[2]", "Ena@0=[1]"}
	if !slices.Equal(got, want) {
		t.Errorf("writes\n%v\nwant\n%v", got, want)
	}
	for _, c := range []struct {
		c    sunspec.Curve
		want string
	}{
		{sunspec.Curve{Model: 705, Index: 1, Points: [][2]float64{{100, 0}}}, "read-only"},
		{sunspec.Curve{Model: 705, Index: 4, Points: [][2]float64{{100, 0}}}, "no stored curve 4"},
		{sunspec.Curve{Model: 705, Points: make([][2]float64, 5)}, "at most 4 points"},
		{sunspec.Curve{Model: 704}, "no curves"},
	} {
		if _, err := sunspec.PlanCurve(m, c.c, read); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%+v: %v, want %q", c.c, err, c.want)
		}
	}
	if _, err := sunspec.PlanCurve(m, sunspec.Curve{Model: 706}, read); !errors.Is(err, sunspec.ErrUnsupported) {
		t.Errorf("706 absent: %v", err)
	}

	// Model 711: control sets without points, adopted through AdptCtlReq.
	fd := filled(12+2*10, 0xFFFF)
	fd[3], fd[9], fd[10], fd[11] = 2, 0xFFFE, 0xFFFD, 0 // NCtl, Db_SF -2, K_SF -3, RspTms_SF 0
	fd[12+9], fd[22+9] = 1, 0                           // ReadOnly of each set
	m = &sunspec.Map{Models: []sunspec.Instance{{ID: 711, Addr: 3000, L: len(fd), Def: sunspec.Lookup(711)}}}
	p, err = sunspec.PlanCurve(m, sunspec.Curve{Model: 711, Set: map[string]any{"DbOf": 0.036, "KOf": 0.05}},
		func(sunspec.Instance) ([]uint16, error) { return fd, nil })
	if err != nil {
		t.Fatal(err)
	}
	got = nil
	for _, w := range p.Writes {
		addr, words, _ := w.Encode()
		got = append(got, fmt.Sprintf("%s@%d=%v", w.Point, addr-3000, words))
	}
	if want := []string{"Ctl.2.DbOf@22=[0 4]", "Ctl.2.KOf@26=[50]", "AdptCtlReq@1=[2]"}; !slices.Equal(got, want) {
		t.Errorf("711 writes %v, want %v", got, want)
	}
}

// A vendor model's definition loaded at run time, in the SunSpec JSON
// layout (sizes left out where the type gives them), is used by Walk and
// Decode from then on.
func TestRegisterDefinition(t *testing.T) {
	def, err := sunspec.ParseDefinition([]byte(`{"id": 64950, "group": {"name": "vendor_battery", "label": "Vendor Battery",
	  "points": [{"name": "ID", "type": "uint16"}, {"name": "L", "type": "uint16"},
	    {"name": "N", "type": "uint16"}, {"name": "SoC", "type": "uint16", "sf": "SoC_SF", "units": "Pct"},
	    {"name": "SoC_SF", "type": "sunssf"}, {"name": "Mode", "type": "enum16", "access": "RW", "symbols": [{"name": "IDLE", "value": 0}, {"name": "CHARGE", "value": 1}]}],
	  "groups": [{"name": "cell", "count": "N", "points": [{"name": "V", "type": "uint16", "sf": -3, "units": "V"}]}]}}`))
	if err != nil {
		t.Fatal(err)
	}
	if def.Fixed != 4 || def.Groups[0].CountPoint != "N" || def.Points[3].Offset != 3 {
		t.Fatalf("%+v", def)
	}
	if err := sunspec.Register(def); err != nil {
		t.Fatal(err)
	}
	if sunspec.Lookup(64950) != def || !slices.Contains(sunspec.Known(), 64950) {
		t.Error("not registered")
	}
	r := &memReader{regs: layout(40000, true, model(64950, []uint16{2, 655, 0xFFFF, 1, 3301, 3299}))}
	m, err := sunspec.Find(r)
	if err != nil {
		t.Fatal(err)
	}
	regs, _ := sunspec.ReadModel(r, m.Models[0])
	got := byName(sunspec.Decode(m.Models[0], regs))
	if got["SoC"].V != 65.5 || got["Mode"].V != "CHARGE" || got["2.V"].V != 3.299 || got["SoC"].Unit != "%" {
		t.Errorf("%v %v %v", got["SoC"].V, got["Mode"].V, got["2.V"].V)
	}
	for _, bad := range []string{`{}`, `{"id": 5, "group": {"name": "x", "points": [{"name": "Q", "type": "nope"}]}}`, `not json`} {
		if _, err := sunspec.ParseDefinition([]byte(bad)); err == nil {
			t.Errorf("%s parsed", bad)
		}
	}
	if err := sunspec.Register(nil); err == nil {
		t.Error("nil registered")
	}
}

// EncodePoint lays raw values into registers, refusing what does not fit.
func TestEncodePoint(t *testing.T) {
	for _, c := range []struct {
		p    sunspec.Point
		v    any
		want []uint16
	}{
		{sunspec.Point{Name: "a", Type: "uint16", Size: 1}, 7, []uint16{7}},
		{sunspec.Point{Name: "b", Type: "int32", Size: 2}, int64(-2), []uint16{0xFFFF, 0xFFFE}},
		{sunspec.Point{Name: "c", Type: "uint64", Size: 4}, uint64(1 << 63), []uint16{0x8000, 0, 0, 0}},
		{sunspec.Point{Name: "d", Type: "string", Size: 2}, "AB", []uint16{0x4142, 0}},
		{sunspec.Point{Name: "e", Type: "float32", Size: 2}, 1.0, []uint16{0x3F80, 0}},
	} {
		got, err := sunspec.EncodePoint(&c.p, c.v)
		if err != nil || !slices.Equal(got, c.want) {
			t.Errorf("%s: %x %v, want %x", c.p.Name, got, err, c.want)
		}
	}
	for _, c := range []struct {
		p sunspec.Point
		v any
	}{
		{sunspec.Point{Name: "a", Type: "uint16", Size: 1}, 70000}, {sunspec.Point{Name: "a", Type: "uint16", Size: 1}, 1.5},
		{sunspec.Point{Name: "a", Type: "int16", Size: 1}, "x"}, {sunspec.Point{Name: "a", Type: "pad", Size: 1}, 0},
	} {
		if _, err := sunspec.EncodePoint(&c.p, c.v); err == nil {
			t.Errorf("%+v %v encoded", c.p, c.v)
		}
	}
}

// Model 1 at 65 registers (no pad), as some devices publish it and
// pysunspec2 accepts.
func TestCommonModel65(t *testing.T) {
	common := append(append(append(append(append(str("Maker", 16), str("M", 16)...), str("", 8)...), str("1", 8)...), str("S1", 16)...), 7)
	r := &memReader{regs: layout(40000, true, model(1, common), model(64901, []uint16{1}))}
	m, err := sunspec.Find(r)
	if err != nil || m.Models[0].L != 65 || m.Models[1].ID != 64901 {
		t.Fatalf("%+v %v", m, err)
	}
	id, err := sunspec.ReadIdentity(r, m)
	if err != nil || id.Manufacturer != "Maker" || id.Serial != "S1" || id.DeviceAddress != 7 {
		t.Errorf("%+v %v", id, err)
	}
}

// A map one register off a base (counted from one) is found after the
// usual bases; a device that answered nowhere is not probed further.
func TestFindNeighbours(t *testing.T) {
	for _, base := range []uint16{40001, 39999, 50001, 49999, 1} {
		regs := layout(base, true, model(1, filled(66, 0)))
		r := &memReader{regs: regs, refuse: true}
		m, err := sunspec.Find(refusingReader{r})
		if err != nil || m.Base != base {
			t.Errorf("base %d: %+v %v", base, m, err)
		}
	}
	r := &memReader{regs: map[uint16]uint16{}}
	if _, err := sunspec.Find(r); !errors.Is(err, errSilent) || len(r.reads) != 3 {
		t.Errorf("silent device: %v after %d reads", err, len(r.reads))
	}
	// FindAt tries only what it is given.
	r = &memReader{regs: layout(1000, true, model(1, filled(66, 0))), refuse: true}
	if m, err := sunspec.FindAt(refusingReader{r}, 1000); err != nil || m.Base != 1000 {
		t.Errorf("FindAt: %+v %v", m, err)
	}
	if _, err := sunspec.FindAt(refusingReader{r}, 40000); !errors.Is(err, sunspec.ErrNotSunSpec) {
		t.Errorf("FindAt elsewhere: %v", err)
	}
}

// Scale factor edge cases: out of the -10..10 range (left out, as a
// device fault), a fixed power in the definition, uint64 values past
// int64, and IPv6 addresses.
func TestScaleFactorEdges(t *testing.T) {
	def := &sunspec.Model{ID: 64005, Points: []sunspec.Point{
		{Name: "A", Offset: 0, Type: "uint16", Size: 1, SF: "A_SF"},
		{Name: "A_SF", Offset: 1, Type: "sunssf", Size: 1},
		{Name: "B", Offset: 2, Type: "uint16", Size: 1, SF: float64(30)},
		{Name: "C", Offset: 3, Type: "uint64", Size: 4},
		{Name: "D", Offset: 7, Type: "ipv6addr", Size: 8},
		{Name: "E", Offset: 15, Type: "int16", Size: 1, SF: "A_SF"},
	}}
	regs := []uint16{5, 10, 7, 0xFFFF, 0xFFFF, 0xFFFF, 0xFFFE, 0xFE80, 0, 0, 0, 0, 0, 0, 1, 0xFFFF}
	got := decoded(def, regs)
	if got["A"] != 5e10 || got["E"] != -1e10 || got["C"] != float64(math.MaxUint64-1) || got["D"] != "fe80::1" {
		t.Errorf("%v", got)
	}
	if _, ok := got["B"]; ok {
		t.Error("fixed power 30 applied")
	}
	regs[1] = 11
	if got := decoded(def, regs); got["A"] != nil || got["E"] != nil {
		t.Errorf("scale factor 11 applied: %v", got)
	}
	regs[1] = uint16(0x10000 - 11)
	if got := decoded(def, regs); got["A"] != nil {
		t.Errorf("scale factor -11 applied: %v", got)
	}
}

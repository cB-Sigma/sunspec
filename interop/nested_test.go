// Copyright 2026 Abhinav Gautam
// SPDX-License-Identifier: Apache-2.0

package interop

import (
	"encoding/json"
	"fmt"
	"math"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/cB-Sigma/sunspec"
)

// example gives raw values for pysunspec2 for every point of a model
// (address types left out), two occurrences of every repeating group, and
// the count points set to two.
func example(def *sunspec.Model) map[string]any {
	counts := map[string]bool{}
	var collect func(gs []sunspec.Group)
	collect = func(gs []sunspec.Group) {
		for _, g := range gs {
			if g.CountPoint != "" {
				counts[g.CountPoint] = true
			}
			collect(g.Groups)
		}
	}
	collect(def.Groups)
	n := 0
	var group func(pts []sunspec.Point, gs []sunspec.Group) map[string]any
	group = func(pts []sunspec.Point, gs []sunspec.Group) map[string]any {
		out := map[string]any{}
		for _, p := range pts {
			n++
			if counts[p.Name] {
				out[p.Name] = 2
				continue
			}
			switch {
			case p.Type == "pad" || p.Type == "ipaddr" || p.Type == "ipv6addr" || p.Type == "eui48":
			case p.Type == "sunssf":
				out[p.Name] = -1 - n%2
			case strings.HasPrefix(p.Type, "enum"):
				best := int64(-1)
				for _, v := range p.Symbols {
					if best < 0 || v < best {
						best = v
					}
				}
				if best < 0 {
					best = 1
				}
				out[p.Name] = best
			case strings.HasPrefix(p.Type, "bitfield"):
				out[p.Name] = 1 << (n % 8)
			case p.Type == "string":
				s := fmt.Sprintf("%s%d", p.Name, n)
				if len(s) > 2*p.Size {
					s = s[len(s)-2*p.Size:]
				}
				out[p.Name] = s
			case strings.HasPrefix(p.Type, "float"):
				out[p.Name] = float64(n) + 0.25
			case strings.HasPrefix(p.Type, "int"):
				out[p.Name] = -(n % 300) - 1
			case strings.HasPrefix(p.Type, "acc"):
				out[p.Name] = 1000 + n
			default:
				out[p.Name] = 100 + n%1000
			}
		}
		for _, g := range gs {
			if g.Count == 1 {
				out[g.Name] = group(g.Points, g.Groups)
				continue
			}
			k := 2
			if g.Count > 1 {
				k = g.Count
			}
			occ := make([]map[string]any, k)
			for i := range occ {
				occ[i] = group(g.Points, g.Groups)
			}
			out[g.Name] = occ
		}
		return out
	}
	return group(def.Points, def.Groups)
}

type pyLayout struct {
	Len    int `json:"len"`
	Points map[string]struct {
		O int `json:"o"`
		V any `json:"v"`
	} `json:"points"`
	Error string `json:"error"`
}

// Every SunSpec model, nested groups to depth three included: pysunspec2
// encodes made-up values with its own definitions (two occurrences of
// every repeating group), places every point itself and decodes them
// again; this module, given pysunspec2's registers, places every point at
// the same offset under the same path and decodes the same values.
func TestEveryModelLayoutAgainstPysunspec2(t *testing.T) {
	name := needPeer(t)
	s := spec{Base: 40000, Unit: 1}
	for _, id := range sunspec.Known() {
		s.Models = append(s.Models, model{ID: id, Values: example(sunspec.Lookup(id))})
	}
	js, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command("docker", "exec", name, "python", "-I", "/sigma/pysunspec2_device.py", "--layout", "--spec", string(js)).Output()
	if err != nil {
		t.Fatalf("pysunspec2 layout: %v", err)
	}
	var lay map[string]pyLayout
	if err := json.Unmarshal(out, &lay); err != nil {
		t.Fatal(err)
	}
	out, err = exec.Command("docker", "exec", name, "python", "-I", "/sigma/pysunspec2_device.py", "--dump", "--spec", string(js)).Output()
	if err != nil {
		t.Fatalf("pysunspec2 dump: %v", err)
	}
	var dump struct {
		Errors []string          `json:"errors"`
		Regs   map[string]uint16 `json:"regs"`
	}
	if err := json.Unmarshal(out, &dump); err != nil {
		t.Fatal(err)
	}
	for _, e := range dump.Errors {
		t.Errorf("pysunspec2: %s", e)
	}
	regs := map[uint16]uint16{}
	for k, v := range dump.Regs {
		a, _ := strconv.Atoi(k)
		regs[uint16(a)] = v
	}
	m, err := sunspec.Walk(memReader(regs), 40000)
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Models) != len(sunspec.Known()) {
		t.Fatalf("%d models in pysunspec2's map, want %d", len(m.Models), len(sunspec.Known()))
	}
	points, values, nested := 0, 0, 0
	for _, inst := range m.Models {
		py := lay[fmt.Sprint(inst.ID)]
		if py.Error != "" {
			t.Errorf("model %d: pysunspec2: %s", inst.ID, py.Error)
		}
		if inst.L != py.Len {
			t.Errorf("model %d: L %d, pysunspec2 %d", inst.ID, inst.L, py.Len)
		}
		data, err := sunspec.ReadModel(memReader(regs), inst)
		if err != nil {
			t.Fatal(err)
		}
		ours := map[string]sunspec.Located{}
		for _, l := range sunspec.Layout(inst, data) {
			ours[l.Path] = l
		}
		dec := map[string]sunspec.Value{}
		for _, v := range sunspec.Decode(inst, data) {
			dec[v.Path] = v
		}
		paths := make([]string, 0, len(py.Points))
		for p := range py.Points {
			paths = append(paths, p)
		}
		sort.Strings(paths)
		for _, path := range paths {
			pp := py.Points[path]
			l, ok := ours[path]
			if !ok {
				t.Errorf("model %d: %s placed by pysunspec2 at %d, not by us", inst.ID, path, pp.O)
				continue
			}
			points++
			if strings.Count(path, ".") >= 3 {
				nested++
			}
			if l.Offset != pp.O {
				t.Errorf("model %d: %s at %d, pysunspec2 %d", inst.ID, path, l.Offset, pp.O)
			}
			v, decoded := dec[path]
			if l.Point.Type == "sunssf" || l.Point.Type == "pad" {
				continue
			}
			switch {
			case pp.V == nil && decoded:
				t.Errorf("model %d: %s decoded as %v, pysunspec2 says not implemented", inst.ID, path, v.V)
			case pp.V != nil && !decoded:
				t.Errorf("model %d: %s not decoded, pysunspec2 %v", inst.ID, path, pp.V)
			case pp.V != nil && !agree(l.Point, v, pp.V):
				t.Errorf("model %d: %s = %v (raw %d), pysunspec2 %v", inst.ID, path, v.V, v.Raw, pp.V)
			case pp.V != nil:
				values++
			}
		}
		for path := range ours {
			if _, ok := py.Points[path]; !ok {
				t.Errorf("model %d: we place %s, pysunspec2 does not", inst.ID, path)
			}
		}
	}
	t.Logf("%d models: %d points placed alike (%d in nested groups), %d values decoded alike", len(m.Models), points, nested, values)
}

func agree(p *sunspec.Point, v sunspec.Value, py any) bool {
	switch {
	case strings.HasPrefix(p.Type, "enum"), strings.HasPrefix(p.Type, "bitfield"):
		f, ok := py.(float64)
		return ok && int64(f) == v.Raw
	}
	if f, ok := py.(float64); ok {
		g, ok := v.V.(float64)
		return ok && math.Abs(g-f) <= 1e-6*math.Max(1, math.Abs(f))
	}
	return fmt.Sprint(v.V) == fmt.Sprint(py)
}

type memReader map[uint16]uint16

func (m memReader) ReadHolding(addr, count uint16) ([]uint16, error) {
	out := make([]uint16, count)
	for i := range out {
		v, ok := m[addr+uint16(i)]
		if !ok {
			return nil, fmt.Errorf("no register %d", addr+uint16(i))
		}
		out[i] = v
	}
	return out, nil
}

// Curves written to pysunspec2-built devices over Modbus TCP: a volt-var
// curve (705), a must-trip curve of a trip curve set (707) and a watt-var
// curve (712), planned by PlanCurve; every write lands on a point
// pysunspec2's definitions mark writable and is kept.
func TestCurvesToPysunspec2Device(t *testing.T) {
	crv := func(ro int, pts ...map[string]any) map[string]any {
		return map[string]any{"ActPt": len(pts), "ReadOnly": ro, "Pt": pts}
	}
	url := device(t, spec{Base: 40000, Unit: 1, Models: []model{
		{ID: 1, Values: map[string]any{"Mn": "Example Solar", "Md": "ES-10K-3P", "SN": "CRV1"}},
		{ID: 705, Values: map[string]any{"Ena": 0, "AdptCrvReq": 0, "AdptCrvRslt": 1, "NPt": 4, "NCrv": 3, "V_SF": -2, "DeptRef_SF": -1, "RspTms_SF": 0,
			"Crv": []map[string]any{
				crv(1, map[string]any{"V": 9200, "Var": 300}, map[string]any{"V": 9800, "Var": 0}, map[string]any{"V": 10200, "Var": 0}, map[string]any{"V": 10800, "Var": -300}),
				crv(0, map[string]any{}, map[string]any{}, map[string]any{}, map[string]any{}),
				crv(0, map[string]any{}, map[string]any{}, map[string]any{}, map[string]any{}),
			}}},
		{ID: 707, Values: map[string]any{"Ena": 0, "NPt": 3, "NCrvSet": 2, "V_SF": -1, "Tms_SF": -2, "Crv": []map[string]any{
			{"ReadOnly": 1, "MustTrip": map[string]any{"ActPt": 1, "Pt": []map[string]any{{"V": 500, "Tms": 16}, {}, {}}},
				"MayTrip": map[string]any{"ActPt": 0, "Pt": []map[string]any{{}, {}, {}}}, "MomCess": map[string]any{"ActPt": 0, "Pt": []map[string]any{{}, {}, {}}}},
			{"ReadOnly": 0, "MustTrip": map[string]any{"ActPt": 0, "Pt": []map[string]any{{}, {}, {}}},
				"MayTrip": map[string]any{"ActPt": 0, "Pt": []map[string]any{{}, {}, {}}}, "MomCess": map[string]any{"ActPt": 0, "Pt": []map[string]any{{}, {}, {}}}},
		}}},
		{ID: 712, Values: map[string]any{"Ena": 0, "NPt": 2, "NCrv": 2, "W_SF": 0, "DeptRef_SF": 0, "Crv": []map[string]any{
			crv(1, map[string]any{"W": 0, "Var": 0}, map[string]any{"W": 100, "Var": -40}), crv(0, map[string]any{}, map[string]any{})}}},
	}})
	r := dial(t, url, 1)
	m, got := readAll(t, r)
	expect(t, got, map[string]any{
		"derVoltVar.1.Pt.1.V": 92.0, "derVoltVar.1.Pt.4.Var": -30.0, "derVoltVar.1.ReadOnly": "R", "derVoltVar.NCrv": 3.0,
		"derTripLV.1.MustTrip.Pt.1.V": 50.0, "derTripLV.1.MustTrip.Pt.1.Tms": 0.16, "derTripLV.NCrvSet": 2.0,
		"derWattVar.1.Pt.2.W": 100.0, "derWattVar.1.Pt.2.Var": -40.0,
	}, "derVoltVar.2.Pt.1.V", "derTripLV.2.MayTrip.Pt.3.Tms", "derWattVar.2.Pt.1.W")
	on := true
	for _, c := range []sunspec.Curve{
		{Model: 705, Points: [][2]float64{{93, 25}, {99, 0}, {101, 0}, {107, -25}}, Set: map[string]any{"DeptRef": "VAR_MAX_PCT", "RspTms": 10}, Enable: &on},
		{Model: 707, Kind: "MustTrip", Points: [][2]float64{{45, 0.16}, {60, 2}}, Enable: &on},
		{Model: 712, Index: 2, Points: [][2]float64{{20, 0}, {100, -44}}},
	} {
		p, err := sunspec.PlanCurve(m, c, func(i sunspec.Instance) ([]uint16, error) { return sunspec.ReadModel(r, i) })
		if err != nil {
			t.Fatalf("%+v: %v", c, err)
		}
		for _, w := range p.Writes {
			addr, words, err := w.Encode()
			if err != nil {
				t.Fatal(err)
			}
			if err := r.WriteHolding(addr, words); err != nil {
				t.Fatalf("model %d: writing %s at %d: %v", c.Model, w.Point, addr, err)
			}
		}
		after, err := sunspec.ReadModel(r, *m.Model(c.Model))
		if err != nil {
			t.Fatal(err)
		}
		for _, w := range p.Writes {
			if !w.Kept(after) {
				t.Errorf("model %d: %s not kept", c.Model, w.Point)
			}
		}
		t.Logf("model %d: %d writes kept", c.Model, len(p.Writes))
	}
	_, got = readAll(t, r)
	expect(t, got, map[string]any{
		"derVoltVar.2.Pt.1.V": 93.0, "derVoltVar.2.Pt.4.Var": -25.0, "derVoltVar.2.ActPt": 4.0, "derVoltVar.2.RspTms": 10.0,
		"derVoltVar.AdptCrvReq": 2.0, "derVoltVar.Ena": "ENABLED",
		"derTripLV.2.MustTrip.Pt.2.V": 60.0, "derTripLV.2.MustTrip.Pt.2.Tms": 2.0, "derTripLV.2.MustTrip.ActPt": 2.0,
		"derWattVar.2.Pt.2.Var": -44.0, "derWattVar.AdptCrvReq": 2.0,
	})
	// The first curve is read-only in pysunspec2's definitions too.
	l, _ := sunspec.Locate(*m.Model(705), nil, "NPt")
	if err := r.WriteHolding(m.Model(705).Addr+uint16(l.Offset), []uint16{9}); !r.Refused(err) {
		t.Errorf("write to NPt: %v, want exception 2", err)
	}
}

// Copyright 2026 Abhinav Gautam
// SPDX-License-Identifier: Apache-2.0

package device_test

import (
	"fmt"
	"slices"
	"testing"

	"github.com/cB-Sigma/sunspec"
	"github.com/cB-Sigma/sunspec/device"
)

// Build lays out model 705's curves with their nested points; the reader
// places every value where Build put it, and the curve points are
// writable.
func TestBuildNested(t *testing.T) {
	data, writable, err := device.Build(705, map[string]any{"Ena": 1, "V_SF": -1, "DeptRef_SF": 0, "RspTms_SF": 0, "Crv": []map[string]any{
		{"ActPt": 2, "ReadOnly": 1, "Pt": []map[string]any{{"V": 950, "Var": 30}, {"V": 1050, "Var": -30}}},
		{"ActPt": 3, "ReadOnly": 0, "Pt": []map[string]any{{"V": 920, "Var": 40}, {"V": 1000, "Var": 0}, {"V": 1080, "Var": -40}}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if want := 13 + 2*(10+3*2); len(data) != want {
		t.Fatalf("%d registers, want %d", len(data), want)
	}
	inst := sunspec.Instance{ID: 705, L: len(data), Def: sunspec.Lookup(705)}
	got := map[string]any{}
	for _, v := range sunspec.Decode(inst, data) {
		got[v.Path] = v.V
	}
	for k, want := range map[string]any{"NPt": 3.0, "NCrv": 2.0, "Ena": "ENABLED", "Crv.1.ReadOnly": "R", "Crv.1.Pt.2.V": 105.0,
		"Crv.1.Pt.2.Var": -30.0, "Crv.2.Pt.3.V": 108.0, "Crv.2.ActPt": 3.0} {
		if fmt.Sprint(got[k]) != fmt.Sprint(want) {
			t.Errorf("%s = %v, want %v", k, got[k], want)
		}
	}
	for _, path := range []string{"Crv.2.Pt.1.V", "Crv.2.Pt.3.Var", "AdptCrvReq", "Ena"} {
		l, _ := sunspec.Locate(inst, data, path)
		if !slices.Contains(writable, l.Offset) {
			t.Errorf("%s at %d is not writable", path, l.Offset)
		}
	}
	for _, path := range []string{"NPt", "AdptCrvRslt"} {
		l, _ := sunspec.Locate(inst, data, path)
		if slices.Contains(writable, l.Offset) {
			t.Errorf("%s at %d is writable", path, l.Offset)
		}
	}
	if _, _, err := device.Build(64900, nil); err == nil {
		t.Error("undescribed model built")
	}
	if _, _, err := device.Build(705, map[string]any{"Ena": "x"}); err == nil {
		t.Error("a bad enum built")
	}
}

// Every described model in one image, walked straight from the image:
// the chain and every layout end where the model's length says.
func TestAllModelsAndNotImplemented(t *testing.T) {
	im, at, err := device.AllModels(40000)
	if err != nil {
		t.Fatal(err)
	}
	m, err := sunspec.Find(im)
	if err != nil || len(m.Models) != len(sunspec.Known()) {
		t.Fatalf("%v: %d models", err, len(m.Models))
	}
	for _, inst := range m.Models {
		if inst.Addr != at[inst.ID] {
			t.Errorf("model %d at %d, want %d", inst.ID, inst.Addr, at[inst.ID])
		}
		regs, err := sunspec.ReadModel(im, inst)
		if err != nil {
			t.Fatal(err)
		}
		end := 0
		for _, l := range sunspec.Layout(inst, regs) {
			end = max(end, l.Offset+l.Point.Size)
		}
		if end != inst.L || len(sunspec.Decode(inst, regs)) == 0 {
			t.Errorf("model %d: layout ends at %d of %d", inst.ID, end, inst.L)
		}
	}
	for _, id := range sunspec.Known() {
		def := sunspec.Lookup(id)
		data := device.NotImplemented(id)
		for _, v := range sunspec.Decode(sunspec.Instance{ID: id, L: len(data), Def: def}, data) {
			if v.V != 0.0 {
				t.Errorf("model %d: %s = %v in a not-implemented model", id, v.Name, v.V)
			}
		}
	}
	if device.NotImplemented(64900) != nil || device.Example(64900) != nil {
		t.Error("an undescribed model")
	}
}

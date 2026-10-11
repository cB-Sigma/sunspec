// Copyright 2026 Abhinav Gautam
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/cB-Sigma/sunspec"
)

// A made-up definition in the source layout, with a curve group counted
// by a point and points nested in it, comes out with offsets counted
// after ID and L, static group places and lengths, and counts.
func TestConvert(t *testing.T) {
	src := `{"id": 64999, "group": {"name": "test", "label": "Test ", "points": [
	  {"name": "ID", "type": "uint16", "size": 1}, {"name": "L", "type": "uint16", "size": 1},
	  {"name": "N", "type": "uint16", "size": 1, "mandatory": "M"},
	  {"name": "W", "type": "int32", "size": 2, "sf": "W_SF", "units": "W", "access": "RW", "label": "Watts"},
	  {"name": "W_SF", "type": "sunssf", "size": 1, "static": "S"}],
	 "groups": [{"name": "Crv", "count": "N", "points": [{"name": "A", "type": "uint16", "size": 1}],
	   "groups": [{"name": "Once", "points": [{"name": "B", "type": "enum16", "size": 1, "symbols": [{"name": "X", "value": 3}]}]},
	              {"name": "Pt", "count": "N", "points": [{"name": "V", "type": "uint16", "size": 1}]},
	              {"name": "After", "points": [{"name": "C", "type": "uint16", "size": 1}]}]}]}}`
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "model_64999.json"), []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(dir, "models.json")
	if err := run(dir, "abc", out); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Models []sunspec.Model `json:"models"`
	}
	if err := json.Unmarshal(b, &doc); err != nil {
		t.Fatal(err)
	}
	m := doc.Models[0]
	if m.ID != 64999 || m.Label != "Test" || m.Fixed != 4 || m.Len != 0 || len(m.Points) != 3 || m.Points[1].Offset != 1 || m.Points[1].SF != "W_SF" || !m.Points[1].RW {
		t.Fatalf("model %+v", m)
	}
	g := m.Groups[0]
	if g.CountPoint != "N" || g.Offset != 4 || g.Len != 3 || len(g.Groups) != 3 {
		t.Fatalf("group %+v", g)
	}
	once, pt, after := g.Groups[0], g.Groups[1], g.Groups[2]
	if once.Count != 1 || once.Offset != 1 || once.Points[0].Symbols["X"] != 3 || pt.CountPoint != "N" || pt.Offset != 2 || after.Offset != 2 || after.Count != 1 {
		t.Errorf("nested %+v %+v %+v", once, pt, after)
	}
	if err := run("", "", ""); err == nil {
		t.Error("no arguments")
	}
}

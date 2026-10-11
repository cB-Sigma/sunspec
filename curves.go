// Copyright 2026 Abhinav Gautam
// SPDX-License-Identifier: Apache-2.0

package sunspec

import (
	"fmt"
	"slices"
	"sort"
)

// Curves of the IEEE 1547-2018 models (SunSpec 705 volt-var, 706
// volt-watt, 707 to 710 voltage and frequency trip, 711 frequency droop,
// 712 watt-var): each model keeps several stored curves (control sets for
// 711) in a repeating group, each with its points in a nested repeating
// group. The first stored curve holds the settings in force and is read
// only; a curve is written to another, then adopted by writing its index
// to the model's adopt request point, and the device reports the outcome
// in the adopt result point (AdptCrvRslt: IN_PROGRESS, COMPLETED or
// FAILED).

// Curve is a curve to write and adopt.
type Curve struct {
	// Model is 705, 706, 707, 708, 709, 710, 711 or 712.
	Model int `json:"model"`
	// Index is the stored curve to write, counting from 1; 0 means 2,
	// the first writable one.
	Index int `json:"index,omitempty"`
	// Kind picks the curve of a trip model's curve set (707 to 710):
	// MustTrip (the default), MayTrip or MomCess.
	Kind string `json:"kind,omitempty"`
	// Points are the curve's points in engineering units, in order: x
	// then y as the model defines them (V and Var in % for 705, V and W
	// for 706, W and Var for 712, V or Hz and Tms in seconds for the
	// trip curves). The active point count (ActPt) is set to their
	// number. Model 711 has no points.
	Points [][2]float64 `json:"points,omitempty"`
	// Set gives other points of the curve, by name, in engineering units
	// or symbol names: {"DeptRef": "VAR_MAX_PCT", "RspTms": 10}.
	Set map[string]any `json:"set,omitempty"`
	// Enable, when set, writes the model's Ena after the adopt request:
	// true turns the function on, false off.
	Enable *bool `json:"enable,omitempty"`
}

// CurveModels lists the models PlanCurve writes.
var CurveModels = []int{705, 706, 707, 708, 709, 710, 711, 712}

// PlanCurve plans the writes of a curve on the device's model: the
// points, the active point count, the other points given, the adopt
// request and the enable. regs reads the model's registers (for the
// curve counts, the scale factors and the read-only flag). A device
// without the model gets ErrUnsupported.
func PlanCurve(m *Map, c Curve, regs func(Instance) ([]uint16, error)) (Plan, error) {
	if !slices.Contains(CurveModels, c.Model) {
		return Plan{}, fmt.Errorf("model %d has no curves PlanCurve writes (one of %v)", c.Model, CurveModels)
	}
	inst := m.Model(c.Model)
	if inst == nil {
		return Plan{}, ErrUnsupported
	}
	if inst.Def == nil || len(inst.Def.Groups) == 0 {
		return Plan{}, fmt.Errorf("model %d is not described", c.Model)
	}
	r, err := regs(*inst)
	if err != nil {
		return Plan{}, err
	}
	crv := &inst.Def.Groups[0]
	idx := c.Index
	if idx == 0 {
		idx = 2
	}
	prefix := fmt.Sprintf("%s.%d.", crv.Name, idx)
	if _, ok := Locate(*inst, r, prefix+"ReadOnly"); !ok {
		return Plan{}, fmt.Errorf("model %d has no stored curve %d", c.Model, idx)
	}
	if v, ok := pointRaw(*inst, r, prefix+"ReadOnly"); ok && v == 1 {
		return Plan{}, fmt.Errorf("stored curve %d of model %d is read-only", idx, c.Model)
	}
	group := crv
	if c.Kind != "" || len(crv.Groups) > 1 {
		kind := c.Kind
		if kind == "" {
			kind = "MustTrip"
		}
		group = nil
		for i := range crv.Groups {
			if crv.Groups[i].Name == kind && crv.Groups[i].Count == 1 {
				group = &crv.Groups[i]
			}
		}
		if group == nil {
			return Plan{}, fmt.Errorf("model %d has no curve kind %q", c.Model, kind)
		}
		prefix += kind + "."
	}
	var pt *Group
	for i := range group.Groups {
		if group.Groups[i].Name == "Pt" {
			pt = &group.Groups[i]
		}
	}
	p := Plan{Model: c.Model, Note: "after the writes, the model's adopt result tells whether the device took the curve"}
	add := func(name string, v any) error {
		w, err := PlanWrite(*inst, r, name, v)
		if err != nil {
			return err
		}
		p.Writes = append(p.Writes, w)
		return nil
	}
	if len(c.Points) > 0 {
		if pt == nil || len(pt.Points) < 2 {
			return Plan{}, fmt.Errorf("model %d's curves have no points", c.Model)
		}
		x, y := pt.Points[0].Name, pt.Points[1].Name
		for j, xy := range c.Points {
			name := fmt.Sprintf("%sPt.%d.", prefix, j+1)
			if _, ok := Locate(*inst, r, name+x); !ok {
				return Plan{}, fmt.Errorf("model %d's curves take at most %d points, not %d", c.Model, j, len(c.Points))
			}
			if err := add(name+x, xy[0]); err != nil {
				return Plan{}, err
			}
			if err := add(name+y, xy[1]); err != nil {
				return Plan{}, err
			}
		}
		if _, ok := Locate(*inst, r, prefix+"ActPt"); ok {
			if err := add(prefix+"ActPt", len(c.Points)); err != nil {
				return Plan{}, err
			}
		}
	}
	keys := make([]string, 0, len(c.Set))
	for k := range c.Set {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if err := add(prefix+k, c.Set[k]); err != nil {
			return Plan{}, err
		}
	}
	adopt := "AdptCrvReq"
	if inst.Def.point(adopt) == nil {
		adopt = "AdptCtlReq"
	}
	if err := add(adopt, idx); err != nil {
		return Plan{}, err
	}
	if c.Enable != nil {
		on := "DISABLED"
		if *c.Enable {
			on = "ENABLED"
		}
		if err := add("Ena", on); err != nil {
			return Plan{}, err
		}
	}
	return p, nil
}

// pointRaw reads a point's raw value; ok is false when it is not
// implemented or not there.
func pointRaw(inst Instance, regs []uint16, name string) (int64, bool) {
	l, ok := Locate(inst, regs, name)
	if !ok || l.Offset+l.Point.Size > len(regs) {
		return 0, false
	}
	return Raw(l.Point.Type, regs[l.Offset:l.Offset+l.Point.Size])
}

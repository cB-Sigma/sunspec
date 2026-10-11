package sunspec

import (
	"errors"
	"fmt"
	"math"
	"slices"
)

// DER controls: the writable points that switch an inverter on or off,
// cap its output and fix its power factor. Each action is planned on the
// models the device has, preferring the IEEE 1547-2018 models (7xx) over
// the older ones (123), and is read back after it is written: the
// outcome says whether the device kept the values (confirmed), which
// shows the setpoint was taken, not that the plant's output has followed.

// The control actions, the values of Control.Action.
const (
	// ActionConnect connects the device to the grid (starts it).
	ActionConnect = "connect"
	// ActionDisconnect disconnects it (stops it).
	ActionDisconnect = "disconnect"
	// ActionLimitPower caps the output at Control.Percent of the rating.
	ActionLimitPower = "limitPower"
	// ActionClearPowerLimit lifts the cap.
	ActionClearPowerLimit = "clearPowerLimit"
	// ActionSetPowerFactor fixes the power factor (Control.PowerFactor).
	ActionSetPowerFactor = "setPowerFactor"
	// ActionClearPowerFactor ends the fixed power factor.
	ActionClearPowerFactor = "clearPowerFactor"
)

// Actions lists the control actions.
var Actions = []string{ActionConnect, ActionDisconnect, ActionLimitPower, ActionClearPowerLimit, ActionSetPowerFactor, ActionClearPowerFactor}

// Control is a control command's content.
type Control struct {
	// Action is one of Actions.
	Action string `json:"action"`
	// Percent is the output limit in percent of the rated power
	// (limitPower).
	Percent *float64 `json:"percent,omitempty"`
	// PowerFactor is the displacement power factor (setPowerFactor):
	// 0 < pf ≤ 1 for model 704, with Excitation; for model 123 a signed
	// value in the device's own sign convention.
	PowerFactor *float64 `json:"powerFactor,omitempty"`
	// Excitation is over (default) or under (model 704).
	Excitation string `json:"excitation,omitempty"`
}

// Check validates the content without I/O.
func (c Control) Check() error {
	switch c.Action {
	case ActionConnect, ActionDisconnect, ActionClearPowerLimit, ActionClearPowerFactor:
		return nil
	case ActionLimitPower:
		if c.Percent == nil || *c.Percent < 0 || *c.Percent > 100 {
			return errors.New("limitPower needs percent from 0 to 100")
		}
		return nil
	case ActionSetPowerFactor:
		if c.PowerFactor == nil || *c.PowerFactor == 0 || math.Abs(*c.PowerFactor) > 1 {
			return errors.New("setPowerFactor needs powerFactor from -1 to 1, not 0")
		}
		switch c.Excitation {
		case "", "over", "under":
		default:
			return fmt.Errorf("excitation %q is not over or under", c.Excitation)
		}
		return nil
	}
	return fmt.Errorf("unknown SunSpec action %q (one of %v)", c.Action, Actions)
}

// IsControl reports whether a decoded JSON command content (a
// map[string]any) is a SunSpec control: its "action" is one of Actions.
func IsControl(content any) bool {
	m, ok := content.(map[string]any)
	if !ok {
		return false
	}
	a, _ := m["action"].(string)
	return slices.Contains(Actions, a)
}

// Write is one point to write: its model instance, the point and the
// raw value.
type Write struct {
	// Model is the instance the point belongs to.
	Model Instance
	// Point is a top-level point name, or "<group>.<point>" for a group
	// that occurs once ("PFWInj.PF").
	Point string
	// Raw is the unscaled value to write.
	Raw int64
}

// Plan is the writes an action needs, in order (setpoints before their
// enable switch).
type Plan struct {
	// Model is the model number the plan writes to.
	Model int `json:"model"`
	// Writes are the writes, in the order they are to be sent.
	Writes []Write `json:"-"`
	// Note says how the action was mapped when that is not obvious.
	Note string `json:"note,omitempty"`
}

// ErrUnsupported says the device has no model for the action.
var ErrUnsupported = errors.New("the device has no SunSpec model for this control")

// PlanControl turns an action into writes on the device's models. regs
// reads a model instance's registers (for its scale factors). Connect and
// disconnect go to model 123, else 715, else 703; the power limit and
// the power factor to model 704, else 123. A device with none of these
// gets ErrUnsupported.
func PlanControl(m *Map, c Control, regs func(Instance) ([]uint16, error)) (Plan, error) {
	if err := c.Check(); err != nil {
		return Plan{}, err
	}
	der, old := m.Model(704), m.Model(123)
	switch c.Action {
	case ActionConnect, ActionDisconnect:
		on := int64(0)
		if c.Action == ActionConnect {
			on = 1
		}
		if old != nil {
			return Plan{Model: 123, Writes: []Write{{*old, "Conn", on}}}, nil
		}
		if ctl := m.Model(715); ctl != nil {
			return Plan{Model: 715, Writes: []Write{{*ctl, "OpCtl", on}}}, nil // STOP 0, START 1
		}
		if es := m.Model(703); es != nil {
			return Plan{Model: 703, Writes: []Write{{*es, "ES", on}}, Note: "enter service permitted or not (model 703)"}, nil
		}
	case ActionLimitPower, ActionClearPowerLimit:
		inst, enable, sf := der, "WMaxLimPctEna", "WMaxLimPct_SF"
		if inst == nil {
			inst, enable = old, "WMaxLim_Ena"
		}
		if inst == nil {
			break
		}
		if c.Action == ActionClearPowerLimit {
			return Plan{Model: inst.ID, Writes: []Write{{*inst, enable, 0}}}, nil
		}
		raw, err := scaled(*inst, sf, *c.Percent, regs)
		if err != nil {
			return Plan{}, err
		}
		return Plan{Model: inst.ID, Writes: []Write{{*inst, "WMaxLimPct", raw}, {*inst, enable, 1}}}, nil
	case ActionSetPowerFactor, ActionClearPowerFactor:
		if der != nil {
			if c.Action == ActionClearPowerFactor {
				return Plan{Model: 704, Writes: []Write{{*der, "PFWInjEna", 0}}}, nil
			}
			raw, err := scaled(*der, "PF_SF", math.Abs(*c.PowerFactor), regs)
			if err != nil {
				return Plan{}, err
			}
			ext := int64(0) // OVER_EXCITED
			if c.Excitation == "under" {
				ext = 1
			}
			return Plan{Model: 704, Writes: []Write{{*der, "PFWInj.PF", raw}, {*der, "PFWInj.Ext", ext}, {*der, "PFWInjEna", 1}}}, nil
		}
		if old != nil {
			if c.Action == ActionClearPowerFactor {
				return Plan{Model: 123, Writes: []Write{{*old, "OutPFSet_Ena", 0}}}, nil
			}
			raw, err := scaled(*old, "OutPFSet_SF", *c.PowerFactor, regs)
			if err != nil {
				return Plan{}, err
			}
			p := Plan{Model: 123, Writes: []Write{{*old, "OutPFSet", raw}, {*old, "OutPFSet_Ena", 1}}}
			if c.Excitation != "" {
				p.Note = "model 123 takes a signed power factor in the device's own convention; excitation was not used"
			}
			return p, nil
		}
	}
	return Plan{}, ErrUnsupported
}

// scaled converts a value to the raw register value with the model's
// scale factor point.
func scaled(inst Instance, sfName string, v float64, regs func(Instance) ([]uint16, error)) (int64, error) {
	p := inst.Def.point(sfName)
	if p == nil {
		return 0, fmt.Errorf("model %d has no scale factor %s", inst.ID, sfName)
	}
	r, err := regs(inst)
	if err != nil {
		return 0, err
	}
	if p.Offset >= len(r) {
		return 0, fmt.Errorf("model %d is shorter than its scale factor %s", inst.ID, sfName)
	}
	sf := int16(r[p.Offset])
	if sf == math.MinInt16 {
		sf = 0
	}
	return int64(math.Round(v / math.Pow10(int(sf)))), nil
}

// point finds a top-level point, or a point of a group that occurs once
// ("PFWInj.PF"), with its offset from the model's data start.
func (m *Model) point(name string) *Point {
	for i := range m.Points {
		if m.Points[i].Name == name {
			return &m.Points[i]
		}
	}
	for gi := range m.Groups {
		g := &m.Groups[gi]
		for i := range g.Points {
			if g.Name+"."+g.Points[i].Name == name {
				p := g.Points[i]
				p.Offset += g.Offset
				return &p
			}
		}
	}
	return nil
}

// Encode turns a write into its register address and words.
func (w Write) Encode() (uint16, []uint16, error) {
	p := w.Model.Def.point(w.Point)
	if p == nil {
		return 0, nil, fmt.Errorf("model %d has no point %s", w.Model.ID, w.Point)
	}
	if !p.RW {
		return 0, nil, fmt.Errorf("model %d point %s is read-only", w.Model.ID, w.Point)
	}
	if p.Offset+p.Size > w.Model.L {
		return 0, nil, fmt.Errorf("model %d is too short for %s", w.Model.ID, w.Point)
	}
	var words []uint16
	switch p.Size {
	case 1:
		if p.Type == "int16" && (w.Raw < math.MinInt16 || w.Raw > math.MaxInt16) || p.Type != "int16" && (w.Raw < 0 || w.Raw > math.MaxUint16) {
			return 0, nil, fmt.Errorf("%d does not fit %s (%s)", w.Raw, w.Point, p.Type)
		}
		words = []uint16{uint16(w.Raw)}
	case 2:
		words = []uint16{uint16(uint32(w.Raw) >> 16), uint16(w.Raw)}
	default:
		return 0, nil, fmt.Errorf("writing %s (%d registers) is not supported", w.Point, p.Size)
	}
	return w.Model.Addr + uint16(p.Offset), words, nil
}

// ReadBack returns the raw value the device holds for the write's point,
// from regs, a fresh read of the write's model after the writes; ok is
// false when the model has no such point or regs is too short.
func (w Write) ReadBack(regs []uint16) (int64, bool) {
	p := w.Model.Def.point(w.Point)
	if p == nil || p.Offset+p.Size > len(regs) {
		return 0, false
	}
	var u uint64
	for _, x := range regs[p.Offset : p.Offset+p.Size] {
		u = u<<16 | uint64(x)
	}
	if p.Type == "int16" {
		return int64(int16(u)), true
	}
	if p.Type == "int32" {
		return int64(int32(u)), true
	}
	return int64(u), true
}

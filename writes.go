// Copyright 2026 Abhinav Gautam
// SPDX-License-Identifier: Apache-2.0

package sunspec

import (
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"net"
	"net/netip"
	"strings"
)

// Writes of any writable point: top-level, in a group that occurs once,
// in a repeating group or in a nested one (curve points), of every
// integer size, float, string and address type. PlanWrite takes the value
// in engineering units and applies the point's scale factor; Encode turns
// a Write into the registers to send.

// locate places the write's point in its model.
func (w Write) locate(regs []uint16) (Located, error) {
	if w.Model.Def == nil {
		return Located{}, fmt.Errorf("model %d is not described", w.Model.ID)
	}
	if l, ok := Locate(w.Model, regs, w.Point); ok {
		return l, nil
	}
	if p := w.Model.Def.point(w.Point); p != nil {
		return Located{}, fmt.Errorf("model %d is too short for %s", w.Model.ID, w.Point)
	}
	if len(regs) == 0 && hasCountPoint(w.Model.Def.Groups) {
		return Located{}, fmt.Errorf("model %d has no point %s without its registers (Write.Regs): its repeating groups are counted by a point", w.Model.ID, w.Point)
	}
	return Located{}, fmt.Errorf("model %d has no point %s", w.Model.ID, w.Point)
}

func hasCountPoint(gs []Group) bool {
	for _, g := range gs {
		if g.CountPoint != "" || hasCountPoint(g.Groups) {
			return true
		}
	}
	return false
}

// Encode turns a write into its register address and words.
func (w Write) Encode() (uint16, []uint16, error) {
	l, err := w.locate(w.Regs)
	if err != nil {
		return 0, nil, err
	}
	p := l.Point
	if !p.RW {
		return 0, nil, fmt.Errorf("model %d point %s is read-only", w.Model.ID, w.Point)
	}
	if l.Offset+p.Size > w.Model.L {
		return 0, nil, fmt.Errorf("model %d is too short for %s", w.Model.ID, w.Point)
	}
	words, err := encodePoint(p, w.Point, w.Raw, w.Value)
	if err != nil {
		return 0, nil, err
	}
	return w.Model.Addr + uint16(l.Offset), words, nil
}

// ReadBack returns the raw value the device holds for the write's point,
// from regs, a fresh read of the write's model after the writes (for a
// float point, its bits); ok is false when the model has no such point or
// regs is too short. Kept compares any type.
func (w Write) ReadBack(regs []uint16) (int64, bool) {
	l, err := w.locate(regs)
	if err != nil || l.Offset+l.Point.Size > len(regs) || l.Point.Size > 4 {
		return 0, false
	}
	var u uint64
	for _, x := range regs[l.Offset : l.Offset+l.Point.Size] {
		u = u<<16 | uint64(x)
	}
	switch l.Point.Type {
	case "int16", "sunssf":
		return int64(int16(u)), true
	case "int32":
		return int64(int32(u)), true
	}
	return int64(u), true
}

// Kept reports whether regs, a fresh read of the write's model, holds
// exactly the registers the write sent.
func (w Write) Kept(regs []uint16) bool {
	l, err := w.locate(regs)
	if err != nil || l.Offset+l.Point.Size > len(regs) {
		return false
	}
	words, err := encodePoint(l.Point, w.Point, w.Raw, w.Value)
	if err != nil {
		return false
	}
	for i, x := range words {
		if regs[l.Offset+i] != x {
			return false
		}
	}
	return true
}

func signed(typ string) bool {
	switch typ {
	case "int16", "int32", "int64", "sunssf":
		return true
	}
	return false
}

// encodePoint turns a raw value (or, for floats, strings and addresses,
// a Value) into a point's registers.
func encodePoint(p *Point, name string, raw int64, value any) ([]uint16, error) {
	switch p.Type {
	case "float32", "float64":
		f := float64(raw)
		if value != nil {
			v, ok := toFloat(value)
			if !ok {
				return nil, fmt.Errorf("%v is not a number for %s (%s)", value, name, p.Type)
			}
			f = v
		}
		if p.Type == "float32" && p.Size == 2 {
			if !math.IsInf(f, 0) && !math.IsNaN(f) && math.Abs(f) > math.MaxFloat32 {
				return nil, fmt.Errorf("%v does not fit %s (%s)", f, name, p.Type)
			}
			b := math.Float32bits(float32(f))
			return []uint16{uint16(b >> 16), uint16(b)}, nil
		}
		if p.Type == "float64" && p.Size == 4 {
			b := math.Float64bits(f)
			return []uint16{uint16(b >> 48), uint16(b >> 32), uint16(b >> 16), uint16(b)}, nil
		}
	case "string":
		s, ok := value.(string)
		if !ok {
			return nil, fmt.Errorf("a string point %s takes a string Value: writing it as Raw is not supported", name)
		}
		if len(s) > 2*p.Size {
			return nil, fmt.Errorf("%q is longer than %s (%d characters)", s, name, 2*p.Size)
		}
		b := make([]byte, 2*p.Size)
		copy(b, s)
		out := make([]uint16, p.Size)
		for i := range out {
			out[i] = binary.BigEndian.Uint16(b[2*i:])
		}
		return out, nil
	case "ipaddr", "ipv6addr":
		s, _ := value.(string)
		a, err := netip.ParseAddr(s)
		if err != nil || p.Type == "ipaddr" && !a.Is4() || p.Type == "ipv6addr" && !a.Is6() || 2*p.Size != a.BitLen()/8 {
			return nil, fmt.Errorf("%v is not an address for %s (%s)", value, name, p.Type)
		}
		b := a.AsSlice()
		out := make([]uint16, p.Size)
		for i := range out {
			out[i] = binary.BigEndian.Uint16(b[2*i:])
		}
		return out, nil
	case "eui48":
		s, _ := value.(string)
		hw, err := net.ParseMAC(s)
		if err != nil || len(hw) != 6 || p.Size != 4 {
			return nil, fmt.Errorf("%v is not an EUI-48 for %s", value, name)
		}
		b := append([]byte{0, 0}, hw...)
		return []uint16{binary.BigEndian.Uint16(b), binary.BigEndian.Uint16(b[2:]), binary.BigEndian.Uint16(b[4:]), binary.BigEndian.Uint16(b[6:])}, nil
	case "pad":
		return nil, fmt.Errorf("%s is a pad", name)
	default:
		if p.Size < 1 || p.Size > 4 {
			break
		}
		if u, ok := value.(uint64); ok && !signed(p.Type) && p.Size == 4 {
			return []uint16{uint16(u >> 48), uint16(u >> 32), uint16(u >> 16), uint16(u)}, nil
		}
		bits := 16 * p.Size
		if bits < 64 {
			lo, hi := int64(0), int64(1)<<bits-1
			if signed(p.Type) {
				lo, hi = -(int64(1) << (bits - 1)), int64(1)<<(bits-1)-1
			}
			if raw < lo || raw > hi {
				return nil, fmt.Errorf("%d does not fit %s (%s)", raw, name, p.Type)
			}
		} else if !signed(p.Type) && raw < 0 {
			return nil, fmt.Errorf("%d does not fit %s (%s)", raw, name, p.Type)
		}
		out := make([]uint16, p.Size)
		u := uint64(raw)
		for i := p.Size - 1; i >= 0; i-- {
			out[i] = uint16(u)
			u >>= 16
		}
		return out, nil
	}
	return nil, fmt.Errorf("writing %s (%s, %d registers) is not supported", name, p.Type, p.Size)
}

// EncodePoint turns a raw value into a point's registers: an integer (any
// Go integer type, or a whole float64) for the integer types, unscaled; a
// number for float32 and float64; a string for string, ipaddr, ipv6addr
// and eui48. Values out of the type's range are refused. A simulator or
// a test builds register maps with it.
func EncodePoint(p *Point, v any) ([]uint16, error) {
	switch p.Type {
	case "float32", "float64", "string", "ipaddr", "ipv6addr", "eui48":
		return encodePoint(p, p.Name, 0, v)
	}
	if u, ok := v.(uint64); ok {
		if p.Size == 4 && !signed(p.Type) {
			return encodePoint(p, p.Name, 0, u)
		}
		if u > math.MaxInt64 {
			return nil, fmt.Errorf("%d does not fit %s (%s)", u, p.Name, p.Type)
		}
		return encodePoint(p, p.Name, int64(u), nil)
	}
	f, ok := toFloat(v)
	if !ok {
		return nil, fmt.Errorf("%v is not a number for %s (%s)", v, p.Name, p.Type)
	}
	if i, ok := v.(int64); ok {
		return encodePoint(p, p.Name, i, nil)
	}
	if f != math.Trunc(f) || math.Abs(f) > 1<<63 {
		return nil, fmt.Errorf("%v is not a whole number for %s (%s)", v, p.Name, p.Type)
	}
	return encodePoint(p, p.Name, int64(f), nil)
}

func toFloat(v any) (float64, bool) {
	switch x := v.(type) {
	case float64:
		return x, true
	case float32:
		return float64(x), true
	case int:
		return float64(x), true
	case int8:
		return float64(x), true
	case int16:
		return float64(x), true
	case int32:
		return float64(x), true
	case int64:
		return float64(x), true
	case uint:
		return float64(x), true
	case uint8:
		return float64(x), true
	case uint16:
		return float64(x), true
	case uint32:
		return float64(x), true
	case uint64:
		return float64(x), true
	}
	return 0, false
}

// PlanWrite plans a write of one point in engineering units: v is a
// number for the numeric types (the point's scale factor, read from regs,
// is applied: 230.1 V with V_SF -1 is written as 2301), a symbol name or
// a number for an enum, a list of bit names ([]string) or a number for a
// bitfield, a string for string and address types. regs are the model's
// registers as just read; the point may be any Decode name or path,
// nested groups included ("Crv.2.Pt.1.V"). The point must be writable.
func PlanWrite(inst Instance, regs []uint16, name string, v any) (Write, error) {
	w := Write{Model: inst, Point: name, Regs: regs}
	l, err := w.locate(regs)
	if err != nil {
		return Write{}, err
	}
	p := l.Point
	if !p.RW {
		return Write{}, fmt.Errorf("model %d point %s is read-only", inst.ID, name)
	}
	switch {
	case p.Type == "string" || p.Type == "ipaddr" || p.Type == "ipv6addr" || p.Type == "eui48":
		s, ok := v.(string)
		if !ok {
			return Write{}, fmt.Errorf("%s (%s) takes a string, not %v", name, p.Type, v)
		}
		w.Value = s
	case strings.HasPrefix(p.Type, "float"):
		f, ok := toFloat(v)
		if !ok {
			return Write{}, fmt.Errorf("%s (%s) takes a number, not %v", name, p.Type, v)
		}
		w.Value = f
	case strings.HasPrefix(p.Type, "enum"):
		raw, err := enumValue(p, name, v)
		if err != nil {
			return Write{}, err
		}
		w.Raw = raw
	case strings.HasPrefix(p.Type, "bitfield"):
		raw, err := bitsValue(p, name, v)
		if err != nil {
			return Write{}, err
		}
		w.Raw = raw
	default:
		f, ok := toFloat(v)
		if !ok {
			return Write{}, fmt.Errorf("%s (%s) takes a number, not %v", name, p.Type, v)
		}
		sf, scaled, usable := l.Scale(regs)
		if !usable {
			return Write{}, fmt.Errorf("model %d point %s: its scale factor is not implemented or out of range", inst.ID, name)
		}
		if scaled {
			f /= math.Pow10(sf)
		}
		if math.IsNaN(f) || math.IsInf(f, 0) || math.Abs(f) > math.MaxInt64/2 {
			return Write{}, fmt.Errorf("%v does not fit %s (%s)", v, name, p.Type)
		}
		w.Raw = int64(math.Round(f))
	}
	if _, _, err := w.Encode(); err != nil {
		return Write{}, err
	}
	return w, nil
}

func enumValue(p *Point, name string, v any) (int64, error) {
	if s, ok := v.(string); ok {
		if n, ok := p.Symbols[s]; ok {
			return n, nil
		}
		return 0, fmt.Errorf("%s has no symbol %q", name, s)
	}
	f, ok := toFloat(v)
	if !ok || f != math.Trunc(f) {
		return 0, fmt.Errorf("%s takes a symbol name or a whole number, not %v", name, v)
	}
	return int64(f), nil
}

func bitsValue(p *Point, name string, v any) (int64, error) {
	if names, ok := v.([]string); ok {
		var raw int64
		var missing []string
		for _, n := range names {
			bit, ok := p.Symbols[n]
			if !ok || bit < 0 || bit > 62 {
				missing = append(missing, n)
				continue
			}
			raw |= 1 << bit
		}
		if len(missing) > 0 {
			return 0, fmt.Errorf("%s has no bits %v", name, missing)
		}
		return raw, nil
	}
	f, ok := toFloat(v)
	if !ok || f != math.Trunc(f) {
		return 0, errors.New(name + " takes bit names or a whole number")
	}
	return int64(f), nil
}

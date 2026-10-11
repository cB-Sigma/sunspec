// Copyright 2026 Abhinav Gautam
// SPDX-License-Identifier: Apache-2.0

package device

import (
	"fmt"
	"sort"
	"strings"

	"github.com/cB-Sigma/sunspec"
)

// Build lays out a described model's data registers from named raw
// values, as a device publishes them: the top-level points, then each
// group occurrence after occurrence, nested groups after their parent's
// points. values maps a point name to its raw (unscaled) value (an
// integer, a float for float points, a string for string and address
// points, or []uint16 for the registers as they are), a group that occurs
// once to a map[string]any, and a repeating group to a []map[string]any.
// Points not given hold their type's not-implemented value. A count point
// not given is set from the lists: to the longest list of the groups it
// counts (shorter lists are filled up with not-implemented occurrences),
// so a volt-var curve set is
//
//	Build(705, map[string]any{"V_SF": -1, "Crv": []map[string]any{
//		{"ActPt": 2, "Pt": []map[string]any{{"V": 950, "Var": 30}, {"V": 1050, "Var": -30}}}}})
//
// It returns the registers and the offsets of the points the definition
// marks writable.
func Build(id int, values map[string]any) ([]uint16, []int, error) {
	def := sunspec.Lookup(id)
	if def == nil {
		return nil, nil, fmt.Errorf("model %d is not described", id)
	}
	b := &builder{id: id, counts: map[string]int{}}
	b.inferCounts(def.Groups, values)
	top := &bscope{vals: values}
	data := b.points(def.Points, values, top, def.Fixed)
	b.markWritable(def.Points, 0)
	if b.err != nil {
		return nil, nil, b.err
	}
	data = b.groups(def.Groups, data, 0, values, top)
	if b.err != nil {
		return nil, nil, b.err
	}
	if def.Len > 0 && len(data) < def.Len {
		data = append(data, make([]uint16, def.Len-len(data))...)
	}
	sort.Ints(b.writable)
	return data, b.writable, nil
}

type builder struct {
	id       int
	counts   map[string]int // inferred counts by count point name
	writable []int
	err      error
}

// bscope is one occurrence's values with the raw counts given in it.
type bscope struct {
	parent *bscope
	vals   map[string]any
}

func (s *bscope) count(name string) (int, bool) {
	for ; s != nil; s = s.parent {
		if v, ok := s.vals[name]; ok {
			if n, ok := asInt(v); ok {
				return n, true
			}
		}
	}
	return 0, false
}

func asInt(v any) (int, bool) {
	switch x := v.(type) {
	case int:
		return x, true
	case int64:
		return int(x), true
	case uint16:
		return int(x), true
	case float64:
		if x == float64(int(x)) {
			return int(x), true
		}
	}
	return 0, false
}

func list(v any) []map[string]any {
	switch x := v.(type) {
	case []map[string]any:
		return x
	case []any:
		out := make([]map[string]any, 0, len(x))
		for _, e := range x {
			m, _ := e.(map[string]any)
			out = append(out, m)
		}
		return out
	case map[string]any:
		return []map[string]any{x}
	}
	return nil
}

// inferCounts finds, for each count point, the longest list it counts.
func (b *builder) inferCounts(gs []sunspec.Group, vals map[string]any) {
	for _, g := range gs {
		occ := list(vals[g.Name])
		if g.CountPoint != "" && len(occ) > b.counts[g.CountPoint] {
			b.counts[g.CountPoint] = len(occ)
		}
		for _, o := range occ {
			b.inferCounts(g.Groups, o)
		}
	}
}

func (b *builder) fail(format string, args ...any) {
	if b.err == nil {
		b.err = fmt.Errorf("model %d: "+format, append([]any{b.id}, args...)...)
	}
}

// points encodes a group's own points (at least size registers).
func (b *builder) points(pts []sunspec.Point, vals map[string]any, sc *bscope, size int) []uint16 {
	end := size
	for _, p := range pts {
		end = max(end, p.Offset+p.Size)
	}
	out := make([]uint16, end)
	fill(out, 0, pts)
	for i := range pts {
		p := &pts[i]
		v, ok := vals[p.Name]
		if !ok {
			if n, inferred := b.counts[p.Name]; inferred {
				v, ok = n, true
			}
		}
		if !ok || v == nil {
			continue
		}
		var words []uint16
		if w, isWords := v.([]uint16); isWords {
			if len(w) != p.Size {
				b.fail("%s takes %d registers, not %d", p.Name, p.Size, len(w))
				continue
			}
			words = w
		} else {
			var err error
			if words, err = sunspec.EncodePoint(p, v); err != nil {
				b.fail("%v", err)
				continue
			}
		}
		copy(out[p.Offset:], words)
	}
	return out
}

// groups appends the groups' occurrences to data; base is where the
// parent occurrence starts in the model.
func (b *builder) groups(gs []sunspec.Group, data []uint16, base int, vals map[string]any, parent *bscope) []uint16 {
	for _, g := range gs {
		if len(data) < base+g.Offset {
			data = append(data, make([]uint16, base+g.Offset-len(data))...)
		}
		occ := list(vals[g.Name])
		n := len(occ)
		switch {
		case g.Count > 0:
			n = g.Count
		case g.CountPoint != "":
			if c, ok := parent.count(g.CountPoint); ok {
				n = c
			} else {
				n = b.counts[g.CountPoint]
			}
		}
		if g.Count == 1 && len(occ) > 1 {
			b.fail("group %s occurs once, not %d times", g.Name, len(occ))
		}
		if len(occ) > n {
			b.fail("group %s has %d occurrences given, its count says %d", g.Name, len(occ), n)
		}
		for k := 0; k < n; k++ {
			var ov map[string]any
			if k < len(occ) {
				ov = occ[k]
			}
			if ov == nil {
				ov = map[string]any{}
			}
			sc := &bscope{parent: parent, vals: ov}
			start := len(data)
			own := b.points(g.Points, ov, sc, 0)
			b.markWritable(g.Points, start)
			data = append(data, own...)
			data = b.groups(g.Groups, data, start, ov, sc)
			if len(data) < start+g.Len {
				data = append(data, make([]uint16, start+g.Len-len(data))...)
			}
		}
	}
	return data
}

func (b *builder) markWritable(pts []sunspec.Point, base int) {
	for _, p := range pts {
		if p.RW {
			for i := 0; i < p.Size; i++ {
				b.writable = append(b.writable, base+p.Offset+i)
			}
		}
	}
}

// Add appends a model built from named raw values (Build), marks its
// writable points writable, and returns the address of its first data
// register.
func (im *Image) Add(id int, values map[string]any) (uint16, error) {
	data, writable, err := Build(id, values)
	if err != nil {
		return 0, err
	}
	if int(im.next)+2+len(data) > 0xFFFF {
		return 0, fmt.Errorf("model %d (%d registers) does not fit after %d", id, len(data), im.next)
	}
	addr := im.Model(uint16(id), data)
	for _, o := range writable {
		im.Writable(addr + uint16(o))
	}
	return addr, nil
}

// Example gives made-up raw values for every point of a described model,
// two occurrences of each repeating group (nested ones too) and the
// groups that occur once: scale factors -1, enums their first symbol,
// bitfields their first bit, strings the point's name, numbers that grow
// with the point's place. Count points are left to Build. It returns nil
// for a model the tables do not describe.
func Example(id int) map[string]any {
	def := sunspec.Lookup(id)
	if def == nil {
		return nil
	}
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
			if counts[p.Name] {
				continue
			}
			n++
			if v := exampleValue(p, n); v != nil {
				out[p.Name] = v
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

func exampleValue(p sunspec.Point, n int) any {
	switch {
	case p.Type == "pad":
		return nil
	case p.Type == "sunssf":
		return -1
	case strings.HasPrefix(p.Type, "enum"):
		best, ok := int64(0), false
		for _, v := range p.Symbols {
			if !ok || v < best {
				best, ok = v, true
			}
		}
		if !ok {
			return 1
		}
		return best
	case strings.HasPrefix(p.Type, "bitfield"):
		best, ok := int64(0), false
		for _, v := range p.Symbols {
			if v >= 0 && v < 16*int64(p.Size) && (!ok || v < best) {
				best, ok = v, true
			}
		}
		if !ok {
			return 0
		}
		return int64(1) << best
	case p.Type == "string":
		s := p.Name
		if len(s) > 2*p.Size {
			s = s[:2*p.Size]
		}
		return s
	case p.Type == "ipaddr":
		return "192.0.2.1"
	case p.Type == "ipv6addr":
		return "2001:db8::1"
	case p.Type == "eui48":
		return "02:00:5e:00:53:01"
	case strings.HasPrefix(p.Type, "float"):
		return float64(n) + 0.5
	case p.Type == "int16" || p.Type == "int32" || p.Type == "int64":
		return -(n % 100) - 1
	case strings.HasPrefix(p.Type, "acc"):
		return 1000 + n
	}
	return 100 + n%1000
}

// AllModels builds an image at base holding one instance of every
// described model, in model number order, each with Example values (the
// repeating groups twice), then the end model. It returns the image and
// the address of each model's first data register.
func AllModels(base uint16) (*Image, map[int]uint16, error) {
	im := NewImage(base)
	at := map[int]uint16{}
	for _, id := range sunspec.Known() {
		addr, err := im.Add(id, Example(id))
		if err != nil {
			return nil, nil, err
		}
		at[id] = addr
	}
	if int(im.next)+2 > 0xFFFF {
		return nil, nil, fmt.Errorf("the models do not fit after %d", base)
	}
	im.End()
	return im, at, nil
}

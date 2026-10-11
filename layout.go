// Copyright 2026 Abhinav Gautam
// SPDX-License-Identifier: Apache-2.0

package sunspec

import "fmt"

// Located is a point placed in a model instance's registers by Layout.
type Located struct {
	// Name is the name Decode gives the point's value (see Value.Name).
	Name string
	// Path names the point with every group spelt out, occurrences
	// counted from 1: "W", "PFWInj.PF", "module.2.DCV",
	// "Crv.1.Pt.3.V", "Crv.2.MustTrip.Pt.1.Tms".
	Path string
	// Point is the point's definition (its Offset is the definition's,
	// counted within its group).
	Point *Point
	// Offset counts registers from the model's first data register.
	Offset int

	scope *scope
}

// scope is one group occurrence (or the model's top level): where its
// points are, for the scale factors and counts named from inside it.
type scope struct {
	parent *scope
	at     map[string]int
	defs   map[string]*Point
}

func (s *scope) find(name string) (*Point, int, bool) {
	for ; s != nil; s = s.parent {
		if off, ok := s.at[name]; ok {
			return s.defs[name], off, true
		}
	}
	return nil, 0, false
}

// Layout places every point of a model instance: the top-level points,
// then each group in turn, occurrence after occurrence, groups nested in
// a group after its own points, to any depth. How often a group occurs
// comes from its definition (once, or a fixed number), from a count point
// (read from regs, looked up from the group's own scope outwards to the
// model's top level), or from the model's length (as many occurrences as
// fit). Occurrences that would start past the registers available (L, or
// regs when shorter) are not placed. regs may be nil for a model whose
// layout needs no count point.
func Layout(inst Instance, regs []uint16) []Located {
	def := inst.Def
	if def == nil {
		return nil
	}
	bound := inst.L
	if len(regs) > 0 && (bound <= 0 || len(regs) < bound) {
		bound = len(regs)
	}
	l := &layouter{regs: regs, bound: bound}
	top := &scope{at: map[string]int{}, defs: map[string]*Point{}}
	end := l.points(def.Points, 0, top, "", "")
	repeating := 0
	for _, g := range def.Groups {
		if g.Count != 1 {
			repeating++
		}
	}
	l.groups(def.Groups, 0, end, top, "", "", repeating == 1)
	return l.out
}

type layouter struct {
	regs  []uint16
	bound int
	out   []Located
}

// points places a group's own points from start and returns where they
// end.
func (l *layouter) points(pts []Point, start int, sc *scope, name, path string) int {
	end := start
	for i := range pts {
		p := &pts[i]
		off := start + p.Offset
		sc.at[p.Name], sc.defs[p.Name] = off, p
		l.out = append(l.out, Located{Name: name + p.Name, Path: path + p.Name, Point: p, Offset: off, scope: sc})
		end = max(end, off+p.Size)
	}
	return end
}

// groups places groups one after the other from start (the end of their
// parent's points; base is where the parent occurrence begins) and returns
// where the last ends. A group never starts before its definition's
// offset, nor is an occurrence shorter than the definition's length (both
// count nested repeating groups as absent). oneRepeating is true at the
// top level of a model whose only repeating group is among them (its
// values are named "2.DCV").
func (l *layouter) groups(gs []Group, base, start int, parent *scope, name, path string, oneRepeating bool) int {
	cur := start
	for gi := range gs {
		g := &gs[gi]
		cur = max(cur, base+g.Offset)
		n := l.count(g, parent, cur)
		for k := 0; k < n; k++ {
			if cur >= l.bound || g.Count != 1 && g.Len > 0 && cur+g.Len > l.bound {
				break
			}
			sc := &scope{parent: parent, at: map[string]int{}, defs: map[string]*Point{}}
			gname, gpath := name+g.Name+".", path+g.Name+"."
			if g.Count != 1 {
				gpath = fmt.Sprintf("%s%s.%d.", path, g.Name, k+1)
				if oneRepeating && name == "" {
					gname = fmt.Sprintf("%d.", k+1)
				} else {
					gname = fmt.Sprintf("%s%s.%d.", name, g.Name, k+1)
				}
			}
			end := l.points(g.Points, cur, sc, gname, gpath)
			end = max(l.groups(g.Groups, cur, end, sc, gname, gpath, false), cur+g.Len)
			if end <= cur {
				break // an empty occurrence: nothing more to place
			}
			cur = end
		}
	}
	return cur
}

// maxOccurrences bounds a count read from a device.
const maxOccurrences = 10000

func (l *layouter) count(g *Group, sc *scope, at int) int {
	switch {
	case g.Count > 0:
		return g.Count
	case g.CountPoint != "":
		p, off, ok := sc.find(g.CountPoint)
		if !ok || off+p.Size > len(l.regs) {
			return 0
		}
		raw, ok := Raw(p.Type, l.regs[off:off+p.Size])
		if !ok || raw < 0 {
			return 0
		}
		return int(min(raw, maxOccurrences))
	case g.FromLength:
		if g.Len <= 0 || at >= l.bound {
			return 0
		}
		return (l.bound - at) / g.Len
	}
	return 0
}

// Locate finds a point of an instance by its Decode name or its Path
// ("Crv.1.Pt.3.V"); regs, the model's registers, give the counts of
// repeating groups that have a count point (nil will do for the others).
func Locate(inst Instance, regs []uint16, name string) (Located, bool) {
	for _, l := range Layout(inst, regs) {
		if l.Path == name || l.Name == name {
			return l, true
		}
	}
	return Located{}, false
}

// MinSF and MaxSF bound a usable scale factor: SunSpec scale factors run
// from -10 to 10. A value scaled by a scale factor outside them (a device
// fault) is left out rather than turned into a huge or tiny number.
const (
	MinSF = -10
	MaxSF = 10
)

// Scale returns the power of ten a located point is scaled by, read from
// regs: its scale factor point (looked up from the point's own group
// outwards to the model's top level, as pysunspec2 does for one level) or
// the fixed power its definition gives. scaled is false for a point
// without a scale factor, or whose scale factor point the model does not
// have (then the value is taken as it is). usable is false when the scale
// factor point is not implemented (0x8000), outside MinSF to MaxSF, or
// past regs.
func (l Located) Scale(regs []uint16) (sf int, scaled, usable bool) {
	switch s := l.Point.SF.(type) {
	case string:
		_, off, ok := l.scope.find(s)
		if !ok {
			return 0, false, true
		}
		if off >= len(regs) {
			return 0, true, false
		}
		v := int16(regs[off])
		if v < MinSF || v > MaxSF {
			return 0, true, false
		}
		return int(v), true, true
	case float64:
		if s < MinSF || s > MaxSF {
			return 0, true, false
		}
		return int(s), true, true
	case int:
		if s < MinSF || s > MaxSF {
			return 0, true, false
		}
		return s, true, true
	}
	return 0, false, true
}

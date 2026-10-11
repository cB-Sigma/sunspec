// Copyright 2026 Abhinav Gautam
// SPDX-License-Identifier: Apache-2.0

package device

import (
	"strings"

	"github.com/cB-Sigma/sunspec"
)

// NotImplemented returns the data registers of a model described by
// package sunspec with every point at its type's "not implemented" value
// (0x8000 for int16, sunssf and pad, 0x80000000 for int32, all ones for
// unsigned integers, enums and bitfields, zero for accumulators, strings
// and addresses, NaN for floats), ready to have the points a device
// implements set over it. A model with repeating groups gets its fixed
// part and then repeats[i] occurrences of its i-th repeating group (none
// when not given; a group counted by a point gets that point set to the
// number, and nested repeating groups get none); groups that occur once
// are always included. Build lays out values of nested groups. It
// returns nil for a model the tables do not describe.
func NotImplemented(id int, repeats ...int) []uint16 {
	def := sunspec.Lookup(id)
	if def == nil {
		return nil
	}
	values := map[string]any{}
	r := 0
	for _, g := range def.Groups {
		if g.Count == 1 {
			continue
		}
		k := 0
		if r < len(repeats) {
			k = repeats[r]
		}
		r++
		if g.Count > 1 {
			continue // a fixed number of occurrences
		}
		values[g.Name] = make([]map[string]any, k)
	}
	out, _, err := Build(id, values)
	if err != nil {
		return nil
	}
	return out
}

func fill(regs []uint16, base int, points []sunspec.Point) {
	for _, p := range points {
		off := base + p.Offset
		if off < 0 || off+p.Size > len(regs) {
			continue
		}
		w := regs[off : off+p.Size]
		for i := range w {
			w[i] = 0
		}
		switch {
		case p.Type == "int16" || p.Type == "sunssf" || p.Type == "pad" ||
			p.Type == "int32" || p.Type == "int64":
			w[0] = 0x8000
		case p.Type == "float32":
			w[0] = 0x7FC0
		case p.Type == "float64":
			w[0] = 0x7FF8
		case strings.HasPrefix(p.Type, "acc"), p.Type == "string", p.Type == "ipaddr", p.Type == "ipv6addr":
			// zero
		default: // uint16/32/64, count, enums, bitfields, eui48
			for i := range w {
				w[i] = 0xFFFF
			}
		}
	}
}

// Copyright 2026 Abhinav Gautam
// SPDX-License-Identifier: Apache-2.0

package sunspec

// Found is what Scan found on one Modbus unit.
type Found struct {
	// Unit is the unit address asked.
	Unit uint8 `json:"unit"`
	// Map is the unit's SunSpec map, nil when Err is set.
	Map *Map `json:"map,omitempty"`
	// Identity is its common model, when it has one.
	Identity Identity `json:"identity,omitempty"`
	// Err is ErrNotSunSpec for a unit that answered without a map, or the
	// read error of a unit that did not answer.
	Err error `json:"-"`
}

// Scan looks for a SunSpec map (Find) on each unit in turn; reader gives
// the Reader for a unit (mbtcp.Reader.ForUnit shares one connection). A
// gateway or a site with several devices answers on several units: an
// inverter on 1, its meters on 2 and 3, an SMA device on 126. Each unit
// costs three to eight reads, and a silent one its timeout for each.
func Scan(units []uint8, reader func(unit uint8) Reader) []Found {
	out := make([]Found, 0, len(units))
	for _, u := range units {
		r := reader(u)
		f := Found{Unit: u}
		f.Map, f.Err = Find(r)
		if f.Err == nil && f.Map.Model(1) != nil {
			f.Identity, _ = ReadIdentity(r, f.Map)
		}
		out = append(out, f)
	}
	return out
}

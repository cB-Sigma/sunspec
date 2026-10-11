// Copyright 2026 Abhinav Gautam
// SPDX-License-Identifier: Apache-2.0

package sunspec

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// Definitions at run time: a vendor's model, or a SunSpec model newer
// than the embedded tables, read from a definition file in the layout of
// the SunSpec Alliance's json/model_<id>.json (the layout pysunspec2
// loads) and registered so that Walk, Decode, Layout and the writes use
// it.

type srcSymbol struct {
	Name  string `json:"name"`
	Value int64  `json:"value"`
}

type srcPoint struct {
	Name      string      `json:"name"`
	Type      string      `json:"type"`
	Size      int         `json:"size"`
	SF        any         `json:"sf"`
	Units     string      `json:"units"`
	Access    string      `json:"access"`
	Mandatory string      `json:"mandatory"`
	Static    string      `json:"static"`
	Label     string      `json:"label"`
	Symbols   []srcSymbol `json:"symbols"`
}

type srcGroup struct {
	Name   string     `json:"name"`
	Label  string     `json:"label"`
	Count  any        `json:"count"`
	Points []srcPoint `json:"points"`
	Groups []srcGroup `json:"groups"`
}

// typeSizes are the register counts of the types whose size a definition
// may leave out.
var typeSizes = map[string]int{"int16": 1, "uint16": 1, "count": 1, "acc16": 1, "enum16": 1, "bitfield16": 1, "pad": 1, "sunssf": 1,
	"int32": 2, "uint32": 2, "acc32": 2, "enum32": 2, "bitfield32": 2, "ipaddr": 2, "float32": 2,
	"int64": 4, "uint64": 4, "acc64": 4, "bitfield64": 4, "eui48": 4, "float64": 4, "ipv6addr": 8}

// ParseDefinition reads one model definition in the layout of the SunSpec
// Alliance's json/model_<id>.json files: {"id": …, "group": {"name",
// "label", "points": [...], "groups": [...]}}, the top group's first two
// points being ID and L. It keeps what this package uses (names, types,
// sizes, scale factors, units, access, mandatory and static flags,
// symbols, labels, group counts and nesting) and lays out the offsets.
func ParseDefinition(b []byte) (*Model, error) {
	var src struct {
		ID    int      `json:"id"`
		Group srcGroup `json:"group"`
	}
	if err := json.Unmarshal(b, &src); err != nil {
		return nil, fmt.Errorf("model definition: %w", err)
	}
	if src.ID <= 0 || src.ID >= EndID || src.Group.Name == "" {
		return nil, errors.New("model definition: no id or no group name")
	}
	pts, plen, err := defPoints(src.ID, src.Group.Points, true)
	if err != nil {
		return nil, err
	}
	gs, end, repeats, err := defGroups(src.ID, src.Group.Groups, plen)
	if err != nil {
		return nil, err
	}
	m := &Model{ID: src.ID, Name: src.Group.Name, Label: strings.TrimSpace(src.Group.Label), Points: pts, Groups: gs}
	if repeats {
		m.Fixed = plen
	} else {
		m.Len = end
	}
	return m, nil
}

func defPoints(id int, src []srcPoint, skipHeader bool) ([]Point, int, error) {
	var out []Point
	off := 0
	for i, p := range src {
		if skipHeader && i < 2 && (p.Name == "ID" || p.Name == "L") {
			continue
		}
		size := p.Size
		if size == 0 {
			size = typeSizes[p.Type]
		}
		if size <= 0 || p.Name == "" {
			return nil, 0, fmt.Errorf("model %d point %q (%s) has no size", id, p.Name, p.Type)
		}
		q := Point{Name: p.Name, Offset: off, Type: p.Type, Size: size, Units: p.Units, RW: p.Access == "RW",
			M: p.Mandatory == "M", Static: p.Static == "S", Label: strings.TrimSpace(p.Label)}
		switch sf := p.SF.(type) {
		case nil:
		case string:
			q.SF = sf
		case float64:
			q.SF = sf
		default:
			return nil, 0, fmt.Errorf("model %d point %s: scale factor %v", id, p.Name, p.SF)
		}
		if len(p.Symbols) > 0 {
			q.Symbols = map[string]int64{}
			for _, s := range p.Symbols {
				q.Symbols[s.Name] = s.Value
			}
		}
		out = append(out, q)
		off += size
	}
	return out, off, nil
}

// defGroups lays out groups from start, repeating groups counted as
// absent for the static places and lengths; it reports whether any of
// them, or of their nested groups, repeats.
func defGroups(id int, src []srcGroup, start int) ([]Group, int, bool, error) {
	var out []Group
	off := start
	repeats := false
	for _, g := range src {
		pts, plen, err := defPoints(id, g.Points, false)
		if err != nil {
			return nil, 0, false, err
		}
		sub, end, subRepeats, err := defGroups(id, g.Groups, plen)
		if err != nil {
			return nil, 0, false, err
		}
		q := Group{Name: g.Name, Offset: off, Len: end, Points: pts, Groups: sub}
		switch c := g.Count.(type) {
		case nil:
			q.Count = 1
		case float64:
			if c == 0 {
				q.FromLength = true
			} else if c > 0 && c == float64(int(c)) {
				q.Count = int(c)
			} else {
				return nil, 0, false, fmt.Errorf("model %d group %s: count %v", id, g.Name, c)
			}
		case string:
			q.CountPoint = c
		default:
			return nil, 0, false, fmt.Errorf("model %d group %s: count %v", id, g.Name, g.Count)
		}
		if q.Count == 1 {
			off += end
			repeats = repeats || subRepeats
		} else {
			repeats = true
		}
		out = append(out, q)
	}
	return out, off, repeats, nil
}

// Register adds a model definition, or replaces the one with its ID (the
// embedded tables included), for every later Walk, Lookup and Known.
// Instances found before keep the definition they were found with.
func Register(m *Model) error {
	if m == nil || m.ID <= 0 || m.ID >= EndID {
		return errors.New("sunspec: Register needs a model with an ID from 1 to 65534")
	}
	loadOnce.Do(load)
	if loadErr != nil {
		return loadErr
	}
	modelsMu.Lock()
	defer modelsMu.Unlock()
	next := make(map[int]*Model, len(models)+1)
	for id, d := range models {
		next[id] = d
	}
	next[m.ID] = m
	models = next
	return nil
}

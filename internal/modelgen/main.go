// Copyright 2026 Abhinav Gautam
// SPDX-License-Identifier: Apache-2.0

// Command modelgen derives models.json from the SunSpec Alliance's model
// definitions (github.com/sunspec/models, directory json/, Apache-2.0),
// each read with sunspec.ParseDefinition: for each point its name,
// offset, type, size, scale factor, unit, access, mandatory and static
// flags, symbols and short label, and for each group its name, place,
// length, count and nested groups; descriptions, comments and other prose
// are not taken. It applies the corrections NOTICE lists. Run it from the
// module root:
//
//	go run ./internal/modelgen -src /path/to/sunspec-models/json -commit <sha> -out models.json
package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/cB-Sigma/sunspec"
)

// fix applies the corrections NOTICE lists: model 111's St symbols carry
// a stray "gg" prefix in the source.
func fix(m *sunspec.Model) {
	if m.ID != 111 {
		return
	}
	for i := range m.Points {
		p := &m.Points[i]
		if p.Name != "St" {
			continue
		}
		sym := map[string]int64{}
		for k, v := range p.Symbols {
			sym[strings.TrimPrefix(k, "gg")] = v
		}
		p.Symbols = sym
	}
}

// write lays the file out with one point per line.
func write(buf *bytes.Buffer, source string, ms []*sunspec.Model) error {
	s, _ := json.Marshal(source)
	fmt.Fprintf(buf, "{\n \"source\": %s,\n \"models\": [\n", s)
	for i, m := range ms {
		name, _ := json.Marshal(m.Name)
		label, _ := json.Marshal(m.Label)
		fmt.Fprintf(buf, "  {\"id\": %d, \"name\": %s, \"l\": %s", m.ID, name, label)
		if m.Len > 0 {
			fmt.Fprintf(buf, ", \"len\": %d", m.Len)
		} else {
			fmt.Fprintf(buf, ", \"fixed\": %d", m.Fixed)
		}
		buf.WriteString(",\n   \"points\": [\n")
		if err := writePoints(buf, m.Points, "    "); err != nil {
			return err
		}
		buf.WriteString("   ]")
		if len(m.Groups) > 0 {
			buf.WriteString(",\n   \"groups\": [\n")
			if err := writeGroups(buf, m.Groups, "    "); err != nil {
				return err
			}
			buf.WriteString("   ]")
		}
		buf.WriteString("}")
		if i < len(ms)-1 {
			buf.WriteString(",")
		}
		buf.WriteString("\n")
	}
	buf.WriteString(" ]\n}\n")
	return nil
}

func writePoints(buf *bytes.Buffer, ps []sunspec.Point, indent string) error {
	for i, p := range ps {
		b, err := json.Marshal(p)
		if err != nil {
			return err
		}
		buf.WriteString(indent)
		buf.Write(b)
		if i < len(ps)-1 {
			buf.WriteString(",")
		}
		buf.WriteString("\n")
	}
	return nil
}

func writeGroups(buf *bytes.Buffer, gs []sunspec.Group, indent string) error {
	for i, g := range gs {
		name, _ := json.Marshal(g.Name)
		fmt.Fprintf(buf, "%s{\"name\": %s, \"off\": %d, \"len\": %d", indent, name, g.Offset, g.Len)
		switch {
		case g.Count > 0:
			fmt.Fprintf(buf, ", \"count\": %d", g.Count)
		case g.CountPoint != "":
			cp, _ := json.Marshal(g.CountPoint)
			fmt.Fprintf(buf, ", \"countPoint\": %s", cp)
		case g.FromLength:
			buf.WriteString(", \"fromLength\": true")
		}
		fmt.Fprintf(buf, ",\n%s \"points\": [\n", indent)
		if err := writePoints(buf, g.Points, indent+"  "); err != nil {
			return err
		}
		fmt.Fprintf(buf, "%s ]", indent)
		if len(g.Groups) > 0 {
			fmt.Fprintf(buf, ",\n%s \"groups\": [\n", indent)
			if err := writeGroups(buf, g.Groups, indent+"  "); err != nil {
				return err
			}
			fmt.Fprintf(buf, "%s ]", indent)
		}
		buf.WriteString("}")
		if i < len(gs)-1 {
			buf.WriteString(",")
		}
		buf.WriteString("\n")
	}
	return nil
}

func run(src, commit, out string) error {
	if src == "" || commit == "" || out == "" {
		return errors.New("-src, -commit and -out are required")
	}
	files, err := filepath.Glob(filepath.Join(src, "model_*.json"))
	if err != nil {
		return err
	}
	if len(files) == 0 {
		return fmt.Errorf("no model_*.json in %s", src)
	}
	var ms []*sunspec.Model
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			return err
		}
		m, err := sunspec.ParseDefinition(b)
		if err != nil {
			return fmt.Errorf("%s: %w", f, err)
		}
		fix(m)
		ms = append(ms, m)
	}
	sort.Slice(ms, func(i, j int) bool { return ms[i].ID < ms[j].ID })
	source := "Derived from the SunSpec Alliance information models (https://github.com/sunspec/models, json/model_<id>.json, commit " +
		commit + ", Apache License 2.0) by internal/modelgen: point names, offsets, types, sizes, scale factors, units, access, " +
		"mandatory and static flags, symbol values and short labels; group names, places, lengths and counts. Offsets count from " +
		"the first register after the model's ID and L (a group's points from the start of an occurrence). Modified: the symbol " +
		"names of model 111's St are written without the stray \"gg\" prefix the source has, as in models 101 to 113; labels are trimmed."
	var buf bytes.Buffer
	if err := write(&buf, source, ms); err != nil {
		return err
	}
	// The output must read back as JSON.
	var check any
	if err := json.Unmarshal(buf.Bytes(), &check); err != nil {
		return fmt.Errorf("generated file is not JSON: %w", err)
	}
	return os.WriteFile(out, buf.Bytes(), 0o644)
}

func main() {
	src := flag.String("src", "", "the json/ directory of a github.com/sunspec/models checkout")
	commit := flag.String("commit", "", "the commit of that checkout, recorded in the output")
	out := flag.String("out", "models.json", "the file to write")
	flag.Parse()
	if err := run(*src, *commit, *out); err != nil {
		fmt.Fprintln(os.Stderr, "modelgen:", err)
		os.Exit(1)
	}
}

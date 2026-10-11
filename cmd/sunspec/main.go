// Copyright 2026 Abhinav Gautam
// SPDX-License-Identifier: Apache-2.0

// Command sunspec inspects SunSpec devices over Modbus: it scans units
// for SunSpec maps, reads and decodes every model (nested groups
// included), writes a point in engineering units and reads it back, and
// lists the model definitions it knows.
//
//	sunspec scan  -url tcp://192.168.1.40:502 -units 1,2,3,126
//	sunspec read  -url tcp://192.168.1.40:502 -unit 1 [-model 705] [-json] [-raw]
//	sunspec write -url tcp://192.168.1.40:502 -unit 1 -model 704 -point WMaxLimPct -value 60
//	sunspec models [-model 705]
//
// The URL picks the transport: tcp://, tcp+tls:// (with -cert, -key and
// -ca), udp://, rtu:///dev/ttyUSB0 (with -baud, -parity, -databits,
// -stopbits), rtuovertcp://, rtuoverudp://.
package main

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/cB-Sigma/sunspec"
	"github.com/cB-Sigma/sunspec/mbtcp"
)

func main() {
	if err := run(os.Args[1:], os.Stdout); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return
		}
		fmt.Fprintln(os.Stderr, "sunspec:", err)
		os.Exit(1)
	}
}

const usage = `usage: sunspec <command> [flags]

commands:
  scan    look for SunSpec maps on several units
  read    find the map and decode every model
  write   write one point (engineering units) and read it back
  models  list the model definitions, or one model's points

"sunspec <command> -h" lists a command's flags.`

func run(args []string, out io.Writer) error {
	if len(args) == 0 {
		fmt.Fprintln(out, usage)
		return flag.ErrHelp
	}
	switch args[0] {
	case "scan":
		return scan(args[1:], out)
	case "read":
		return read(args[1:], out)
	case "write":
		return write(args[1:], out)
	case "models":
		return models(args[1:], out)
	case "-h", "-help", "--help", "help":
		fmt.Fprintln(out, usage)
		return flag.ErrHelp
	}
	return fmt.Errorf("unknown command %q (scan, read, write, models)", args[0])
}

// conn holds the transport flags.
type conn struct {
	url, parity, cert, key, ca, defs string
	baud, dataBits, stopBits         uint
	timeout                          time.Duration
}

func (c *conn) flags(fs *flag.FlagSet) {
	fs.StringVar(&c.url, "url", "", "device URL: tcp://host:502, tcp+tls://host:802, udp://, rtu:///dev/ttyUSB0, rtuovertcp://, rtuoverudp://")
	fs.DurationVar(&c.timeout, "timeout", 0, "response timeout (0: 2s, 300ms on a serial line)")
	fs.UintVar(&c.baud, "baud", 0, "serial speed (rtu; 0: 19200)")
	fs.StringVar(&c.parity, "parity", "", "serial parity N, E or O (rtu)")
	fs.UintVar(&c.dataBits, "databits", 0, "serial data bits (rtu; 0: 8)")
	fs.UintVar(&c.stopBits, "stopbits", 0, "serial stop bits (rtu; 0: 2 without parity, 1 with)")
	fs.StringVar(&c.cert, "cert", "", "client certificate PEM file (tcp+tls)")
	fs.StringVar(&c.key, "key", "", "client key PEM file (tcp+tls)")
	fs.StringVar(&c.ca, "ca", "", "CA or server certificate PEM file (tcp+tls)")
	fs.StringVar(&c.defs, "defs", "", "model definition files to load first (SunSpec JSON layout, comma-separated): vendor or newer models")
}

func (c *conn) open(unit uint8) (*mbtcp.Reader, error) {
	if c.url == "" {
		return nil, errors.New("-url is required")
	}
	if c.defs != "" {
		for _, f := range strings.Split(c.defs, ",") {
			b, err := os.ReadFile(strings.TrimSpace(f))
			if err != nil {
				return nil, err
			}
			d, err := sunspec.ParseDefinition(b)
			if err != nil {
				return nil, fmt.Errorf("%s: %w", f, err)
			}
			if err := sunspec.Register(d); err != nil {
				return nil, err
			}
		}
	}
	cfg := mbtcp.Config{URL: c.url, Unit: unit, Timeout: c.timeout, Speed: c.baud, Parity: c.parity, DataBits: c.dataBits, StopBits: c.stopBits}
	if c.cert != "" || c.key != "" {
		pair, err := tls.LoadX509KeyPair(c.cert, c.key)
		if err != nil {
			return nil, err
		}
		cfg.TLSClientCert = &pair
	}
	if c.ca != "" {
		pem, err := os.ReadFile(c.ca)
		if err != nil {
			return nil, err
		}
		cfg.TLSRootCAs = x509.NewCertPool()
		if !cfg.TLSRootCAs.AppendCertsFromPEM(pem) {
			return nil, fmt.Errorf("%s holds no PEM certificate", c.ca)
		}
	}
	return mbtcp.Open(cfg)
}

func parseFlags(fs *flag.FlagSet, args []string, out io.Writer) error {
	fs.SetOutput(out)
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		return fmt.Errorf("unexpected argument %q", fs.Arg(0))
	}
	return nil
}

func unitOf(u int) (uint8, error) {
	if u < 0 || u > 255 {
		return 0, fmt.Errorf("unit %d is out of range", u)
	}
	return uint8(u), nil
}

func scan(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("scan", flag.ContinueOnError)
	var c conn
	c.flags(fs)
	units := fs.String("units", "1,2,3,126", "units to try: a list (1,2,126) or ranges (1-10,126)")
	jsonOut := fs.Bool("json", false, "print JSON")
	if err := parseFlags(fs, args, out); err != nil {
		return err
	}
	list, err := parseUnits(*units)
	if err != nil {
		return err
	}
	r, err := c.open(list[0])
	if err != nil {
		return err
	}
	defer r.Close()
	found := sunspec.Scan(list, func(u uint8) sunspec.Reader { return r.ForUnit(u) })
	if *jsonOut {
		type row struct {
			sunspec.Found
			Error string `json:"error,omitempty"`
		}
		rows := make([]row, 0, len(found))
		for _, f := range found {
			e := ""
			if f.Err != nil {
				e = f.Err.Error()
			}
			rows = append(rows, row{f, e})
		}
		return writeJSON(out, rows)
	}
	for _, f := range found {
		if f.Err != nil {
			fmt.Fprintf(out, "unit %d: %v\n", f.Unit, f.Err)
			continue
		}
		fmt.Fprintf(out, "unit %d: SunSpec map at %d, models %v", f.Unit, f.Map.Base, ids(f.Map))
		if f.Identity.Manufacturer != "" {
			fmt.Fprintf(out, "; %s %s, serial %s", f.Identity.Manufacturer, f.Identity.Model, f.Identity.Serial)
		}
		fmt.Fprintln(out)
	}
	return nil
}

func parseUnits(s string) ([]uint8, error) {
	var out []uint8
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		lo, hi, isRange := strings.Cut(part, "-")
		a, err := strconv.Atoi(lo)
		if err != nil || a < 0 || a > 255 {
			return nil, fmt.Errorf("unit %q is not 0 to 255", part)
		}
		b := a
		if isRange {
			if b, err = strconv.Atoi(hi); err != nil || b < a || b > 255 {
				return nil, fmt.Errorf("unit range %q is not ascending within 0 to 255", part)
			}
		}
		for u := a; u <= b; u++ {
			out = append(out, uint8(u))
		}
	}
	if len(out) == 0 {
		return nil, errors.New("no units")
	}
	return out, nil
}

func ids(m *sunspec.Map) []int {
	out := make([]int, 0, len(m.Models))
	for _, inst := range m.Models {
		out = append(out, inst.ID)
	}
	return out
}

func writeJSON(out io.Writer, v any) error {
	e := json.NewEncoder(out)
	e.SetIndent("", "  ")
	return e.Encode(v)
}

func read(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("read", flag.ContinueOnError)
	var c conn
	c.flags(fs)
	unit := fs.Int("unit", 1, "Modbus unit")
	only := fs.Int("model", 0, "read only this model (0: all)")
	jsonOut := fs.Bool("json", false, "print JSON")
	raw := fs.Bool("raw", false, "also print each model's registers")
	if err := parseFlags(fs, args, out); err != nil {
		return err
	}
	u, err := unitOf(*unit)
	if err != nil {
		return err
	}
	r, err := c.open(u)
	if err != nil {
		return err
	}
	defer r.Close()
	m, err := sunspec.Find(r)
	if err != nil {
		return err
	}
	type point struct {
		Name string `json:"name"`
		Path string `json:"path"`
		V    any    `json:"value"`
		Unit string `json:"unit,omitempty"`
	}
	type model struct {
		ID     int      `json:"id"`
		Name   string   `json:"name"`
		Addr   uint16   `json:"addr"`
		Len    int      `json:"len"`
		Known  bool     `json:"known"`
		Regs   []uint16 `json:"regs,omitempty"`
		Points []point  `json:"points,omitempty"`
		Error  string   `json:"error,omitempty"`
	}
	var models []model
	for _, inst := range m.Models {
		if *only != 0 && inst.ID != *only {
			continue
		}
		e := model{ID: inst.ID, Name: inst.Name, Addr: inst.Addr, Len: inst.L, Known: inst.Def != nil}
		regs, err := sunspec.ReadModel(r, inst)
		if err != nil {
			e.Error = err.Error()
			models = append(models, e)
			continue
		}
		if *raw {
			e.Regs = regs
		}
		for _, v := range sunspec.Decode(inst, regs) {
			e.Points = append(e.Points, point{v.Name, v.Path, v.V, v.Unit})
		}
		models = append(models, e)
	}
	if *only != 0 && len(models) == 0 {
		return fmt.Errorf("the map has no model %d (it has %v)", *only, ids(m))
	}
	if *jsonOut {
		return writeJSON(out, map[string]any{"base": m.Base, "models": models})
	}
	fmt.Fprintf(out, "SunSpec map at %d\n", m.Base)
	for _, e := range models {
		label := ""
		if d := sunspec.Lookup(e.ID); d != nil {
			label = " (" + d.Label + ")"
		}
		fmt.Fprintf(out, "\nmodel %d %s%s at %d, %d registers\n", e.ID, e.Name, label, e.Addr, e.Len)
		if e.Error != "" {
			fmt.Fprintf(out, "  %s\n", e.Error)
		}
		if !e.Known {
			fmt.Fprintln(out, "  not described")
		}
		for _, p := range e.Points {
			fmt.Fprintf(out, "  %-28s %v %s\n", p.Name, p.V, p.Unit)
		}
		if *raw {
			fmt.Fprintf(out, "  registers %v\n", e.Regs)
		}
	}
	return nil
}

func write(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("write", flag.ContinueOnError)
	var c conn
	c.flags(fs)
	unit := fs.Int("unit", 1, "Modbus unit")
	model := fs.Int("model", 0, "model ID")
	pointName := fs.String("point", "", "point name or path (WMaxLimPct, PFWInj.PF, Crv.2.Pt.1.V)")
	value := fs.String("value", "", "value in engineering units, a symbol name, or bit names joined by +")
	if err := parseFlags(fs, args, out); err != nil {
		return err
	}
	if *model == 0 || *pointName == "" || *value == "" {
		return errors.New("-model, -point and -value are required")
	}
	u, err := unitOf(*unit)
	if err != nil {
		return err
	}
	r, err := c.open(u)
	if err != nil {
		return err
	}
	defer r.Close()
	m, err := sunspec.Find(r)
	if err != nil {
		return err
	}
	inst := m.Model(*model)
	if inst == nil {
		return fmt.Errorf("the map has no model %d (it has %v)", *model, ids(m))
	}
	regs, err := sunspec.ReadModel(r, *inst)
	if err != nil {
		return err
	}
	var v any = *value
	if f, err := strconv.ParseFloat(*value, 64); err == nil {
		v = f
	} else if l, ok := sunspec.Locate(*inst, regs, *pointName); ok && strings.HasPrefix(l.Point.Type, "bitfield") {
		v = strings.Split(*value, "+")
	}
	w, err := sunspec.PlanWrite(*inst, regs, *pointName, v)
	if err != nil {
		return err
	}
	addr, words, err := w.Encode()
	if err != nil {
		return err
	}
	if err := r.WriteHolding(addr, words); err != nil {
		return fmt.Errorf("writing %v at %d: %w", words, addr, err)
	}
	after, err := sunspec.ReadModel(r, *inst)
	if err != nil {
		return err
	}
	kept := "kept"
	if !w.Kept(after) {
		kept = "NOT kept"
	}
	now := "?"
	for _, d := range sunspec.Decode(*inst, after) {
		if d.Name == *pointName || d.Path == *pointName {
			now = fmt.Sprint(d.V)
		}
	}
	fmt.Fprintf(out, "wrote %v at %d; the device %s it; %s.%s is now %s\n", words, addr, kept, inst.Name, *pointName, now)
	if !w.Kept(after) {
		return errors.New("the device did not keep the value")
	}
	return nil
}

func models(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("models", flag.ContinueOnError)
	model := fs.Int("model", 0, "show this model's points and groups")
	jsonOut := fs.Bool("json", false, "print JSON")
	if err := parseFlags(fs, args, out); err != nil {
		return err
	}
	if *model == 0 {
		known := sunspec.Known()
		if *jsonOut {
			list := make([]map[string]any, 0, len(known))
			for _, id := range known {
				d := sunspec.Lookup(id)
				list = append(list, map[string]any{"id": id, "name": d.Name, "label": d.Label, "short": sunspec.ShortName(id)})
			}
			return writeJSON(out, list)
		}
		for _, id := range known {
			d := sunspec.Lookup(id)
			fmt.Fprintf(out, "%5d  %-20s %s\n", id, sunspec.ShortName(id), d.Label)
		}
		return nil
	}
	d := sunspec.Lookup(*model)
	if d == nil {
		return fmt.Errorf("model %d is not described", *model)
	}
	if *jsonOut {
		return writeJSON(out, d)
	}
	length := fmt.Sprintf("%d registers", d.Len)
	if d.Len == 0 {
		length = fmt.Sprintf("%d fixed registers, then repeating groups", d.Fixed)
	}
	fmt.Fprintf(out, "model %d %s (%s), %s\n", d.ID, d.Name, d.Label, length)
	printPoints(out, d.Points, "  ")
	printGroups(out, d.Groups, "  ")
	return nil
}

func printPoints(out io.Writer, pts []sunspec.Point, indent string) {
	for _, p := range pts {
		extra := []string{p.Type}
		if p.SF != nil {
			extra = append(extra, fmt.Sprintf("sf %v", p.SF))
		}
		if p.Units != "" {
			extra = append(extra, p.Units)
		}
		if p.RW {
			extra = append(extra, "RW")
		}
		if len(p.Symbols) > 0 {
			names := make([]string, 0, len(p.Symbols))
			for n := range p.Symbols {
				names = append(names, n)
			}
			sort.Slice(names, func(i, j int) bool { return p.Symbols[names[i]] < p.Symbols[names[j]] })
			extra = append(extra, strings.Join(names, "|"))
		}
		fmt.Fprintf(out, "%s%3d %-16s %s\n", indent, p.Offset, p.Name, strings.Join(extra, ", "))
	}
}

func printGroups(out io.Writer, gs []sunspec.Group, indent string) {
	for _, g := range gs {
		count := "once"
		switch {
		case g.Count > 1:
			count = fmt.Sprintf("%d times", g.Count)
		case g.CountPoint != "":
			count = "× " + g.CountPoint
		case g.FromLength:
			count = "to the model's length"
		}
		size := fmt.Sprintf("%d registers each", g.Len)
		for _, sub := range g.Groups {
			if sub.Count != 1 {
				size = fmt.Sprintf("%d registers each besides its repeating groups", g.Len)
			}
		}
		fmt.Fprintf(out, "%sgroup %s (%s), %s\n", indent, g.Name, count, size)
		printPoints(out, g.Points, indent+"  ")
		printGroups(out, g.Groups, indent+"  ")
	}
}

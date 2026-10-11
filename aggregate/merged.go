// Copyright 2026 Abhinav Gautam
// SPDX-License-Identifier: Apache-2.0

package aggregate

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"
	"sync"
	"time"

	"github.com/cB-Sigma/sunspec"
	"github.com/cB-Sigma/sunspec/device"
)

// Rule says how a merged point's value is made from the devices' values
// (each in engineering units, its own scale factor applied).
type Rule int

// The rules. RuleDefault picks Sum for accumulators and for points in W,
// VA, var, A, Wh, VAh, varh and Ah; First for count points and for
// unscaled points without a unit (identifiers, counters, modes); Mean
// for the others (V, Hz, power factor, percentages, temperatures).
// Enums, strings and addresses are always the first device's; bitfields
// (alarms, events) the union of the bits set.
const (
	RuleDefault Rule = iota
	Sum
	Mean
	Min
	Max
	First
)

// WriteRule says where a write to a merged point goes.
type WriteRule int

// The write rules. WriteDefault splits a write to a point merged by Sum
// (a power setpoint: each of n devices gets 1/n) and fans out the others
// (a percentage limit, an enable, a curve point: each device gets the
// value). Refuse answers such a write with exception 2.
const (
	WriteDefault WriteRule = iota
	FanOut
	Split
	Refuse
)

// Source is one device of a Merged map.
type Source struct {
	// Name names the device in Status.
	Name string
	// Up reaches it.
	Up Upstream
}

// Copy places one device's model in the merged map as it is.
type Copy struct {
	// Source is the index of the device in Config.Sources.
	Source int
	// Model is the model ID; Nth picks its occurrence in the device's
	// map, from 1 (0 is 1).
	Model, Nth int
}

// Config shapes a Merged map.
type Config struct {
	// Base is the address of the marker (40000, 50000 or 0).
	Base uint16
	// Common are model 1's raw values ("Mn", "Md", "SN", "Vr", "DA"):
	// the aggregator's own identity. The common model is not writable.
	Common map[string]any
	// Sources are the devices.
	Sources []Source
	// Merge lists the models merged across every device that has them
	// (the first occurrence in each), in map order after the common
	// model. The first device that has the model gives its layout
	// (repeating group counts); occurrence k of a group merges with
	// occurrence k of the other devices.
	Merge []int
	// Copy lists models copied from one device each, after the merged
	// ones.
	Copy []Copy
	// Rules override the merge rule of points, by "<model>.<path>"
	// ("103.W", "160.module.1.DCW").
	Rules map[string]Rule
	// Writes override the write rule of points, by "<model>.<path>".
	Writes map[string]WriteRule
	// MaxAge is how old the devices' values may be when a client reads;
	// older ones are read again first (0: one second).
	MaxAge time.Duration
}

// SourceStatus is how a device's last read went.
type SourceStatus struct {
	Name string
	// Err is the last read's error (nil: it was read).
	Err error
	// At is when it was last read completely.
	At time.Time
}

// Merged is one SunSpec map over several devices. It is a
// device.Handler (for any unit) and safe for concurrent use: a client's
// read that finds the values older than MaxAge reads every device again
// first, holding other clients back meanwhile. A device that fails to
// answer keeps its last values in the merge (Status says so).
type Merged struct {
	cfg     Config
	mu      sync.Mutex
	im      *device.Image
	m       *sunspec.Map
	merged  []*mergedModel
	copies  []*copyModel
	srcs    []*srcState
	at      time.Time
	stale   bool
	counted map[int]map[string]bool // count points by model
}

type srcInst struct {
	src  int
	inst sunspec.Instance
}

type mergedModel struct {
	inst sunspec.Instance
	from []srcInst
}

type copyModel struct {
	inst sunspec.Instance
	from srcInst
}

type srcState struct {
	name string
	up   Upstream
	m    *sunspec.Map
	regs map[uint16][]uint16 // by model address
	want []sunspec.Instance
	err  error
	at   time.Time
}

// New reads every device's map and builds the merged map. Every device
// must answer now: the merged map's layout comes from theirs.
func New(cfg Config) (*Merged, error) {
	if len(cfg.Sources) == 0 {
		return nil, errors.New("aggregate: no sources")
	}
	if cfg.MaxAge <= 0 {
		cfg.MaxAge = time.Second
	}
	g := &Merged{cfg: cfg, counted: map[int]map[string]bool{}}
	for i, s := range cfg.Sources {
		if s.Up == nil {
			return nil, fmt.Errorf("aggregate: source %d has no upstream", i)
		}
		m, err := sunspec.Find(s.Up)
		if err != nil {
			return nil, fmt.Errorf("aggregate: source %q: %w", s.Name, err)
		}
		g.srcs = append(g.srcs, &srcState{name: s.Name, up: s.Up, m: m, regs: map[uint16][]uint16{}})
	}
	need := func(src int, inst sunspec.Instance) {
		s := g.srcs[src]
		for _, w := range s.want {
			if w.Addr == inst.Addr {
				return
			}
		}
		s.want = append(s.want, inst)
	}
	for _, id := range cfg.Merge {
		mm := &mergedModel{}
		for i, s := range g.srcs {
			if inst := s.m.Model(id); inst != nil {
				mm.from = append(mm.from, srcInst{i, *inst})
				need(i, *inst)
			}
		}
		if len(mm.from) == 0 {
			return nil, fmt.Errorf("aggregate: no source has model %d", id)
		}
		if sunspec.Lookup(id) == nil {
			return nil, fmt.Errorf("aggregate: model %d is not described; copy it instead", id)
		}
		g.merged = append(g.merged, mm)
	}
	for _, c := range cfg.Copy {
		if c.Source < 0 || c.Source >= len(g.srcs) {
			return nil, fmt.Errorf("aggregate: copy of model %d from source %d: no such source", c.Model, c.Source)
		}
		nth := max(c.Nth, 1)
		var found *sunspec.Instance
		for _, inst := range g.srcs[c.Source].m.Models {
			if inst.ID == c.Model {
				if nth--; nth == 0 {
					found = &inst
					break
				}
			}
		}
		if found == nil {
			return nil, fmt.Errorf("aggregate: source %q has no model %d (occurrence %d)", g.srcs[c.Source].name, c.Model, max(c.Nth, 1))
		}
		g.copies = append(g.copies, &copyModel{from: srcInst{c.Source, *found}})
		need(c.Source, *found)
	}
	for _, s := range g.srcs {
		if err := s.read(); err != nil {
			return nil, fmt.Errorf("aggregate: source %q: %w", s.name, err)
		}
	}

	common, _, err := device.Build(1, cfg.Common)
	if err != nil {
		return nil, fmt.Errorf("aggregate: the common model: %w", err)
	}
	im := device.NewImage(cfg.Base)
	im.Model(1, common)
	for _, mm := range g.merged {
		f := mm.from[0]
		data := append([]uint16(nil), g.srcs[f.src].regs[f.inst.Addr]...)
		addr := im.Model(uint16(f.inst.ID), data)
		mm.inst = sunspec.Instance{ID: f.inst.ID, Addr: addr, L: len(data), Def: f.inst.Def}
		for _, l := range sunspec.Layout(mm.inst, data) {
			if l.Point.RW {
				for k := 0; k < l.Point.Size; k++ {
					im.Writable(addr + uint16(l.Offset+k))
				}
			}
		}
		counted := map[string]bool{}
		var walk func(gs []sunspec.Group)
		walk = func(gs []sunspec.Group) {
			for _, gr := range gs {
				if gr.CountPoint != "" {
					counted[gr.CountPoint] = true
				}
				walk(gr.Groups)
			}
		}
		walk(mm.inst.Def.Groups)
		g.counted[mm.inst.ID] = counted
	}
	for _, c := range g.copies {
		data := g.srcs[c.from.src].regs[c.from.inst.Addr]
		addr := im.Model(uint16(c.from.inst.ID), data)
		c.inst = sunspec.Instance{ID: c.from.inst.ID, Addr: addr, L: len(data), Def: c.from.inst.Def}
	}
	if int(im.Next())+2 > 0xFFFF || int(im.Next()) < int(cfg.Base) {
		return nil, errors.New("aggregate: the merged map does not fit in the register space")
	}
	im.End()
	g.im = im
	g.m, err = sunspec.Walk(im, cfg.Base)
	if err != nil {
		return nil, err
	}
	g.combine()
	g.at = time.Now()
	return g, nil
}

// read reads every model the merge needs from the device; a model that
// fails keeps its last registers.
func (s *srcState) read() error {
	var first error
	for _, inst := range s.want {
		regs, err := sunspec.ReadModel(s.up, inst)
		if err != nil {
			if first == nil {
				first = err
			}
			continue
		}
		s.regs[inst.Addr] = regs
	}
	s.err = first
	if first == nil {
		s.at = time.Now()
	}
	return first
}

// Map is the merged map: the common model, the merged models, the copied
// ones.
func (g *Merged) Map() sunspec.Map {
	return sunspec.Map{Base: g.m.Base, Models: append([]sunspec.Instance(nil), g.m.Models...)}
}

// Status reports each device's last read.
func (g *Merged) Status() []SourceStatus {
	g.mu.Lock()
	defer g.mu.Unlock()
	out := make([]SourceStatus, len(g.srcs))
	for i, s := range g.srcs {
		out[i] = SourceStatus{Name: s.name, Err: s.err, At: s.at}
	}
	return out
}

// Refresh reads every device now (in parallel) and merges; the error
// joins the devices' errors.
func (g *Merged) Refresh() error {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.refresh()
}

func (g *Merged) refresh() error {
	errs := make([]error, len(g.srcs))
	var wg sync.WaitGroup
	for i, s := range g.srcs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := s.read(); err != nil {
				errs[i] = fmt.Errorf("source %q: %w", s.name, err)
			}
		}()
	}
	wg.Wait()
	g.combine()
	g.at, g.stale = time.Now(), false
	return errors.Join(errs...)
}

// Run refreshes every interval until ctx ends (a background poll; reads
// then rarely wait for the devices).
func (g *Merged) Run(ctx context.Context, every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			_ = g.Refresh()
		}
	}
}

// ReadHolding answers a read of the merged map (any unit).
func (g *Merged) ReadHolding(_ uint8, addr, count uint16) ([]uint16, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.stale || time.Since(g.at) > g.cfg.MaxAge {
		_ = g.refresh() // a failing device keeps its last values; Status says so
	}
	return g.im.ReadHolding(addr, count)
}

// ---- merging ----

func (g *Merged) combine() {
	for _, c := range g.copies {
		regs := g.srcs[c.from.src].regs[c.from.inst.Addr]
		data := make([]uint16, c.inst.L)
		copy(data, regs)
		g.im.Set(c.inst.Addr, data...)
	}
	for _, mm := range g.merged {
		g.combineModel(mm)
	}
}

type srcView struct {
	src    int
	inst   sunspec.Instance
	regs   []uint16
	vals   map[string]sunspec.Value
	places map[string]sunspec.Located
}

func view(inst sunspec.Instance, regs []uint16) srcView {
	v := srcView{inst: inst, regs: regs, vals: map[string]sunspec.Value{}, places: map[string]sunspec.Located{}}
	for _, x := range sunspec.Decode(inst, regs) {
		v.vals[x.Path] = x
	}
	for _, l := range sunspec.Layout(inst, regs) {
		v.places[l.Path] = l
	}
	return v
}

func (g *Merged) views(mm *mergedModel) []srcView {
	var out []srcView
	for _, f := range mm.from {
		if regs := g.srcs[f.src].regs[f.inst.Addr]; regs != nil {
			v := view(f.inst, regs)
			v.src = f.src
			out = append(out, v)
		}
	}
	return out
}

func (g *Merged) rule(id int, l sunspec.Located) Rule {
	if r, ok := g.cfg.Rules[fmt.Sprintf("%d.%s", id, l.Path)]; ok && r != RuleDefault {
		return r
	}
	p := l.Point
	switch {
	case g.counted[id][p.Name]:
		return First
	case strings.HasPrefix(p.Type, "acc"):
		return Sum
	}
	switch sunspec.Unit(p.Units) {
	case "W", "VA", "var", "A", "Wh", "VAh", "varh", "Varh", "Ah":
		return Sum
	}
	if p.Units == "" && p.SF == nil {
		return First
	}
	return Mean
}

func combineValues(r Rule, xs []float64) float64 {
	out := xs[0]
	switch r {
	case Sum, Mean:
		out = 0
		for _, x := range xs {
			out += x
		}
		if r == Mean {
			out /= float64(len(xs))
		}
	case Min:
		for _, x := range xs {
			out = math.Min(out, x)
		}
	case Max:
		for _, x := range xs {
			out = math.Max(out, x)
		}
	}
	return out
}

func numeric(typ string) bool {
	switch {
	case strings.HasPrefix(typ, "int"), strings.HasPrefix(typ, "uint"), strings.HasPrefix(typ, "acc"),
		typ == "count", strings.HasPrefix(typ, "float"):
		return true
	}
	return false
}

// sfPath finds the scale factor point a located point names: in its own
// group occurrence first, then outwards to the model's top level.
func sfPath(places map[string]sunspec.Located, l sunspec.Located) (string, bool) {
	name, ok := l.Point.SF.(string)
	if !ok {
		return "", false
	}
	segs := strings.Split(l.Path, ".")
	for i := len(segs) - 1; i >= 0; i-- {
		cand := strings.Join(append(append([]string(nil), segs[:i]...), name), ".")
		if p, ok := places[cand]; ok && p.Point.Type == "sunssf" {
			return cand, true
		}
	}
	return "", false
}

// encodeRaw encodes a raw integer for p; ok is false when it does not fit
// the type or lands on its "not implemented" value.
func encodeRaw(p *sunspec.Point, raw int64) ([]uint16, bool) {
	words, err := sunspec.EncodePoint(p, raw)
	if err != nil {
		return nil, false
	}
	if _, ok := sunspec.Raw(p.Type, words); !ok && !strings.HasPrefix(p.Type, "acc") {
		return nil, false
	}
	return words, true
}

type pending struct {
	l sunspec.Located
	v float64
}

func (g *Merged) combineModel(mm *mergedModel) {
	vs := g.views(mm)
	if len(vs) == 0 {
		return
	}
	var out []uint16
	for _, v := range vs {
		if len(v.regs) == mm.inst.L {
			out = append([]uint16(nil), v.regs...)
			break
		}
	}
	if out == nil {
		out, _ = g.im.ReadHolding(mm.inst.Addr, uint16(mm.inst.L))
	}
	ours := sunspec.Layout(mm.inst, out)
	places := map[string]sunspec.Located{}
	for _, l := range ours {
		places[l.Path] = l
	}
	var scaled []pending
	for _, l := range ours {
		p := l.Point
		switch {
		case strings.HasPrefix(p.Type, "bitfield"):
			var bits uint64
			set := false
			for _, v := range vs {
				if sl, ok := v.places[l.Path]; ok && sl.Offset+p.Size <= len(v.regs) {
					if raw, ok := sunspec.Raw(p.Type, v.regs[sl.Offset:sl.Offset+p.Size]); ok {
						bits |= uint64(raw)
						set = true
					}
				}
			}
			if set {
				if words, err := sunspec.EncodePoint(p, bits); err == nil {
					copy(out[l.Offset:], words)
				}
			}
		case numeric(p.Type):
			var xs []float64
			for _, v := range vs {
				if x, ok := v.vals[l.Path]; ok {
					if f, ok := x.V.(float64); ok {
						xs = append(xs, f)
					}
				}
			}
			r := g.rule(mm.inst.ID, l)
			if len(xs) == 0 || g.counted[mm.inst.ID][p.Name] {
				continue
			}
			c := combineValues(r, xs)
			if _, named := p.SF.(string); named && !strings.HasPrefix(p.Type, "float") {
				scaled = append(scaled, pending{l, c})
			} else {
				g.encodeFixed(out, l, c)
			}
		}
	}
	// Scale factors: for each, the finest of the devices' that lets every
	// merged value using it fit its type.
	groups := map[string][]pending{}
	var order []string
	for _, pd := range scaled {
		sp, ok := sfPath(places, pd.l)
		if !ok {
			g.encodeFixed(out, pd.l, pd.v)
			continue
		}
		if _, seen := groups[sp]; !seen {
			order = append(order, sp)
		}
		groups[sp] = append(groups[sp], pd)
	}
	for _, sp := range order {
		lo, have := sunspec.MaxSF, false
		for _, v := range vs {
			if sl, ok := v.places[sp]; ok && sl.Offset < len(v.regs) {
				x := int16(v.regs[sl.Offset])
				if x >= sunspec.MinSF && x <= sunspec.MaxSF {
					lo, have = min(lo, int(x)), true
				}
			}
		}
		if !have {
			continue
		}
		for sf := lo; sf <= sunspec.MaxSF; sf++ {
			enc := make([][]uint16, 0, len(groups[sp]))
			fits := true
			for _, pd := range groups[sp] {
				words, ok := encodeRaw(pd.l.Point, int64(math.Round(pd.v/math.Pow10(sf))))
				if !ok {
					fits = false
					break
				}
				enc = append(enc, words)
			}
			if !fits {
				continue
			}
			out[places[sp].Offset] = uint16(int16(sf))
			for i, pd := range groups[sp] {
				copy(out[pd.l.Offset:], enc[i])
			}
			break
		}
	}
	g.im.Set(mm.inst.Addr, out...)
}

// encodeFixed encodes a merged value of a point without a scale factor
// point (a float, or a fixed power of ten in the definition).
func (g *Merged) encodeFixed(out []uint16, l sunspec.Located, v float64) {
	p := l.Point
	var words []uint16
	var ok bool
	if strings.HasPrefix(p.Type, "float") {
		var err error
		words, err = sunspec.EncodePoint(p, v)
		ok = err == nil
	} else {
		pow := 0
		if f, isFixed := p.SF.(float64); isFixed {
			pow = int(f)
		}
		words, ok = encodeRaw(p, int64(math.Round(v/math.Pow10(pow))))
	}
	if ok {
		copy(out[l.Offset:], words)
	}
}

// ---- writes ----

type routed struct {
	src   int
	addr  uint16
	words []uint16
}

// WriteHolding answers a write to the merged map (any unit): to a copied
// model it goes to that device, to a merged model to every device that
// has the points written (WriteRule), point by point, in engineering
// units through each device's own scale factors. The common model and
// the registers outside the models refuse writes with exception 2. A
// write that ends inside a multi-register point is refused with
// exception 3. When a device refuses or does not answer, the others
// still get theirs (no rollback) and the client gets that device's
// exception, or exception 11.
func (g *Merged) WriteHolding(_ uint8, addr uint16, words []uint16) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if len(words) == 0 {
		return device.IllegalValue
	}
	end := int(addr) + len(words)
	in := func(inst sunspec.Instance) bool {
		return int(addr) >= int(inst.Addr) && end <= int(inst.Addr)+inst.L
	}
	for _, c := range g.copies {
		if in(c.inst) {
			s := g.srcs[c.from.src]
			err := s.up.WriteHolding(c.from.inst.Addr+(addr-c.inst.Addr), words)
			g.stale = true
			return upstreamError(err)
		}
	}
	for _, mm := range g.merged {
		if in(mm.inst) {
			return g.writeMerged(mm, addr, words)
		}
	}
	return device.IllegalAddress
}

func (g *Merged) writeMerged(mm *mergedModel, addr uint16, words []uint16) error {
	for i := range words {
		if !g.im.IsWritable(addr + uint16(i)) {
			return device.IllegalAddress
		}
	}
	cur, err := g.im.ReadHolding(mm.inst.Addr, uint16(mm.inst.L))
	if err != nil {
		return device.DeviceFailure
	}
	off := int(addr - mm.inst.Addr)
	next := append([]uint16(nil), cur...)
	copy(next[off:], words)
	vals := map[string]sunspec.Value{}
	for _, v := range sunspec.Decode(mm.inst, next) {
		vals[v.Path] = v
	}
	vs := g.views(mm)
	var plan []routed
	for _, l := range sunspec.Layout(mm.inst, next) {
		p := l.Point
		if l.Offset+p.Size <= off || l.Offset >= off+len(words) {
			continue
		}
		if l.Offset < off || l.Offset+p.Size > off+len(words) {
			return device.IllegalValue
		}
		wr := g.cfg.Writes[fmt.Sprintf("%d.%s", mm.inst.ID, l.Path)]
		if wr == WriteDefault {
			wr = FanOut
			if numeric(p.Type) && g.rule(mm.inst.ID, l) == Sum {
				wr = Split
			}
		}
		if wr == Refuse {
			return device.IllegalAddress
		}
		var targets []srcView
		for _, v := range vs {
			if _, ok := v.places[l.Path]; ok {
				targets = append(targets, v)
			}
		}
		raw := next[l.Offset : l.Offset+p.Size]
		val, decoded := vals[l.Path]
		f, isNum := val.V.(float64)
		for _, v := range targets {
			sl := v.places[l.Path]
			if decoded && isNum && numeric(p.Type) {
				x := f
				if wr == Split {
					x = f / float64(len(targets))
				}
				w, err := sunspec.PlanWrite(v.inst, v.regs, l.Path, x)
				if err != nil {
					return device.IllegalValue
				}
				a, ws, err := w.Encode()
				if err != nil {
					return device.IllegalValue
				}
				plan = append(plan, routed{v.src, a, ws})
				continue
			}
			// Enums, bitfields, strings, unscaled raw values and "not
			// implemented": the registers as written.
			plan = append(plan, routed{v.src, v.inst.Addr + uint16(sl.Offset), append([]uint16(nil), raw...)})
		}
	}
	var first error
	for _, w := range plan {
		if err := g.srcs[w.src].up.WriteHolding(w.addr, w.words); err != nil && first == nil {
			first = err
		}
	}
	g.stale = true
	if first != nil {
		return upstreamError(first)
	}
	g.im.Set(addr, words...)
	return nil
}

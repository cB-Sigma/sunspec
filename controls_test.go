// Copyright 2026 Abhinav Gautam
// SPDX-License-Identifier: Apache-2.0

package sunspec_test

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/cB-Sigma/sunspec"
)

func f(v float64) *float64 { return &v }

func TestControlCheck(t *testing.T) {
	good := []sunspec.Control{
		{Action: "connect"}, {Action: "disconnect"}, {Action: "clearPowerLimit"}, {Action: "clearPowerFactor"},
		{Action: "limitPower", Percent: f(0)}, {Action: "limitPower", Percent: f(100)},
		{Action: "setPowerFactor", PowerFactor: f(-1)}, {Action: "setPowerFactor", PowerFactor: f(0.9), Excitation: "under"},
	}
	for _, c := range good {
		if err := c.Check(); err != nil {
			t.Errorf("%+v: %v", c, err)
		}
	}
	bad := map[string]sunspec.Control{
		"percent from 0 to 100":  {Action: "limitPower", Percent: f(101)},
		"needs percent":          {Action: "limitPower"},
		"not 0":                  {Action: "setPowerFactor", PowerFactor: f(0)},
		"from -1 to 1":           {Action: "setPowerFactor", PowerFactor: f(1.1)},
		"is not over or under":   {Action: "setPowerFactor", PowerFactor: f(0.9), Excitation: "sideways"},
		"unknown SunSpec action": {Action: "reboot"},
	}
	for want, c := range bad {
		if err := c.Check(); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%+v: %v, want %q", c, err, want)
		}
	}
	if !sunspec.IsControl(map[string]any{"action": "limitPower"}) || sunspec.IsControl(map[string]any{"action": "write"}) || sunspec.IsControl("connect") {
		t.Error("IsControl")
	}
}

// mapOf builds a Map from model instances laid out back to back at 40002
// with the given lengths.
func mapOf(models ...[2]int) *sunspec.Map {
	m := &sunspec.Map{Base: 40000}
	a := 40002
	for _, x := range models {
		m.Models = append(m.Models, sunspec.Instance{ID: x[0], Addr: uint16(a + 2), L: x[1], Def: sunspec.Lookup(x[0]), Name: sunspec.ShortName(x[0])})
		a += 2 + x[1]
	}
	return m
}

// regsWith returns a regs function that answers each model with its
// length of zeros, with the given registers set (offset → value).
func regsWith(set map[int]map[int]uint16) func(sunspec.Instance) ([]uint16, error) {
	return func(inst sunspec.Instance) ([]uint16, error) {
		out := make([]uint16, inst.L)
		for o, v := range set[inst.ID] {
			if o < len(out) {
				out[o] = v
			}
		}
		return out, nil
	}
}

func planned(p sunspec.Plan) string {
	var s []string
	for _, w := range p.Writes {
		s = append(s, fmt.Sprintf("%d.%s=%d", w.Model.ID, w.Point, w.Raw))
	}
	return strings.Join(s, " ")
}

func TestPlanControl(t *testing.T) {
	sfs := regsWith(map[int]map[int]uint16{704: {51: 0xFFFD, 52: 0xFFFF}, 123: {21: 0xFFFF, 22: 0xFFFD}})
	der := mapOf([2]int{1, 66}, [2]int{704, 65}, [2]int{715, 7})
	old := mapOf([2]int{1, 66}, [2]int{123, 24})
	es := mapOf([2]int{1, 66}, [2]int{703, 17}, [2]int{704, 65})
	none := mapOf([2]int{1, 66}, [2]int{103, 50})
	both := mapOf([2]int{1, 66}, [2]int{123, 24}, [2]int{704, 65})
	for _, c := range []struct {
		m    *sunspec.Map
		c    sunspec.Control
		want string
		mod  int
	}{
		{der, sunspec.Control{Action: "connect"}, "715.OpCtl=1", 715},
		{der, sunspec.Control{Action: "disconnect"}, "715.OpCtl=0", 715},
		{old, sunspec.Control{Action: "disconnect"}, "123.Conn=0", 123},
		{es, sunspec.Control{Action: "connect"}, "703.ES=1", 703},
		{both, sunspec.Control{Action: "connect"}, "123.Conn=1", 123},
		{der, sunspec.Control{Action: "limitPower", Percent: f(60)}, "704.WMaxLimPct=600 704.WMaxLimPctEna=1", 704},
		{both, sunspec.Control{Action: "limitPower", Percent: f(60)}, "704.WMaxLimPct=600 704.WMaxLimPctEna=1", 704},
		{old, sunspec.Control{Action: "limitPower", Percent: f(75.5)}, "123.WMaxLimPct=755 123.WMaxLim_Ena=1", 123},
		{der, sunspec.Control{Action: "clearPowerLimit"}, "704.WMaxLimPctEna=0", 704},
		{old, sunspec.Control{Action: "clearPowerLimit"}, "123.WMaxLim_Ena=0", 123},
		{der, sunspec.Control{Action: "setPowerFactor", PowerFactor: f(-0.95), Excitation: "under"}, "704.PFWInj.PF=950 704.PFWInj.Ext=1 704.PFWInjEna=1", 704},
		{der, sunspec.Control{Action: "setPowerFactor", PowerFactor: f(0.9)}, "704.PFWInj.PF=900 704.PFWInj.Ext=0 704.PFWInjEna=1", 704},
		{der, sunspec.Control{Action: "clearPowerFactor"}, "704.PFWInjEna=0", 704},
		{old, sunspec.Control{Action: "setPowerFactor", PowerFactor: f(-0.9)}, "123.OutPFSet=-900 123.OutPFSet_Ena=1", 123},
		{old, sunspec.Control{Action: "clearPowerFactor"}, "123.OutPFSet_Ena=0", 123},
	} {
		p, err := sunspec.PlanControl(c.m, c.c, sfs)
		if err != nil || planned(p) != c.want || p.Model != c.mod {
			t.Errorf("%+v: %s (model %d) %v, want %s", c.c, planned(p), p.Model, err, c.want)
		}
	}
	// Notes say how the action was mapped.
	p, _ := sunspec.PlanControl(old, sunspec.Control{Action: "setPowerFactor", PowerFactor: f(0.9), Excitation: "over"}, sfs)
	if !strings.Contains(p.Note, "excitation was not used") {
		t.Errorf("note %q", p.Note)
	}
	p, _ = sunspec.PlanControl(es, sunspec.Control{Action: "connect"}, sfs)
	if !strings.Contains(p.Note, "703") {
		t.Errorf("note %q", p.Note)
	}
	// No model for the action.
	for _, c := range []sunspec.Control{{Action: "connect"}, {Action: "limitPower", Percent: f(10)}, {Action: "clearPowerFactor"}} {
		if _, err := sunspec.PlanControl(none, c, sfs); !errors.Is(err, sunspec.ErrUnsupported) {
			t.Errorf("%+v on 103 only: %v", c, err)
		}
	}
	// A bad control is refused before anything is read.
	if _, err := sunspec.PlanControl(der, sunspec.Control{Action: "limitPower", Percent: f(140)}, nil); err == nil {
		t.Error("limit 140 % planned")
	}
	// A scale factor that is not implemented counts as 0; a failed read
	// of the scale factors fails the plan.
	p, err := sunspec.PlanControl(der, sunspec.Control{Action: "limitPower", Percent: f(60)}, regsWith(map[int]map[int]uint16{704: {52: 0x8000}}))
	if err != nil || planned(p) != "704.WMaxLimPct=60 704.WMaxLimPctEna=1" {
		t.Errorf("SF not implemented: %s %v", planned(p), err)
	}
	boom := errors.New("boom")
	if _, err := sunspec.PlanControl(der, sunspec.Control{Action: "limitPower", Percent: f(60)}, func(sunspec.Instance) ([]uint16, error) { return nil, boom }); !errors.Is(err, boom) {
		t.Errorf("failed read: %v", err)
	}
	short := mapOf([2]int{704, 40})
	if _, err := sunspec.PlanControl(short, sunspec.Control{Action: "limitPower", Percent: f(60)}, sfs); err == nil || !strings.Contains(err.Error(), "shorter than its scale factor") {
		t.Errorf("short model: %v", err)
	}
}

func TestWriteEncode(t *testing.T) {
	m := mapOf([2]int{704, 65}, [2]int{123, 24}, [2]int{1, 66})
	der, old, common := *m.Model(704), *m.Model(123), *m.Model(1)
	for _, c := range []struct {
		w     sunspec.Write
		addr  uint16
		words []uint16
	}{
		{sunspec.Write{Model: der, Point: "WMaxLimPct", Raw: 600}, der.Addr + 13, []uint16{600}},
		{sunspec.Write{Model: der, Point: "PFWInj.Ext", Raw: 1}, der.Addr + 58, []uint16{1}},
		{sunspec.Write{Model: old, Point: "OutPFSet", Raw: -900}, old.Addr + 8, []uint16{0xFC7C}},
		{sunspec.Write{Model: der, Point: "WSet", Raw: -2}, der.Addr + 22, []uint16{0xFFFF, 0xFFFE}},
		{sunspec.Write{Model: der, Point: "PFWInjRvrtTms", Raw: 70000}, der.Addr + 2, []uint16{1, 4464}},
	} {
		addr, words, err := c.w.Encode()
		if err != nil || addr != c.addr || !slices.Equal(words, c.words) {
			t.Errorf("%s: %d %v %v, want %d %v", c.w.Point, addr, words, err, c.addr, c.words)
		}
	}
	str := &sunspec.Model{ID: 64010, Points: []sunspec.Point{{Name: "S", Offset: 0, Type: "string", Size: 4, RW: true}}}
	for _, c := range []struct {
		w    sunspec.Write
		want string
	}{
		{sunspec.Write{Model: der, Point: "Nope", Raw: 1}, "has no point Nope"},
		{sunspec.Write{Model: der, Point: "PF_SF", Raw: 1}, "is read-only"},
		{sunspec.Write{Model: common, Point: "Mn", Raw: 1}, "is read-only"},
		{sunspec.Write{Model: old, Point: "OutPFSet", Raw: 40000}, "does not fit OutPFSet (int16)"},
		{sunspec.Write{Model: der, Point: "WMaxLimPct", Raw: -1}, "does not fit WMaxLimPct (uint16)"},
		{sunspec.Write{Model: sunspec.Instance{ID: 704, Addr: 1, L: 10, Def: sunspec.Lookup(704)}, Point: "WMaxLimPct", Raw: 1}, "too short"},
		{sunspec.Write{Model: sunspec.Instance{ID: 64010, L: 4, Def: str}, Point: "S"}, "is not supported"},
	} {
		if _, _, err := c.w.Encode(); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: %v, want %q", c.w.Point, err, c.want)
		}
	}
}

func TestWriteReadBack(t *testing.T) {
	m := mapOf([2]int{704, 65}, [2]int{123, 24})
	der, old := *m.Model(704), *m.Model(123)
	regs := make([]uint16, 65)
	regs[13], regs[57], regs[22], regs[23] = 600, 950, 0xFFFF, 0xFFFE
	for _, c := range []struct {
		w    sunspec.Write
		want int64
	}{
		{sunspec.Write{Model: der, Point: "WMaxLimPct"}, 600},
		{sunspec.Write{Model: der, Point: "PFWInj.PF"}, 950},
		{sunspec.Write{Model: der, Point: "WSet"}, -2},
	} {
		if got, ok := c.w.ReadBack(regs); !ok || got != c.want {
			t.Errorf("%s = %d %v", c.w.Point, got, ok)
		}
	}
	oregs := make([]uint16, 24)
	oregs[8] = 0xFC7C
	if got, ok := (sunspec.Write{Model: old, Point: "OutPFSet"}).ReadBack(oregs); !ok || got != -900 {
		t.Errorf("int16 read back %d", got)
	}
	if _, ok := (sunspec.Write{Model: der, Point: "Nope"}).ReadBack(regs); ok {
		t.Error("unknown point read back")
	}
	if _, ok := (sunspec.Write{Model: der, Point: "PFWInj.PF"}).ReadBack(regs[:20]); ok {
		t.Error("short registers read back")
	}
}

// memDevice is an in-memory device that takes writes on the registers
// marked writable and may keep another value (an inverter that clamps).
type memDevice struct {
	*memReader
	writable map[uint16]bool
	keep     func(addr, v uint16) uint16
	log      []string
}

func (d *memDevice) WriteHolding(addr uint16, words []uint16) error {
	for i := range words {
		if !d.writable[addr+uint16(i)] {
			return errRefused
		}
	}
	for i, v := range words {
		a := addr + uint16(i)
		d.log = append(d.log, fmt.Sprintf("%d=%d", a, v))
		if d.keep != nil {
			v = d.keep(a, v)
		}
		d.regs[a] = v
	}
	return nil
}

// The whole control path on an in-memory device: plan, encode, write,
// read back; a device that clamps the value shows it. (The same against
// the simulator over Modbus TCP is in sunspec-sim.)
func TestControlPath(t *testing.T) {
	im := newImage(40000)
	im.add(1, filled(66, 0))
	der := filled(65, 0xFFFF)
	der[0], der[12], der[13] = 0, 0, 1000
	der[51], der[52] = 0xFFFD, 0xFFFF
	derAddr := im.add(704, der)
	ctl := filled(7, 0xFFFF)
	ctl[6] = 1
	ctlAddr := im.add(715, ctl)
	im.end()
	dev := &memDevice{memReader: &memReader{regs: im.regs, refuse: true},
		writable: map[uint16]bool{derAddr: true, derAddr + 12: true, derAddr + 13: true, derAddr + 57: true, derAddr + 58: true, ctlAddr + 6: true}}
	r := refusingReader{dev.memReader}
	m, err := sunspec.Find(r)
	if err != nil {
		t.Fatal(err)
	}
	regs := func(inst sunspec.Instance) ([]uint16, error) { return sunspec.ReadModel(r, inst) }
	apply := func(c sunspec.Control) map[string]int64 {
		t.Helper()
		p, err := sunspec.PlanControl(m, c, regs)
		if err != nil {
			t.Fatal(err)
		}
		for _, w := range p.Writes {
			addr, words, err := w.Encode()
			if err != nil {
				t.Fatal(err)
			}
			if err := dev.WriteHolding(addr, words); err != nil {
				t.Fatalf("%s: %v", w.Point, err)
			}
		}
		out := map[string]int64{}
		for _, w := range p.Writes {
			b, err := sunspec.ReadModel(r, w.Model)
			if err != nil {
				t.Fatal(err)
			}
			out[w.Point], _ = w.ReadBack(b)
		}
		return out
	}
	if got := apply(sunspec.Control{Action: "limitPower", Percent: f(60)}); got["WMaxLimPct"] != 600 || got["WMaxLimPctEna"] != 1 {
		t.Errorf("limit %v", got)
	}
	if got := apply(sunspec.Control{Action: "disconnect"}); got["OpCtl"] != 0 || dev.regs[ctlAddr+6] != 0 {
		t.Errorf("disconnect %v", got)
	}
	dev.keep = func(a, v uint16) uint16 {
		if a == derAddr+13 && v < 200 {
			return 200
		}
		return v
	}
	if got := apply(sunspec.Control{Action: "limitPower", Percent: f(5)}); got["WMaxLimPct"] != 200 {
		t.Errorf("clamped %v", got)
	}
	if !slices.Contains(dev.log, fmt.Sprintf("%d=50", derAddr+13)) {
		t.Errorf("write log %v", dev.log)
	}
}

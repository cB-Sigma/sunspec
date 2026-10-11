// Copyright 2026 Abhinav Gautam
// SPDX-License-Identifier: Apache-2.0

package aggregate_test

import (
	"errors"
	"fmt"
	"math"
	"sync/atomic"
	"testing"
	"time"

	mb "github.com/simonvetter/modbus"

	"github.com/cB-Sigma/sunspec"
	"github.com/cB-Sigma/sunspec/aggregate"
	"github.com/cB-Sigma/sunspec/device"
	"github.com/cB-Sigma/sunspec/mbtcp"
)

type inv struct {
	serial         string
	w, wsf, v, hz  int
	evt            int64
	wh             int
	mppt           bool
	limit, limitSF int
}

// inverter is a small inverter map: common, 103, (160), 704.
func inverter(t *testing.T, o inv) (*device.Image, map[int]uint16) {
	t.Helper()
	im := device.NewImage(40000)
	at := map[int]uint16{}
	add := func(id int, v map[string]any) {
		a, err := im.Add(id, v)
		if err != nil {
			t.Fatal(err)
		}
		at[id] = a
	}
	add(1, map[string]any{"Mn": "Example", "Md": "EX-10", "SN": o.serial, "DA": 1})
	add(103, map[string]any{"W": o.w, "W_SF": o.wsf, "PhVphA": o.v, "V_SF": -1, "Hz": o.hz, "Hz_SF": -2,
		"WH": o.wh, "WH_SF": 0, "St": 4, "Evt1": o.evt, "A": 100, "A_SF": -1})
	if o.mppt {
		add(160, map[string]any{"DCA_SF": -2, "DCV_SF": -1, "DCW_SF": 0, "module": []map[string]any{
			{"ID": 1, "IDStr": "PV1", "DCV": 6105, "DCW": 5100}, {"ID": 2, "IDStr": "PV2", "DCV": 6085, "DCW": 4950}}})
	}
	add(704, map[string]any{"WMaxLimPctEna": 0, "WMaxLimPct": o.limit, "WMaxLimPct_SF": o.limitSF, "WSetEna": 0, "WSet": 0, "WSet_SF": 0,
		"PF_SF": -3, "PFWInj": map[string]any{"PF": 1000, "Ext": 0}})
	im.End()
	return im, at
}

func serve(t *testing.T, h device.Handler, o device.Options) *device.Server {
	t.Helper()
	s, err := device.Listen(h, o)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func dial(t *testing.T, url string, unit uint8) *mbtcp.Reader {
	t.Helper()
	r, err := mbtcp.Open(mbtcp.Config{URL: url, Unit: unit, Timeout: 500 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	return r
}

func values(t *testing.T, r sunspec.Reader) map[string]any {
	t.Helper()
	m, err := sunspec.Find(r)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]any{}
	for _, inst := range m.Models {
		regs, err := sunspec.ReadModel(r, inst)
		if err != nil {
			t.Fatal(err)
		}
		for _, v := range sunspec.Decode(inst, regs) {
			out[fmt.Sprintf("%d.%s", inst.ID, v.Path)] = v.V
		}
	}
	return out
}

func near(got any, want float64) bool {
	f, ok := got.(float64)
	return ok && math.Abs(f-want) < 1e-6
}

// Three inverters behind one map: a common model of the aggregator's own,
// the inverter model merged (power and energy summed with a scale factor
// that fits, voltage and frequency averaged, events united, the state the
// first's), the controls merged, one inverter's MPPT model copied. Read
// and written over Modbus TCP, with the inverters themselves over Modbus
// TCP and in-process.
func TestMerged(t *testing.T) {
	a, atA := inverter(t, inv{serial: "A", w: 9800, wsf: 0, v: 2301, hz: 5001, evt: 1, wh: 100000, mppt: true, limit: 1000, limitSF: -1})
	b, atB := inverter(t, inv{serial: "B", w: 980, wsf: 1, v: 2299, hz: 5000, evt: 0, wh: 50000, limit: 100, limitSF: 0})
	c, atC := inverter(t, inv{serial: "C", w: 15000, wsf: 0, v: 2310, hz: 4999, evt: 8, wh: 25, limit: 1000, limitSF: -1})
	da, db, dc := device.NewDevice(a), device.NewDevice(b), device.NewDevice(c)
	srvB := serve(t, db, device.Options{})
	upB := dial(t, srvB.URL(), 1)
	g, err := aggregate.New(aggregate.Config{
		Base:    40000,
		Common:  map[string]any{"Mn": "Example Plant", "Md": "AGG", "SN": "P1", "DA": 1},
		Sources: []aggregate.Source{{"A", aggregate.Local(da, 1)}, {"B", upB}, {"C", aggregate.Local(dc, 1)}},
		Merge:   []int{103, 704},
		Copy:    []aggregate.Copy{{Source: 0, Model: 160}},
		MaxAge:  time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	var ids []int
	for _, inst := range g.Map().Models {
		ids = append(ids, inst.ID)
	}
	if fmt.Sprint(ids) != "[1 103 704 160]" {
		t.Fatalf("models %v", ids)
	}
	srv := serve(t, g, device.Options{})
	r := dial(t, srv.URL(), 1)
	got := values(t, r)
	for k, want := range map[string]float64{
		"103.W": 9800 + 9800 + 15000, "103.PhVphA": 230.3, "103.Hz": 50.0, "103.WH": 150025, "103.A": 30,
		"704.WMaxLimPct": 100, "160.module.2.DCV": 608.5, "160.module.1.DCW": 5100,
	} {
		if !near(got[k], want) {
			t.Errorf("%s = %v, want %v", k, got[k], want)
		}
	}
	if got["1.Mn"] != "Example Plant" || got["103.St"] != "MPPT" || fmt.Sprint(got["103.Evt1"]) != "[DC_DISCONNECT GROUND_FAULT]" {
		t.Errorf("common %v, state %v, events %v", got["1.Mn"], got["103.St"], got["103.Evt1"])
	}

	// A percentage limit fans out, each device in its own scale factor; a
	// power setpoint is split.
	m, _ := sunspec.Find(r)
	ctl := m.Model(704)
	regs, _ := sunspec.ReadModel(r, *ctl)
	for _, w := range []struct {
		point string
		v     any
	}{{"WMaxLimPct", 60}, {"WMaxLimPctEna", "ENABLED"}, {"WSet", 9000}} {
		pw, err := sunspec.PlanWrite(*ctl, regs, w.point, w.v)
		if err != nil {
			t.Fatal(err)
		}
		addr, words, _ := pw.Encode()
		if err := r.WriteHolding(addr, words); err != nil {
			t.Fatalf("write %s: %v", w.point, err)
		}
	}
	for _, c := range []struct {
		d    *device.Device
		addr uint16
		want uint16
	}{{da, atA[704] + 13, 600}, {db, atB[704] + 13, 60}, {dc, atC[704] + 13, 600}, {da, atA[704] + 12, 1}, {db, atB[704] + 12, 1},
		{da, atA[704] + 23, 3000}, {db, atB[704] + 23, 3000}, {dc, atC[704] + 23, 3000}} {
		if v := c.d.Get(c.addr); v != c.want {
			t.Errorf("register %d = %d, want %d", c.addr, v, c.want)
		}
	}
	got = values(t, r)
	if !near(got["704.WMaxLimPct"], 60) || !near(got["704.WSet"], 9000) || got["704.WMaxLimPctEna"] != "ENABLED" {
		t.Errorf("read back %v %v %v", got["704.WMaxLimPct"], got["704.WSet"], got["704.WMaxLimPctEna"])
	}

	// Refused: the common model, a read-only point, part of a two-register
	// point; the copied model's device decides for itself.
	inv103 := m.Model(103)
	for _, w := range []struct {
		addr  uint16
		words []uint16
		err   error
	}{{m.Model(1).Addr, []uint16{1}, mb.ErrIllegalDataAddress}, {inv103.Addr + 12, []uint16{1}, mb.ErrIllegalDataAddress},
		{ctl.Addr + 23, []uint16{1}, mb.ErrIllegalDataValue}, {m.Model(160).Addr + 8, []uint16{1}, mb.ErrIllegalDataAddress}} {
		if err := r.WriteHolding(w.addr, w.words); !errors.Is(err, w.err) {
			t.Errorf("write at %d: %v, want %v", w.addr, err, w.err)
		}
	}

	// A device that stops answering keeps its last values; Status says so.
	_ = srvB.Close()
	da.Set(atA[103]+12, 5000) // W of A
	if err := g.Refresh(); err == nil {
		t.Error("refresh with a device down")
	}
	st := g.Status()
	if st[1].Err == nil || st[0].Err != nil || st[2].Err != nil {
		t.Errorf("status %+v", st)
	}
	got = values(t, r)
	if !near(got["103.W"], 5000+9800+15000) {
		t.Errorf("W with B down: %v", got["103.W"])
	}
	// A write that B cannot take: A and C take it, the client is told.
	pw, _ := sunspec.PlanWrite(*ctl, regs, "WMaxLimPct", 50)
	addr, words, _ := pw.Encode()
	if err := r.WriteHolding(addr, words); !errors.Is(err, mb.ErrGWTargetFailedToRespond) {
		t.Errorf("write with B down: %v", err)
	}
	if da.Get(atA[704]+13) != 500 {
		t.Errorf("A did not take the write: %d", da.Get(atA[704]+13))
	}
}

func TestMergedConfigErrors(t *testing.T) {
	a, _ := inverter(t, inv{serial: "A", w: 1, v: 2300, hz: 5000, limit: 1000, limitSF: -1})
	up := aggregate.Local(device.NewDevice(a), 1)
	empty := device.NewImage(40000)
	empty.End()
	for _, c := range []aggregate.Config{
		{},
		{Sources: []aggregate.Source{{"A", up}}, Merge: []int{701}},
		{Sources: []aggregate.Source{{"A", up}}, Copy: []aggregate.Copy{{Source: 1, Model: 103}}},
		{Sources: []aggregate.Source{{"A", up}}, Copy: []aggregate.Copy{{Source: 0, Model: 103, Nth: 2}}},
		{Sources: []aggregate.Source{{"A", up}, {"E", aggregate.Local(device.NewDevice(empty), 1)}}},
		{Sources: []aggregate.Source{{"A", up}}, Common: map[string]any{"Nope": 1, "Mn": 7}},
	} {
		if _, err := aggregate.New(c); err == nil {
			t.Errorf("%+v accepted", c)
		}
	}
}

// Units passed through: each unit's reads and writes reach its own
// device; with MaxAge, reads inside the SunSpec map come from a snapshot
// (one poll of the device for many reads), writes reach the device and
// the snapshot shows what it kept.
func TestProxy(t *testing.T) {
	a, atA := inverter(t, inv{serial: "A", w: 9800, v: 2301, hz: 5001, limit: 1000, limitSF: -1})
	b, _ := inverter(t, inv{serial: "B", w: 4000, v: 2300, hz: 5000, limit: 1000, limitSF: -1})
	da, db := device.NewDevice(a), device.NewDevice(b)
	da.SetKeep(func(addr, v uint16) uint16 {
		if addr == atA[704]+13 {
			return min(v, 800)
		}
		return v
	})
	var reads atomic.Int64
	da.SetBefore(func(r device.Request) error {
		if !r.Write {
			reads.Add(1)
		}
		return nil
	})
	srvB := serve(t, db, device.Options{RTU: true})
	upB := dial(t, srvB.URL(), 1)
	p := aggregate.NewProxy(map[uint8]aggregate.Upstream{1: aggregate.Local(da, 1), 2: upB}, aggregate.ProxyOptions{MaxAge: time.Hour})
	srv := serve(t, p, device.Options{ASCII: true})
	r := dial(t, srv.URL(), 1)
	if got := values(t, r); got["1.SN"] != "A" || !near(got["103.W"], 9800) {
		t.Errorf("unit 1: %v %v", got["1.SN"], got["103.W"])
	}
	if got := values(t, r.ForUnit(2)); got["1.SN"] != "B" || !near(got["103.W"], 4000) {
		t.Errorf("unit 2: %v %v", got["1.SN"], got["103.W"])
	}
	before := reads.Load()
	da.Set(atA[103]+12, 1234)
	if got := values(t, r); !near(got["103.W"], 9800) || reads.Load() != before {
		t.Errorf("from the snapshot: W %v, %d device reads", got["103.W"], reads.Load()-before)
	}
	if err := r.WriteHolding(atA[704]+13, []uint16{900}); err != nil {
		t.Fatal(err)
	}
	if got, _ := r.ReadHolding(atA[704]+13, 1); got[0] != 800 {
		t.Errorf("the snapshot after a write: %v", got)
	}
	p.Invalidate()
	if got := values(t, r); !near(got["103.W"], 1234) {
		t.Errorf("after Invalidate: %v", got["103.W"])
	}
	if err := r.WriteHolding(atA[103]+12, []uint16{1}); !errors.Is(err, mb.ErrIllegalDataAddress) {
		t.Errorf("read-only: %v", err)
	}
	if _, err := r.ForUnit(3).ReadHolding(40000, 2); err == nil || r.Refused(err) {
		t.Errorf("unit 3 over ASCII: %v, want silence", err)
	}
	_ = srvB.Close()
	p.Invalidate()
	if _, err := r.ForUnit(2).ReadHolding(40000, 2); !errors.Is(err, mb.ErrGWTargetFailedToRespond) {
		t.Errorf("a device down: %v", err)
	}
	// No snapshot: every read goes through.
	p0 := aggregate.NewProxy(map[uint8]aggregate.Upstream{1: aggregate.Local(da, 1)}, aggregate.ProxyOptions{})
	if got, err := p0.ReadHolding(1, atA[103]+12, 1); err != nil || got[0] != 1234 {
		t.Errorf("live: %v %v", got, err)
	}
}

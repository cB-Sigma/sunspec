// Copyright 2026 Abhinav Gautam
// SPDX-License-Identifier: Apache-2.0

package interop

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"net"
	"os/exec"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/cB-Sigma/sunspec"
	"github.com/cB-Sigma/sunspec/aggregate"
	ssdevice "github.com/cB-Sigma/sunspec/device"
)

// The other direction: pysunspec2's client (testdata/pysunspec2_client.py)
// reads and writes maps served by this module's device and aggregate
// packages, over Modbus TCP from the container to the host.

// serveAddr is where a server listens so that the container reaches it at
// host.docker.internal.
func serveAddr() string {
	if runtime.GOOS == "darwin" {
		return "127.0.0.1:0"
	}
	return "0.0.0.0:0"
}

type pyReport struct {
	Base      int    `json:"base"`
	ScanError string `json:"scan_error"`
	Models    []struct {
		ID     int            `json:"id"`
		Addr   int            `json:"addr"`
		Len    int            `json:"len"`
		Known  bool           `json:"known"`
		Error  string         `json:"error"`
		Values map[string]any `json:"values"`
	} `json:"models"`
	Writes map[string]struct {
		OK       bool   `json:"ok"`
		ReadBack any    `json:"readBack"`
		Error    string `json:"error"`
	} `json:"writes"`
}

// pyRead runs pysunspec2's client against addr ("host:port" on this
// machine) for unit, with writes ("model:point=value", engineering units)
// after the scan.
func pyRead(t *testing.T, addr string, unit uint8, writes ...string) pyReport {
	t.Helper()
	name := needPeer(t)
	_, port, err := net.SplitHostPort(addr)
	if err != nil {
		t.Fatal(err)
	}
	args := []string{"exec", name, "python", "-I", "/sigma/pysunspec2_client.py",
		"--host", "host.docker.internal", "--port", port, "--unit", fmt.Sprint(unit)}
	for _, w := range writes {
		args = append(args, "--write", w)
	}
	var stdout, stderr bytes.Buffer
	c := exec.Command("docker", args...)
	c.Stdout, c.Stderr = &stdout, &stderr
	runErr := c.Run()
	var r pyReport
	if err := json.Unmarshal(stdout.Bytes(), &r); err != nil {
		t.Fatalf("pysunspec2 client: %v %v\n%s\n%s", runErr, err, stdout.String(), stderr.String())
	}
	if r.ScanError != "" || runErr != nil {
		t.Fatalf("pysunspec2 scan: %s %v\n%s", r.ScanError, runErr, stderr.String())
	}
	for _, m := range r.Models {
		if m.Known && m.Error != "" {
			t.Errorf("pysunspec2 reports model %d: %s", m.ID, m.Error)
		}
	}
	return r
}

func (r pyReport) value(path string) (any, bool) {
	parts := strings.Split(path, ".")
	id, _ := strconv.Atoi(parts[0])
	for _, m := range r.Models {
		if m.ID != id {
			continue
		}
		var node any = m.Values
		for _, p := range parts[1:] {
			switch n := node.(type) {
			case map[string]any:
				v, ok := n[p]
				if !ok {
					return nil, false
				}
				node = v
			case []any:
				i, err := strconv.Atoi(p)
				if err != nil || i < 1 || i > len(n) {
					return nil, false
				}
				node = n[i-1]
			default:
				return nil, false
			}
		}
		return node, true
	}
	return nil, false
}

func (r pyReport) ids() []int {
	var out []int
	for _, m := range r.Models {
		out = append(out, m.ID)
	}
	return out
}

func pyExpect(t *testing.T, r pyReport, want map[string]any) {
	t.Helper()
	for k, w := range want {
		got, ok := r.value(k)
		g, isNum := got.(float64)
		wf, wantNum := w.(float64)
		if !ok || (isNum && wantNum && math.Abs(g-wf) > 1e-9*math.Max(1, math.Abs(wf))) || (!(isNum && wantNum) && fmt.Sprint(got) != fmt.Sprint(w)) {
			t.Errorf("pysunspec2 %s = %v (present %v), want %v", k, got, ok, w)
		}
	}
	t.Logf("pysunspec2 agreed on %d values", len(want))
}

func pyWrite(t *testing.T, r pyReport, w string, ok bool, readBack float64) {
	t.Helper()
	got := r.Writes[w]
	if got.OK != ok {
		t.Errorf("pysunspec2 write %s: %+v, want ok %v", w, got, ok)
		return
	}
	if ok {
		if f, isNum := got.ReadBack.(float64); !isNum || math.Abs(f-readBack) > 1e-9 {
			t.Errorf("pysunspec2 write %s read back %v, want %v", w, got.ReadBack, readBack)
		}
	}
}

// inverterImage is an inverter laid out by the device package: common,
// three-phase inverter, DER controls, volt-var with two stored curves, a
// vendor model.
func inverterImage(t *testing.T, serial string, w int) (*ssdevice.Image, map[int]uint16) {
	t.Helper()
	im := ssdevice.NewImage(40000)
	at := map[int]uint16{}
	add := func(id int, v map[string]any) {
		a, err := im.Add(id, v)
		if err != nil {
			t.Fatal(err)
		}
		at[id] = a
	}
	add(1, map[string]any{"Mn": "Example Solar", "Md": "EX-10", "Vr": "1.0", "SN": serial, "DA": 1})
	add(103, map[string]any{"A": 145, "A_SF": -1, "PhVphA": 2301, "V_SF": -1, "W": w, "W_SF": 0, "Hz": 5001, "Hz_SF": -2,
		"WH": 100000, "WH_SF": 0, "St": 4, "Evt1": 0})
	add(704, map[string]any{"WMaxLimPctEna": 0, "WMaxLimPct": 1000, "WMaxLimPct_SF": -1, "WSetEna": 0, "WSet": 0, "WSet_SF": 0,
		"PF_SF": -3, "PFWInj": map[string]any{"PF": 1000, "Ext": 0}})
	add(705, map[string]any{"Ena": 0, "AdptCrvReq": 0, "AdptCrvRslt": 1, "V_SF": -1, "DeptRef_SF": 0, "RspTms_SF": 0, "Crv": []map[string]any{
		{"ActPt": 2, "DeptRef": 1, "ReadOnly": 1, "Pt": []map[string]any{{"V": 950, "Var": 30}, {"V": 1050, "Var": -30}}},
		{"ActPt": 2, "DeptRef": 1, "ReadOnly": 0, "Pt": []map[string]any{{"V": 950, "Var": 30}, {"V": 1050, "Var": -30}}},
	}})
	im.Model(64900, []uint16{1, 2, 3})
	im.End()
	return im, at
}

// pysunspec2 reads a map served by the device package, writes setpoints
// and a nested curve point, and is refused on a read-only point.
func TestPysunspec2ReadsDevicePackage(t *testing.T) {
	needPeer(t)
	im, at := inverterImage(t, "DEV1", 9800)
	d, err := ssdevice.Start(im, serveAddr())
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	r := pyRead(t, d.Addr(), 1, "704:WMaxLimPct=60", "704:WMaxLimPctEna=1", "705:Crv.2.Pt.1.V=97.5", "705:AdptCrvReq=2", "103:W=1")
	if r.Base != 40000 || !slices.Equal(r.ids(), []int{1, 103, 704, 705, 64900}) {
		t.Fatalf("pysunspec2 found base %d, models %v", r.Base, r.ids())
	}
	pyExpect(t, r, map[string]any{"1.SN": "DEV1", "1.Mn": "Example Solar", "103.W": 9800.0, "103.A": 14.5, "103.PhVphA": 230.1,
		"103.Hz": 50.01, "103.WH": 100000.0, "103.St": 4.0, "705.NCrv": 2.0, "705.NPt": 2.0, "705.Crv.1.Pt.2.V": 105.0, "705.Crv.2.Pt.2.Var": -30.0})
	pyWrite(t, r, "704:WMaxLimPct=60", true, 60)
	pyWrite(t, r, "704:WMaxLimPctEna=1", true, 1)
	pyWrite(t, r, "705:Crv.2.Pt.1.V=97.5", true, 97.5)
	pyWrite(t, r, "705:AdptCrvReq=2", true, 2)
	pyWrite(t, r, "103:W=1", false, 0)
	if d.Get(at[704]+13) != 600 || d.Get(at[704]+12) != 1 || d.Get(at[705]+1) != 2 {
		t.Errorf("registers: limit %d, enable %d, adopt request %d", d.Get(at[704]+13), d.Get(at[704]+12), d.Get(at[705]+1))
	}
	t.Logf("writes the device took: %v", d.WriteLog())

	// Every described model at once, laid out by the device package.
	all, _, err := ssdevice.AllModels(40000)
	if err != nil {
		t.Fatal(err)
	}
	d2, err := ssdevice.Start(all, serveAddr())
	if err != nil {
		t.Fatal(err)
	}
	defer d2.Close()
	r2 := pyRead(t, d2.Addr(), 1)
	if len(r2.Models) != len(sunspec.Known()) {
		t.Errorf("pysunspec2 found %d models, want %d", len(r2.Models), len(sunspec.Known()))
	}
}

// pysunspec2 reads and writes through the aggregate package: a merged
// map of two inverters (power summed, controls fanned out) and a proxy
// passing units through.
func TestPysunspec2ThroughAggregator(t *testing.T) {
	needPeer(t)
	a, atA := inverterImage(t, "A", 9800)
	b, atB := inverterImage(t, "B", 15000)
	da, db := ssdevice.NewDevice(a), ssdevice.NewDevice(b)
	g, err := aggregate.New(aggregate.Config{Base: 40000, Common: map[string]any{"Mn": "Example Plant", "Md": "AGG", "SN": "P1", "DA": 1},
		Sources: []aggregate.Source{{Name: "A", Up: aggregate.Local(da, 1)}, {Name: "B", Up: aggregate.Local(db, 1)}},
		Merge:   []int{103, 704}, Copy: []aggregate.Copy{{Source: 1, Model: 705}}, MaxAge: 100 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	p := aggregate.NewProxy(map[uint8]aggregate.Upstream{1: aggregate.Local(da, 1), 2: aggregate.Local(db, 1)}, aggregate.ProxyOptions{MaxAge: time.Second})
	srv, err := ssdevice.Listen(ssdevice.Mux{1: p, 2: p, 10: g}, ssdevice.Options{Listen: serveAddr()})
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()

	r := pyRead(t, srv.Addr(), 10, "704:WMaxLimPct=60", "704:WSet=9000", "705:Crv.2.Pt.1.V=97.5", "103:W=1")
	if !slices.Equal(r.ids(), []int{1, 103, 704, 705}) {
		t.Fatalf("pysunspec2 found models %v", r.ids())
	}
	pyExpect(t, r, map[string]any{"1.Mn": "Example Plant", "103.W": 24800.0, "103.PhVphA": 230.1, "103.WH": 200000.0, "103.A": 29.0})
	pyWrite(t, r, "704:WMaxLimPct=60", true, 60)
	pyWrite(t, r, "704:WSet=9000", true, 9000)
	pyWrite(t, r, "705:Crv.2.Pt.1.V=97.5", true, 97.5)
	pyWrite(t, r, "103:W=1", false, 0)
	for _, c := range []struct {
		d    *ssdevice.Device
		addr uint16
		want uint16
	}{{da, atA[704] + 13, 600}, {db, atB[704] + 13, 600}, {da, atA[704] + 23, 4500}, {db, atB[704] + 23, 4500}} {
		if got := c.d.Get(c.addr); got != c.want {
			t.Errorf("register %d = %d, want %d", c.addr, got, c.want)
		}
	}
	if got := db.Get(atB[705] + 13 + 14 + 10); got != 975 { // curve 2's first V: after the fixed part, curve 1 (10 + 2×2) and curve 2's own 10
		t.Errorf("B's curve 2 point 1 V = %d, want 975", got)
	}

	r1 := pyRead(t, srv.Addr(), 2, "704:WMaxLimPct=75")
	pyExpect(t, r1, map[string]any{"1.SN": "B", "103.W": 15000.0})
	pyWrite(t, r1, "704:WMaxLimPct=75", true, 75)
	if db.Get(atB[704]+13) != 750 {
		t.Errorf("through the proxy: %d", db.Get(atB[704]+13))
	}
}

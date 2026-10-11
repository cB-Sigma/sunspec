// Copyright 2026 Abhinav Gautam
// SPDX-License-Identifier: Apache-2.0

package interop

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cB-Sigma/sunspec"
	"github.com/cB-Sigma/sunspec/mbtcp"
)

// The peer: pysunspec2 at this version, in this image.
const (
	image          = "python:3.12.10-slim"
	pysunspec2     = "pysunspec2==1.3.6"
	pymodbus       = "pymodbus==3.8.6" // the Modbus ASCII peer: pysunspec2 has no ASCII client
	firstPort      = 5020
	containerPorts = 8
)

var (
	peerOnce sync.Once
	peerName string
	peerErr  error
	portMu   sync.Mutex
	nextPort = firstPort
)

func TestMain(m *testing.M) {
	code := m.Run()
	if peerName != "" {
		_ = exec.Command("docker", "container", "rm", "-f", peerName).Run()
	}
	os.Exit(code)
}

// needPeer starts the Python container once, with its server ports
// published on 127.0.0.1, and installs pysunspec2 in it.
func needPeer(t *testing.T) string {
	t.Helper()
	if os.Getenv("SIGMA_SUNSPEC_INTEROP") != "1" {
		t.Skip("runs pysunspec2 in Docker; set SIGMA_SUNSPEC_INTEROP=1 to run")
	}
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker is not on PATH")
	}
	peerOnce.Do(func() {
		td, err := filepath.Abs("testdata")
		if err != nil {
			peerErr = err
			return
		}
		name := fmt.Sprintf("sigma-sunspec-pysunspec2-device-%d", os.Getpid())
		args := []string{"run", "-d", "--name", name, "--add-host", "host.docker.internal:host-gateway", "-v", td + ":/sigma:ro"}
		for p := firstPort; p < firstPort+containerPorts; p++ {
			args = append(args, "-p", fmt.Sprintf("127.0.0.1::%d", p))
		}
		args = append(args, image, "sleep", "3600")
		out, err := exec.Command("docker", args...).CombinedOutput()
		if err != nil {
			peerErr = fmt.Errorf("docker run: %v: %s", err, out)
			return
		}
		peerName = name
		out, err = exec.Command("docker", "exec", name, "pip", "install", "--no-cache-dir",
			"--disable-pip-version-check", "--root-user-action=ignore", pysunspec2, pymodbus).CombinedOutput()
		if err != nil {
			peerErr = fmt.Errorf("pip install %s: %v: %s", pysunspec2, err, out)
		}
	})
	if peerErr != nil {
		t.Fatal(peerErr)
	}
	return peerName
}

// model is one model of a device spec: raw point values for pysunspec2,
// or raw registers for a model it does not describe.
type model struct {
	ID     int            `json:"id"`
	Values map[string]any `json:"values,omitempty"`
	Raw    []uint16       `json:"raw,omitempty"`
}

type spec struct {
	Base   uint16  `json:"base"`
	Unit   uint8   `json:"unit"`
	Models []model `json:"models"`
}

// device starts a pysunspec2-built device of the spec in the container
// and returns its Modbus TCP URL on the host.
func device(t *testing.T, s spec) string {
	t.Helper()
	name := needPeer(t)
	portMu.Lock()
	port := nextPort
	nextPort++
	portMu.Unlock()
	if port >= firstPort+containerPorts {
		t.Fatal("no container port left")
	}
	js, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	// Check the spec builds without pysunspec2 model errors first.
	out, err := exec.Command("docker", "exec", name, "python", "-I", "/sigma/pysunspec2_device.py",
		"--dump", "--spec", string(js)).Output()
	if err != nil {
		t.Fatalf("building the device: %v", err)
	}
	var dump struct {
		Errors []string `json:"errors"`
	}
	if err := json.Unmarshal(out, &dump); err != nil || len(dump.Errors) > 0 {
		t.Fatalf("pysunspec2 could not build the device: %v %v", err, dump.Errors)
	}
	if out, err := exec.Command("docker", "exec", "-d", name, "python", "-I", "/sigma/pysunspec2_device.py",
		"--port", fmt.Sprint(port), "--spec", string(js)).CombinedOutput(); err != nil {
		t.Fatalf("starting the device: %v %s", err, out)
	}
	out, err = exec.Command("docker", "port", name, fmt.Sprintf("%d/tcp", port)).Output()
	if err != nil {
		t.Fatalf("docker port: %v", err)
	}
	hostPort := strings.TrimSpace(strings.Split(strings.TrimSpace(string(out)), "\n")[0])
	url := "tcp://" + hostPort
	deadline := time.Now().Add(20 * time.Second)
	for {
		r, err := mbtcp.Dial(url, s.Unit, time.Second)
		if err == nil {
			_, err = r.ReadHolding(s.Base, 2)
			_ = r.Close()
			if err == nil {
				return url
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("the device at %s did not answer: %v", url, err)
		}
		time.Sleep(200 * time.Millisecond)
	}
}

func dial(t *testing.T, url string, unit uint8) *mbtcp.Reader {
	t.Helper()
	r, err := mbtcp.Dial(url, unit, 3*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	return r
}

// readAll finds the map and decodes every model as "<model>.<point>".
func readAll(t *testing.T, r *mbtcp.Reader) (*sunspec.Map, map[string]sunspec.Value) {
	t.Helper()
	m, err := sunspec.Find(r)
	if err != nil {
		t.Fatalf("Find: %v", err)
	}
	got := map[string]sunspec.Value{}
	for _, inst := range m.Models {
		regs, err := sunspec.ReadModel(r, inst)
		if err != nil {
			t.Fatal(err)
		}
		if inst.Def != nil && inst.Def.Len > 0 && inst.L != inst.Def.Len {
			t.Errorf("model %d: length %d, our table says %d", inst.ID, inst.L, inst.Def.Len)
		}
		for _, v := range sunspec.Decode(inst, regs) {
			got[inst.Name+"."+v.Name] = v
		}
	}
	return m, got
}

func ids(m *sunspec.Map) []int {
	var out []int
	for _, inst := range m.Models {
		out = append(out, inst.ID)
	}
	return out
}

// same compares a decoded value with the expected one: floats within a
// relative 1e-9, others as printed.
func same(got, want any) bool {
	if g, ok := got.(float64); ok {
		if w, ok := want.(float64); ok {
			return math.Abs(g-w) <= 1e-9*math.Max(1, math.Abs(w))
		}
	}
	return fmt.Sprint(got) == fmt.Sprint(want)
}

func expect(t *testing.T, got map[string]sunspec.Value, want map[string]any, absent ...string) {
	t.Helper()
	keys := make([]string, 0, len(want))
	for k := range want {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		v, ok := got[k]
		switch {
		case !ok:
			t.Errorf("%s: not decoded, want %v", k, want[k])
		case !same(v.V, want[k]):
			t.Errorf("%s = %v, want %v", k, v.V, want[k])
		}
	}
	for _, k := range absent {
		if v, ok := got[k]; ok {
			t.Errorf("%s holds pysunspec2's not-implemented value but was decoded: %v", k, v.V)
		}
	}
	t.Logf("checked %d values and %d not-implemented points", len(want), len(absent))
}

// apply plans a control, writes it and reads back each point.
func apply(t *testing.T, r *mbtcp.Reader, m *sunspec.Map, c sunspec.Control) map[string]int64 {
	t.Helper()
	p, err := sunspec.PlanControl(m, c, func(inst sunspec.Instance) ([]uint16, error) { return sunspec.ReadModel(r, inst) })
	if err != nil {
		t.Fatalf("%+v: %v", c, err)
	}
	for _, w := range p.Writes {
		addr, words, err := w.Encode()
		if err != nil {
			t.Fatal(err)
		}
		if err := r.WriteHolding(addr, words); err != nil {
			t.Fatalf("writing %d.%s at %d: %v", w.Model.ID, w.Point, addr, err)
		}
	}
	out := map[string]int64{}
	for _, w := range p.Writes {
		regs, err := sunspec.ReadModel(r, w.Model)
		if err != nil {
			t.Fatal(err)
		}
		out[fmt.Sprintf("%d.%s", w.Model.ID, w.Point)], _ = w.ReadBack(regs)
	}
	return out
}

func wantWrites(t *testing.T, what string, got map[string]int64, want map[string]int64) {
	t.Helper()
	for k, w := range want {
		if g, ok := got[k]; !ok || g != w {
			t.Errorf("%s: %s read back %d (planned %v), want %d", what, k, g, ok, w)
		}
	}
	if len(got) != len(want) {
		t.Errorf("%s: wrote %v, want %v", what, got, want)
	}
}

// A three-phase inverter with the IEEE 1547-2018 DER controls (704, 715),
// MPPT inputs (160) and a vendor model, at 40000 on unit 1: discovery,
// identity, scale factors, enums and bitfields, repeating groups,
// pysunspec2's not-implemented values, and the controls written to the
// points pysunspec2's definitions mark read-write.
func TestInverterWithDERControls(t *testing.T) {
	url := device(t, spec{Base: 40000, Unit: 1, Models: []model{
		{ID: 1, Values: map[string]any{"Mn": "Example Solar", "Md": "ES-10K-3P", "Vr": "1.0.0", "SN": "DEMO000001", "DA": 1}},
		{ID: 103, Values: map[string]any{"A": 145, "AphA": 48, "A_SF": -1, "PhVphA": 2301, "PhVphB": 2299, "V_SF": -1,
			"W": 980, "W_SF": 1, "Hz": 5001, "Hz_SF": -2, "WH": 100000, "WH_SF": 0, "DCW": 10050, "DCW_SF": 0,
			"TmpCab": 452, "Tmp_SF": -1, "St": 4, "Evt1": 0b10100}},
		{ID: 120, Values: map[string]any{"DERTyp": 4, "WRtg": 10000, "WRtg_SF": 0}},
		{ID: 160, Values: map[string]any{"DCA_SF": -2, "DCV_SF": -1, "DCW_SF": 0, "N": 2, "module": []map[string]any{
			{"ID": 1, "IDStr": "PV1", "DCA": 835, "DCV": 6105, "DCW": 5100},
			{"ID": 2, "IDStr": "PV2", "DCA": 812, "DCV": 6085, "DCW": 4950}}}},
		{ID: 704, Values: map[string]any{"PFWInjEna": 0, "WMaxLimPctEna": 0, "WMaxLimPct": 1000, "PF_SF": -3, "WMaxLimPct_SF": -1,
			"PFWInj": map[string]any{"PF": 1000, "Ext": 0}}},
		{ID: 715, Values: map[string]any{"OpCtl": 1}},
		{ID: 64900, Raw: []uint16{1, 2, 3}},
	}})
	r := dial(t, url, 1)
	m, got := readAll(t, r)
	if m.Base != 40000 || !slices.Equal(ids(m), []int{1, 103, 120, 160, 704, 715, 64900}) {
		t.Fatalf("map at %d: %v", m.Base, ids(m))
	}
	if unk := m.Model(64900); unk.Def != nil || unk.L != 3 {
		t.Errorf("vendor model %+v", unk)
	}
	id, err := sunspec.ReadIdentity(r, m)
	if err != nil || id != (sunspec.Identity{Manufacturer: "Example Solar", Model: "ES-10K-3P", Version: "1.0.0", Serial: "DEMO000001", DeviceAddress: 1}) {
		t.Errorf("identity %+v %v", id, err)
	}
	expect(t, got, map[string]any{
		"inverter.A": 14.5, "inverter.AphA": 4.8, "inverter.PhVphA": 230.1, "inverter.PhVphB": 229.9,
		"inverter.W": 9800.0, "inverter.Hz": 50.01, "inverter.WH": 100000.0, "inverter.DCW": 10050.0,
		"inverter.TmpCab": 45.2, "inverter.St": "MPPT", "inverter.Evt1": "[AC_DISCONNECT GRID_DISCONNECT]",
		"nameplate.DERTyp": "PV", "nameplate.WRtg": 10000.0,
		"mppt.N": 2.0, "mppt.1.ID": 1.0, "mppt.1.IDStr": "PV1", "mppt.1.DCA": 8.35, "mppt.1.DCV": 610.5, "mppt.1.DCW": 5100.0,
		"mppt.2.IDStr": "PV2", "mppt.2.DCA": 8.12, "mppt.2.DCV": 608.5, "mppt.2.DCW": 4950.0,
		"derControls.WMaxLimPct": 100.0, "derControls.WMaxLimPctEna": "DISABLED", "derControls.PFWInj.PF": 1.0,
		"derControl.OpCtl": "START",
	}, "inverter.PhVphC", "inverter.VA", "inverter.VAr", "inverter.PF", "inverter.TmpSnk", "inverter.DCA",
		"nameplate.VARtg", "mppt.1.DCWH", "mppt.1.Tms", "derControls.WSet", "derControls.PFWAbs.PF", "derControl.DERHb", "common.Opt")

	wantWrites(t, "limitPower 60", apply(t, r, m, sunspec.Control{Action: sunspec.ActionLimitPower, Percent: ptr(60)}),
		map[string]int64{"704.WMaxLimPct": 600, "704.WMaxLimPctEna": 1})
	wantWrites(t, "setPowerFactor -0.95 under", apply(t, r, m, sunspec.Control{Action: sunspec.ActionSetPowerFactor, PowerFactor: ptr(-0.95), Excitation: "under"}),
		map[string]int64{"704.PFWInj.PF": 950, "704.PFWInj.Ext": 1, "704.PFWInjEna": 1})
	wantWrites(t, "disconnect", apply(t, r, m, sunspec.Control{Action: sunspec.ActionDisconnect}), map[string]int64{"715.OpCtl": 0})
	wantWrites(t, "clearPowerLimit", apply(t, r, m, sunspec.Control{Action: sunspec.ActionClearPowerLimit}), map[string]int64{"704.WMaxLimPctEna": 0})
	_, got = readAll(t, r)
	expect(t, got, map[string]any{"derControls.WMaxLimPct": 60.0, "derControls.PFWInj.PF": 0.95, "derControl.OpCtl": "STOP"})
	// A point pysunspec2's definitions mark read-only is refused.
	if err := r.WriteHolding(m.Model(103).Addr+12, []uint16{1}); !r.Refused(err) {
		t.Errorf("write to inverter.W: %v, want exception 2", err)
	}
}

// Single-phase integer and float inverters, a meter, the DER measurement,
// capacity and storage models and the older controls model 123, at 50000
// on unit 126: negative integers, 32- and 64-bit values, float32 and its
// not-implemented NaN.
func TestMixedModelsAt50000(t *testing.T) {
	url := device(t, spec{Base: 50000, Unit: 126, Models: []model{
		{ID: 1, Values: map[string]any{"Mn": "Example Power", "Md": "EP-5K", "SN": "X-0042", "DA": 126}},
		{ID: 101, Values: map[string]any{"A": 52, "A_SF": -1, "PhVphA": 2405, "V_SF": -1, "W": -150, "W_SF": 0,
			"Hz": 4998, "Hz_SF": -2, "PF": -95, "PF_SF": -2, "WH": 123456789, "WH_SF": 0, "St": 2}},
		{ID: 111, Values: map[string]any{"W": 1234.5, "Hz": 50.0, "PhVphA": 231.25, "PF": -0.75, "St": 4}},
		{ID: 201, Values: map[string]any{"A": 105, "A_SF": -1, "PhV": 2302, "V_SF": -1, "Hz": 5000, "Hz_SF": -2,
			"W": -2500, "W_SF": 0, "PF": 98, "PF_SF": -2, "TotWhExp": 987654, "TotWhImp": 4321, "TotWh_SF": 0, "Evt": 0}},
		{ID: 701, Values: map[string]any{"ConnSt": 1, "W": 5000, "W_SF": 0, "Hz": 50010, "Hz_SF": -3,
			"TotWhInj": int64(12345678901), "TotWh_SF": 0, "LNV": 2301, "V_SF": -1, "TmpCab": 41, "Tmp_SF": 0, "ThrotPct": 80}},
		{ID: 702, Values: map[string]any{"WMaxRtg": 5000, "W_SF": 0, "VNomRtg": 230, "V_SF": 0}},
		{ID: 713, Values: map[string]any{"WHRtg": 10000, "WH_SF": 0, "SoC": 655, "SoH": 980, "Pct_SF": -1}},
		{ID: 123, Values: map[string]any{"Conn": 1, "WMaxLimPct": 1000, "WMaxLim_Ena": 0, "OutPFSet_Ena": 0,
			"WMaxLimPct_SF": -1, "OutPFSet_SF": -3}},
	}})
	r := dial(t, url, 126)
	m, got := readAll(t, r)
	if m.Base != 50000 || !slices.Equal(ids(m), []int{1, 101, 111, 201, 701, 702, 713, 123}) {
		t.Fatalf("map at %d: %v", m.Base, ids(m))
	}
	expect(t, got, map[string]any{
		"common.DA":  126.0,
		"inverter.A": 5.2, "inverter.PhVphA": 240.5, "inverter.W": -150.0, "inverter.Hz": 49.98, "inverter.PF": -0.95,
		"inverter.WH": 123456789.0, "inverter.St": "SLEEPING",
		"inverter.2.W": 1234.5, "inverter.2.Hz": 50.0, "inverter.2.PhVphA": 231.25, "inverter.2.PF": -0.75, "inverter.2.St": "MPPT",
		"meter.A": 10.5, "meter.PhV": 230.2, "meter.Hz": 50.0, "meter.W": -2500.0, "meter.PF": 0.98,
		"meter.TotWhExp": 987654.0, "meter.TotWhImp": 4321.0,
		"derAc.W": 5000.0, "derAc.Hz": 50.01, "derAc.TotWhInj": 12345678901.0, "derAc.LNV": 230.1, "derAc.TmpCab": 41.0,
		"derAc.ThrotPct":      80.0,
		"derCapacity.WMaxRtg": 5000.0, "derCapacity.VNomRtg": 230.0,
		"derStorage.WHRtg": 10000.0, "derStorage.SoC": 65.5, "derStorage.SoH": 98.0,
		"controls.WMaxLimPct": 100.0, "controls.Conn": "CONNECT",
	}, "inverter.2.A", "inverter.2.VA", "inverter.2.WH", "meter.VA", "meter.TotVAhExp", "derAc.VA", "derAc.TotVarhInj",
		"derAc.MnAlrmInfo", "derCapacity.VAMaxRtg", "derStorage.WHAvail", "controls.OutPFSet", "inverter.VA")
	if a := m.Model(111); a.Name != "inverter.2" {
		t.Errorf("second inverter model named %q", a.Name)
	}

	wantWrites(t, "limitPower 75.5", apply(t, r, m, sunspec.Control{Action: sunspec.ActionLimitPower, Percent: ptr(75.5)}),
		map[string]int64{"123.WMaxLimPct": 755, "123.WMaxLim_Ena": 1})
	wantWrites(t, "setPowerFactor -0.9", apply(t, r, m, sunspec.Control{Action: sunspec.ActionSetPowerFactor, PowerFactor: ptr(-0.9)}),
		map[string]int64{"123.OutPFSet": -900, "123.OutPFSet_Ena": 1})
	wantWrites(t, "disconnect", apply(t, r, m, sunspec.Control{Action: sunspec.ActionDisconnect}), map[string]int64{"123.Conn": 0})
	_, got = readAll(t, r)
	expect(t, got, map[string]any{"controls.WMaxLimPct": 75.5, "controls.OutPFSet": -0.9, "controls.Conn": "DISCONNECT"})

	// Another unit gets exception 11: a refusal, so not a SunSpec device
	// there rather than a dead link.
	if _, err := sunspec.Find(dial(t, url, 1)); !errors.Is(err, sunspec.ErrNotSunSpec) {
		t.Errorf("unit 1: %v", err)
	}
}

// A map at address 0, found after the device refused 40000 and 50000; a
// three-phase float inverter, a three-phase meter and the status model
// with 64-bit accumulators and a string.
func TestMapAtZero(t *testing.T) {
	url := device(t, spec{Base: 0, Unit: 1, Models: []model{
		{ID: 1, Values: map[string]any{"Mn": "Example Meter Co", "Md": "M3", "SN": "Z0"}},
		{ID: 113, Values: map[string]any{"W": 9876.5, "PhVphA": 230.5, "PhVphB": 229.75, "PhVphC": 231.0, "St": 4}},
		{ID: 203, Values: map[string]any{"W": 3000, "WphA": 1000, "WphB": 1100, "WphC": 900, "W_SF": 0, "Hz": 4999, "Hz_SF": -2}},
		{ID: 122, Values: map[string]any{"PVConn": 0b11, "ActWh": int64(5000000000), "TmSrc": "NTP", "Tms": 123456}},
	}})
	r := dial(t, url, 1)
	m, got := readAll(t, r)
	if m.Base != 0 || !slices.Equal(ids(m), []int{1, 113, 203, 122}) {
		t.Fatalf("map at %d: %v", m.Base, ids(m))
	}
	expect(t, got, map[string]any{
		"common.Mn": "Example Meter Co", "common.Md": "M3", "common.SN": "Z0",
		"inverter.W": 9876.5, "inverter.PhVphA": 230.5, "inverter.PhVphB": 229.75, "inverter.PhVphC": 231.0, "inverter.St": "MPPT",
		"meter.W": 3000.0, "meter.WphA": 1000.0, "meter.WphB": 1100.0, "meter.WphC": 900.0, "meter.Hz": 49.99,
		"status.ActWh": 5000000000.0, "status.TmSrc": "NTP", "status.Tms": 123456.0,
	}, "common.DA", "inverter.A", "inverter.Hz", "meter.A", "status.ActVAh", "status.WAval")
	if v := got["status.PVConn"]; v.Raw != 3 {
		t.Errorf("status.PVConn raw %d (%v), want 3", v.Raw, v.V)
	}
}

// A Modbus device whose marker is at none of the three base addresses.
func TestNoSunSpecMap(t *testing.T) {
	url := device(t, spec{Base: 1000, Unit: 1, Models: []model{{ID: 1, Values: map[string]any{"Mn": "Elsewhere"}}}})
	if _, err := sunspec.Find(dial(t, url, 1)); !errors.Is(err, sunspec.ErrNotSunSpec) {
		t.Errorf("Find: %v, want ErrNotSunSpec", err)
	}
}

func ptr(v float64) *float64 { return &v }

type pyPoint struct {
	N   string           `json:"n"`
	O   int              `json:"o"`
	T   string           `json:"t"`
	S   int              `json:"s"`
	SF  any              `json:"sf"`
	RW  bool             `json:"rw"`
	M   bool             `json:"m"`
	St  bool             `json:"st"`
	Sym map[string]int64 `json:"sym"`
}

type pyGroup struct {
	Name   string    `json:"name"`
	Off    int       `json:"off"`
	Len    int       `json:"len"`
	Count  any       `json:"count"`
	Points []pyPoint `json:"points"`
	Groups []pyGroup `json:"groups"`
}

type pyModel struct {
	Name    string    `json:"name"`
	Label   string    `json:"label"`
	Fixed   int       `json:"fixed"`
	Repeats bool      `json:"repeats"`
	Points  []pyPoint `json:"points"`
	Groups  []pyGroup `json:"groups"`
}

// compareGroups compares groups (and their nested groups) with
// pysunspec2's: place, length, count and points.
func compareGroups(t *testing.T, where string, ours []sunspec.Group, theirs []pyGroup) int {
	t.Helper()
	points := 0
	if len(ours) != len(theirs) {
		t.Errorf("%s: %d groups, pysunspec2 %d", where, len(ours), len(theirs))
	}
	for i, g := range ours {
		if i >= len(theirs) {
			break
		}
		q := theirs[i]
		count := ""
		switch {
		case g.CountPoint != "":
			count = g.CountPoint
		case g.FromLength:
			count = "0"
		case g.Count > 1:
			count = fmt.Sprint(g.Count)
		}
		qc := ""
		if q.Count != nil && fmt.Sprint(q.Count) != "1" {
			qc = fmt.Sprint(q.Count)
		}
		if q.Name != g.Name || q.Off != g.Offset || q.Len != g.Len || qc != count {
			t.Errorf("%s.%s: at %d len %d count %q, pysunspec2 %s at %d len %d count %q", where, g.Name, g.Offset, g.Len, count, q.Name, q.Off, q.Len, qc)
		}
		comparePoints(t, where+"."+g.Name, g.Points, q.Points)
		points += len(g.Points) + compareGroups(t, where+"."+g.Name, g.Groups, q.Groups)
	}
	return points
}

// Our symbol corrections of the source data (see NOTICE): pysunspec2
// carries the source's names.
var corrected = map[string]bool{"111.St": true}

func comparePoints(t *testing.T, where string, ours []sunspec.Point, theirs []pyPoint) {
	t.Helper()
	byName := map[string]pyPoint{}
	for _, p := range theirs {
		byName[p.N] = p
	}
	seen := map[string]bool{}
	for _, p := range ours {
		seen[p.Name] = true
		q, ok := byName[p.Name]
		if !ok {
			t.Errorf("%s.%s: pysunspec2 has no such point", where, p.Name)
			continue
		}
		sf := fmt.Sprint(p.SF)
		if p.SF == nil {
			sf = "<nil>"
		}
		if q.O != p.Offset || q.T != p.Type || q.S != p.Size || fmt.Sprint(q.SF) != sf || q.RW != p.RW || q.M != p.M || q.St != p.Static {
			t.Errorf("%s.%s: ours o=%d t=%s s=%d sf=%s rw=%v m=%v st=%v, pysunspec2 o=%d t=%s s=%d sf=%v rw=%v m=%v st=%v",
				where, p.Name, p.Offset, p.Type, p.Size, sf, p.RW, p.M, p.Static, q.O, q.T, q.S, q.SF, q.RW, q.M, q.St)
		}
		if !corrected[where+"."+p.Name] && fmt.Sprint(sortedSym(p.Symbols)) != fmt.Sprint(sortedSym(q.Sym)) {
			t.Errorf("%s.%s: symbols ours %v, pysunspec2 %v", where, p.Name, p.Symbols, q.Sym)
		}
	}
	for _, q := range theirs {
		if !seen[q.N] {
			t.Errorf("%s.%s: in pysunspec2's definition, not in ours", where, q.N)
		}
	}
}

func sortedSym(m map[string]int64) []string {
	var out []string
	for k, v := range m {
		out = append(out, fmt.Sprintf("%s=%d", k, v))
	}
	sort.Strings(out)
	return out
}

// Every model of our table against pysunspec2's own copy of the SunSpec
// model definitions: lengths, every point's offset, type, size, scale
// factor, access, mandatory and static flags and symbols, and the groups.
func TestModelTablesAgainstPysunspec2(t *testing.T) {
	name := needPeer(t)
	var idList []string
	for _, id := range sunspec.Known() {
		idList = append(idList, fmt.Sprint(id))
	}
	out, err := exec.Command("docker", "exec", name, "python", "-I", "/sigma/pysunspec2_device.py",
		"--defs", strings.Join(idList, ",")).Output()
	if err != nil {
		t.Fatalf("pysunspec2 definitions: %v", err)
	}
	var defs map[string]pyModel
	if err := json.Unmarshal(out, &defs); err != nil {
		t.Fatal(err)
	}
	points := 0
	for _, id := range sunspec.Known() {
		ours := sunspec.Lookup(id)
		py, ok := defs[fmt.Sprint(id)]
		if !ok {
			t.Errorf("model %d: pysunspec2 has no definition", id)
			continue
		}
		where := fmt.Sprint(id)
		if ours.Name != py.Name || ours.Label != strings.TrimSpace(py.Label) {
			t.Errorf("model %d: name %q label %q, pysunspec2 %q %q", id, ours.Name, ours.Label, py.Name, py.Label)
		}
		comparePoints(t, where, ours.Points, py.Points)
		points += len(ours.Points)
		total := py.Fixed
		for _, g := range py.Groups {
			if g.Count == nil || fmt.Sprint(g.Count) == "1" {
				total += g.Len
			}
		}
		switch {
		case py.Repeats && ours.Len > 0, !py.Repeats && ours.Len == 0:
			t.Errorf("model %d: length %d, fixed %d; pysunspec2 has repeating groups: %v", id, ours.Len, ours.Fixed, py.Repeats)
		case ours.Len > 0 && ours.Len != total:
			t.Errorf("model %d: length %d, pysunspec2 %d", id, ours.Len, total)
		case ours.Len == 0 && ours.Fixed != py.Fixed:
			t.Errorf("model %d: fixed part %d, pysunspec2 %d", id, ours.Fixed, py.Fixed)
		}
		points += compareGroups(t, where, ours.Groups, py.Groups)
	}
	t.Logf("compared %d models and %d points with pysunspec2's definitions", len(sunspec.Known()), points)
}

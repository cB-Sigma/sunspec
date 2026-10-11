// Copyright 2026 Abhinav Gautam
// SPDX-License-Identifier: Apache-2.0

package interop

import (
	"encoding/json"
	"fmt"
	"net"
	"os/exec"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/cB-Sigma/sunspec"
	ssdevice "github.com/cB-Sigma/sunspec/device"
	"github.com/cB-Sigma/sunspec/mbtcp"
)

// Modbus ASCII against pymodbus (testdata/pymodbus_ascii.py), both ways:
// pymodbus's ASCII client walks a map the device package serves with
// ASCII framing over TCP and writes to it; this module's ASCII client
// (mbtcp, asciiovertcp://) finds, decodes and writes a map pymodbus
// serves with ASCII framing.
func TestASCIIWithPymodbus(t *testing.T) {
	name := needPeer(t)
	im, at := inverterImage(t, "ASC1", 9800)
	all, err := im.ReadHolding(im.Base, im.Next()-im.Base)
	if err != nil {
		t.Fatal(err)
	}

	// pymodbus's client against our server.
	d, err := ssdevice.StartOptions(im, ssdevice.Options{Listen: serveAddr(), ASCII: true})
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	_, port, _ := net.SplitHostPort(d.Addr())
	limit, enaLimit := at[704]+13, at[704]+12
	w1, w16 := fmt.Sprintf("%d=650", limit), fmt.Sprintf("%d=1,700", enaLimit)
	out, err := exec.Command("docker", "exec", name, "python", "-I", "/sigma/pymodbus_ascii.py", "client",
		"--port", port, "--unit", "1", "--write", w1, "--write", w16, "--write", fmt.Sprintf("%d=1", at[103]+12)).Output()
	if err != nil {
		t.Fatalf("pymodbus client: %v %s", err, out)
	}
	var rep struct {
		Error  string `json:"error"`
		Models []struct {
			ID   int      `json:"id"`
			Addr int      `json:"addr"`
			Len  int      `json:"len"`
			Data []uint16 `json:"data"`
		} `json:"models"`
		Writes map[string]struct {
			OK       bool     `json:"ok"`
			ReadBack []uint16 `json:"readBack"`
		} `json:"writes"`
	}
	if err := json.Unmarshal(out, &rep); err != nil || rep.Error != "" {
		t.Fatalf("pymodbus client: %v %s\n%s", err, rep.Error, out)
	}
	var ids []int
	for _, m := range rep.Models {
		ids = append(ids, m.ID)
		start := m.Addr - int(im.Base)
		if !slices.Equal(m.Data, all[start:start+m.Len]) {
			t.Errorf("model %d: pymodbus read other registers than served", m.ID)
		}
	}
	if !slices.Equal(ids, []int{1, 103, 704, 705, 64900}) {
		t.Errorf("pymodbus walked %v", ids)
	}
	if w := rep.Writes[w1]; !w.OK || !slices.Equal(w.ReadBack, []uint16{650}) {
		t.Errorf("function 6: %+v", w)
	}
	if w := rep.Writes[w16]; !w.OK || !slices.Equal(w.ReadBack, []uint16{1, 700}) || d.Get(limit) != 700 {
		t.Errorf("function 16: %+v", w)
	}
	if w := rep.Writes[fmt.Sprintf("%d=1", at[103]+12)]; w.OK {
		t.Errorf("pymodbus wrote a read-only register: %+v", w)
	}
	t.Logf("pymodbus walked %d models over ASCII and wrote %v", len(rep.Models), d.WriteLog())

	// Our client against pymodbus's server.
	regs := map[string]uint16{}
	for i, w := range all {
		regs[fmt.Sprint(int(im.Base)+i)] = w
	}
	js, _ := json.Marshal(map[string]any{"unit": 3, "regs": regs})
	portMu.Lock()
	cport := nextPort
	nextPort++
	portMu.Unlock()
	if cport >= firstPort+containerPorts {
		t.Fatal("no container port left")
	}
	if out, err := exec.Command("docker", "exec", "-d", name, "python", "-I", "/sigma/pymodbus_ascii.py", "server",
		"--port", fmt.Sprint(cport), "--spec", string(js)).CombinedOutput(); err != nil {
		t.Fatalf("starting the pymodbus server: %v %s", err, out)
	}
	pout, err := exec.Command("docker", "port", name, fmt.Sprintf("%d/tcp", cport)).Output()
	if err != nil {
		t.Fatal(err)
	}
	url := "asciiovertcp://" + strings.TrimSpace(strings.Split(strings.TrimSpace(string(pout)), "\n")[0])
	var r *mbtcp.Reader
	deadline := time.Now().Add(20 * time.Second)
	for {
		r, err = mbtcp.Open(mbtcp.Config{URL: url, Unit: 3, Timeout: 2 * time.Second})
		if err == nil {
			if _, err = r.ReadHolding(im.Base, 2); err == nil {
				break
			}
			_ = r.Close()
		}
		if time.Now().After(deadline) {
			t.Fatalf("the pymodbus server did not answer: %v", err)
		}
		time.Sleep(300 * time.Millisecond)
	}
	defer r.Close()
	m, err := sunspec.Find(r)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]any{}
	for _, inst := range m.Models {
		data, err := sunspec.ReadModel(r, inst)
		if err != nil {
			t.Fatal(err)
		}
		for _, v := range sunspec.Decode(inst, data) {
			got[fmt.Sprintf("%d.%s", inst.ID, v.Path)] = v.V
		}
	}
	for k, want := range map[string]any{"1.SN": "ASC1", "103.W": 9800.0, "103.PhVphA": 230.1, "705.Crv.2.Pt.2.Var": -30.0} {
		if got[k] != want {
			t.Errorf("over ASCII from pymodbus: %s = %v, want %v", k, got[k], want)
		}
	}
	if err := r.WriteHolding(limit, []uint16{550}); err != nil {
		t.Errorf("function 6 to pymodbus: %v", err)
	}
	if err := r.WriteHolding(enaLimit, []uint16{0, 450}); err != nil {
		t.Errorf("function 16 to pymodbus: %v", err)
	}
	if back, err := r.ReadHolding(enaLimit, 2); err != nil || !slices.Equal(back, []uint16{0, 450}) {
		t.Errorf("read back from pymodbus: %v %v", back, err)
	}
	if _, err := r.ReadHolding(im.Next()+10, 1); !r.Refused(err) {
		t.Errorf("pymodbus outside its registers: %v, want an exception", err)
	}
	t.Logf("mbtcp found %d models served by pymodbus over ASCII", len(m.Models))
}

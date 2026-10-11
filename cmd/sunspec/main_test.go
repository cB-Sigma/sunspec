// Copyright 2026 Abhinav Gautam
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"net"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	mb "github.com/simonvetter/modbus"
)

// device serves holding registers for one unit; every register takes
// writes.
type device struct {
	mu   sync.Mutex
	unit uint8
	regs map[uint16]uint16
}

func (d *device) HandleHoldingRegisters(r *mb.HoldingRegistersRequest) ([]uint16, error) {
	if r.UnitId != d.unit {
		return nil, mb.ErrGWTargetFailedToRespond
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if r.IsWrite {
		for i, v := range r.Args {
			d.regs[r.Addr+uint16(i)] = v
		}
		return nil, nil
	}
	out := make([]uint16, r.Quantity)
	for i := range out {
		v, ok := d.regs[r.Addr+uint16(i)]
		if !ok {
			return nil, mb.ErrIllegalDataAddress
		}
		out[i] = v
	}
	return out, nil
}

func (d *device) HandleInputRegisters(*mb.InputRegistersRequest) ([]uint16, error) {
	return nil, mb.ErrIllegalFunction
}
func (d *device) HandleCoils(*mb.CoilsRequest) ([]bool, error) { return nil, mb.ErrIllegalFunction }
func (d *device) HandleDiscreteInputs(*mb.DiscreteInputsRequest) ([]bool, error) {
	return nil, mb.ErrIllegalFunction
}

// serve lays out the marker, model 1 ("Maker"), model 705 with two
// curves of two points and the end model at 40000.
func serve(t *testing.T) string {
	t.Helper()
	regs := map[uint16]uint16{40000: 0x5375, 40001: 0x6E53}
	a := uint16(40002)
	add := func(id uint16, data []uint16) {
		regs[a], regs[a+1] = id, uint16(len(data))
		for i, w := range data {
			regs[a+2+uint16(i)] = w
		}
		a += 2 + uint16(len(data))
	}
	common := make([]uint16, 66)
	common[0], common[16], common[48], common[64] = 0x4D61, 0x4D31, 0x5331, 1 // "Ma", "M1", "S1", DA 1
	add(1, common)
	vv := make([]uint16, 13+2*(10+4))
	for i := range vv {
		vv[i] = 0xFFFF
	}
	vv[0], vv[1], vv[2], vv[3], vv[4], vv[10], vv[11], vv[12] = 0, 0, 1, 2, 2, 0xFFFF, 0, 0 // NPt 2, NCrv 2, V_SF -1
	for k := 0; k < 2; k++ {
		c := 13 + 14*k
		vv[c], vv[c+1], vv[c+9] = 2, 1, uint16(1-k)
		vv[c+10], vv[c+11], vv[c+12], vv[c+13] = 950, 30, 1050, uint16(0x10000-30)
	}
	add(705, vv)
	regs[a], regs[a+1] = 0xFFFF, 0
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()
	srv, err := mb.NewServer(&mb.ServerConfiguration{URL: "tcp://" + addr, Timeout: 5 * time.Second, MaxClients: 4}, &device{unit: 1, regs: regs})
	if err != nil {
		t.Fatal(err)
	}
	if err := srv.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = srv.Stop() })
	return "tcp://" + addr
}

func runOut(t *testing.T, args ...string) (string, error) {
	t.Helper()
	var b bytes.Buffer
	err := run(args, &b)
	return b.String(), err
}

func TestCommands(t *testing.T) {
	url := serve(t)
	out, err := runOut(t, "scan", "-url", url, "-units", "1-2")
	if err != nil || !strings.Contains(out, "unit 1: SunSpec map at 40000, models [1 705]; Ma M1, serial S1") || !strings.Contains(out, "unit 2: no SunSpec map") {
		t.Errorf("scan: %v\n%s", err, out)
	}
	out, err = runOut(t, "read", "-url", url, "-model", "705")
	if err != nil || !strings.Contains(out, "2.Pt.2.V") || !strings.Contains(out, "105 %") || strings.Contains(out, "model 1 ") {
		t.Errorf("read: %v\n%s", err, out)
	}
	out, err = runOut(t, "read", "-url", url, "-json")
	var doc struct {
		Base   int `json:"base"`
		Models []struct {
			ID     int `json:"id"`
			Points []struct {
				Path string `json:"path"`
				V    any    `json:"value"`
			} `json:"points"`
		} `json:"models"`
	}
	if err != nil || json.Unmarshal([]byte(out), &doc) != nil || doc.Base != 40000 || len(doc.Models) != 2 {
		t.Fatalf("read -json: %v\n%s", err, out)
	}
	out, err = runOut(t, "write", "-url", url, "-model", "705", "-point", "Crv.2.Pt.1.V", "-value", "97.5")
	if err != nil || !strings.Contains(out, "kept it") || !strings.Contains(out, "is now 97.5") {
		t.Errorf("write: %v\n%s", err, out)
	}
	out, err = runOut(t, "write", "-url", url, "-model", "705", "-point", "Crv.2.DeptRef", "-value", "VAR_AVAL_PCT")
	if err != nil || !strings.Contains(out, "is now VAR_AVAL_PCT") {
		t.Errorf("write enum: %v\n%s", err, out)
	}
	if _, err := runOut(t, "write", "-url", url, "-model", "705", "-point", "Crv.1.ReadOnly", "-value", "0"); err == nil || !strings.Contains(err.Error(), "read-only") {
		t.Errorf("write read-only: %v", err)
	}
	if _, err := runOut(t, "write", "-url", url, "-model", "704", "-point", "W", "-value", "1"); err == nil || !strings.Contains(err.Error(), "no model 704") {
		t.Errorf("absent model: %v", err)
	}
	out, err = runOut(t, "models")
	if err != nil || !strings.Contains(out, "705  derVoltVar") || strings.Count(out, "\n") != 112 {
		t.Errorf("models: %v (%d lines)", err, strings.Count(out, "\n"))
	}
	out, err = runOut(t, "models", "-model", "707")
	if err != nil || !strings.Contains(out, "group Crv (× NCrvSet)") || !strings.Contains(out, "group Pt (× NPt)") {
		t.Errorf("models 707: %v\n%s", err, out)
	}
	defs := t.TempDir() + "/model_64951.json"
	if err := os.WriteFile(defs, []byte(`{"id": 64951, "group": {"name": "v", "points": [{"name": "ID", "type": "uint16"}, {"name": "L", "type": "uint16"}, {"name": "X", "type": "uint16"}]}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := runOut(t, "read", "-url", url, "-defs", defs, "-model", "1"); err != nil {
		t.Errorf("-defs: %v", err)
	}
	for _, args := range [][]string{{"read"}, {"bogus"}, {"read", "-url", url, "-defs", "missing.json"}, {"scan", "-url", url, "-units", "9-3"}, {"models", "-model", "64900"}} {
		if _, err := runOut(t, args...); err == nil {
			t.Errorf("%v succeeded", args)
		}
	}
	if _, err := runOut(t); !errors.Is(err, flag.ErrHelp) {
		t.Errorf("no command: %v", err)
	}
}

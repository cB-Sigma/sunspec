// Copyright 2026 Abhinav Gautam
// SPDX-License-Identifier: Apache-2.0

package mbtcp_test

import (
	"errors"
	"fmt"
	"net"
	"slices"
	"sync"
	"testing"
	"time"

	mb "github.com/simonvetter/modbus"

	"github.com/cB-Sigma/sunspec"
	"github.com/cB-Sigma/sunspec/mbtcp"
)

// device is a minimal Modbus TCP server for these tests: holding
// registers in a map, answering one unit, exception 2 for an address it
// does not hold, writes logged. (The SunSpec device simulator is in
// sunspec-sim.)
type device struct {
	mu   sync.Mutex
	unit uint8
	regs map[uint16]uint16
	log  []string
}

func (d *device) HandleHoldingRegisters(r *mb.HoldingRegistersRequest) ([]uint16, error) {
	if r.UnitId != d.unit {
		return nil, mb.ErrGWTargetFailedToRespond
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if r.IsWrite {
		for i, v := range r.Args {
			a := r.Addr + uint16(i)
			if _, ok := d.regs[a]; !ok {
				return nil, mb.ErrIllegalDataAddress
			}
			d.regs[a] = v
			d.log = append(d.log, fmt.Sprintf("%d=%d", a, v))
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

// serve starts d on a free port and returns its URL.
func serve(t *testing.T, d *device) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()
	srv, err := mb.NewServer(&mb.ServerConfiguration{URL: "tcp://" + addr, Timeout: 5 * time.Second, MaxClients: 4}, d)
	if err != nil {
		t.Fatal(err)
	}
	if err := srv.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = srv.Stop() })
	return "tcp://" + addr
}

// Reader is a sunspec.Reader and a sunspec.Refuser.
var (
	_ sunspec.Reader  = (*mbtcp.Reader)(nil)
	_ sunspec.Refuser = (*mbtcp.Reader)(nil)
)

func TestIsException(t *testing.T) {
	for _, e := range []error{mb.ErrIllegalFunction, mb.ErrIllegalDataAddress, mb.ErrIllegalDataValue,
		mb.ErrServerDeviceFailure, mb.ErrAcknowledge, mb.ErrServerDeviceBusy, mb.ErrMemoryParityError,
		mb.ErrGWPathUnavailable, mb.ErrGWTargetFailedToRespond} {
		if !mbtcp.IsException(e) || !mbtcp.IsException(fmt.Errorf("reading: %w", e)) {
			t.Errorf("%v is an exception", e)
		}
	}
	for _, e := range []error{nil, mb.ErrRequestTimedOut, mb.ErrProtocolError, errors.New("connection reset")} {
		if mbtcp.IsException(e) {
			t.Errorf("%v is not an exception", e)
		}
	}
}

func TestReadWrite(t *testing.T) {
	const a = 40004
	d := &device{unit: 7, regs: map[uint16]uint16{a: 1, a + 1: 2, a + 2: 3}}
	url := serve(t, d)
	r, err := mbtcp.Dial(url, 7, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	if r.Unit() != 7 {
		t.Errorf("unit %d", r.Unit())
	}
	if err := r.WriteHolding(a, []uint16{9}); err != nil {
		t.Fatal(err)
	}
	if err := r.WriteHolding(a+1, []uint16{8, 7}); err != nil {
		t.Fatal(err)
	}
	d.mu.Lock()
	log := slices.Clone(d.log)
	d.mu.Unlock()
	if !slices.Equal(log, []string{"40004=9", "40005=8", "40006=7"}) {
		t.Errorf("log %v", log)
	}
	// Concurrent reads share the connection one request at a time.
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			got, err := r.ReadHolding(a, 3)
			if err != nil || !slices.Equal(got, []uint16{9, 8, 7}) {
				t.Errorf("%v %v", got, err)
			}
		}()
	}
	wg.Wait()
	_, err = r.ReadHolding(a+3, 4)
	if !r.Refused(err) {
		t.Errorf("read past the end: %v", err)
	}
	// Another unit is not answered: the gateway exception, a refusal.
	other, err := mbtcp.Dial(url, 1, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	if _, err := other.ReadHolding(a, 1); !other.Refused(err) {
		t.Errorf("unit 1: %v", err)
	}
	// A Modbus device without a SunSpec map: Find says so.
	if _, err := sunspec.Find(r); err != sunspec.ErrNotSunSpec {
		t.Errorf("no map: %v", err)
	}
}

func TestDialFails(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	gone := ln.Addr().String()
	_ = ln.Close()
	if _, err := mbtcp.Dial("tcp://"+gone, 1, 300*time.Millisecond); err == nil {
		t.Error("dial to a closed port succeeded")
	}
	if _, err := mbtcp.Dial("ftp://x", 1, 0); err == nil {
		t.Error("bad url accepted")
	}
}

// A device that accepts the connection and never answers: a timeout, which
// is silence, not a refusal; Find returns the read error.
func TestSilence(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			defer c.Close()
		}
	}()
	r, err := mbtcp.Dial("tcp://"+ln.Addr().String(), 1, 200*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	_, err = sunspec.Find(r)
	if err == nil || err == sunspec.ErrNotSunSpec || r.Refused(err) {
		t.Errorf("silent device: %v", err)
	}
}

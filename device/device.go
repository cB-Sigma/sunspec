// Copyright 2026 Abhinav Gautam
// SPDX-License-Identifier: Apache-2.0

// Package device builds and serves a SunSpec register map: the device
// side of SunSpec, for gateways, meters, battery systems, EV chargers and
// test benches that publish their data as SunSpec models, and for tests.
//
// What it follows (SunSpec Device Information Model Specification v1.2,
// section 6, and the published model definitions; our own words): the
// holding registers hold the "SunS" marker at a base address (40000, 50000
// or 0), then each model as an ID register, a length register and that
// many data registers, then the end model (ID 0xFFFF, length 0).
//
// Build lays out any model the sunspec tables describe from named raw
// values, nested repeating groups included, and says which registers its
// definition marks writable; Image collects models into a map; AllModels
// holds every described model. Device serves an image: reads outside the
// map are refused with exception 2 (illegal data address) and writes are
// taken only on registers marked writable; hooks run before every request
// (SetBefore: refuse, delay or drop it), decide the value kept for a write
// (SetKeep: a device that clamps a setpoint) and act on a write once it is
// stored (SetOnWrite: a device that applies a setpoint or adopts a curve).
// Listen, ServeRTU and ServeASCII put any Handler (a Device, a Mux of
// units, or the aggregate package's proxies) on Modbus TCP, Modbus TCP
// over TLS, RTU or ASCII framing over TCP, or a serial line the caller
// opened.
package device

import (
	"fmt"
	"io"
	"sync"

	mb "github.com/simonvetter/modbus"
)

// Request is one holding register request, as the hooks see it.
type Request struct {
	// Unit is the unit addressed.
	Unit uint8
	// Write is true for functions 6 and 16.
	Write bool
	// Addr is the first register; Count the number of registers.
	Addr, Count uint16
	// Words are the values written (nil for a read).
	Words []uint16
}

// Device serves an Image. It is safe for concurrent use.
type Device struct {
	mu     sync.Mutex
	im     *Image
	units  map[uint8]bool
	keep   func(addr, v uint16) uint16
	before func(Request) error
	after  func(*Image, Request)
	writes []string
	srv    *Server
	done   chan struct{}
	once   sync.Once
}

// NewDevice makes a device for the image, answering on the units given
// (none: every unit), that serves nothing until Listen, ServeRTU or
// ServeASCII (or use Start, StartOptions). The device takes the image
// over: change it afterwards only through Set or a write hook.
func NewDevice(im *Image, units ...uint8) *Device {
	d := &Device{im: im, units: map[uint8]bool{}, done: make(chan struct{})}
	for _, u := range units {
		d.units[u] = true
	}
	return d
}

// Start serves the image over Modbus TCP on listen ("host:port"; "" or
// port 0 picks a free port on 127.0.0.1 or the given host), answering on
// the units given (none: every unit; others get exception 11, gateway
// target failed to respond). Close stops it.
func Start(im *Image, listen string, units ...uint8) (*Device, error) {
	return StartOptions(im, Options{Listen: listen, Units: units})
}

// StartOptions serves the image as o says.
func StartOptions(im *Image, o Options) (*Device, error) {
	d := NewDevice(im, o.Units...)
	srv, err := listen(d, Options{Listen: o.Listen, TLSCert: o.TLSCert, TLSClientCAs: o.TLSClientCAs, RTU: o.RTU, ASCII: o.ASCII}, d.done)
	if err != nil {
		return nil, err
	}
	d.srv = srv
	return d, nil
}

// Addr is the address the device listens on ("127.0.0.1:50211"); empty
// when it does not listen.
func (d *Device) Addr() string {
	if d.srv == nil {
		return ""
	}
	return d.srv.Addr()
}

// URL is the device's URL ("tcp://127.0.0.1:50211", "tcp+tls://…",
// "rtuovertcp://…", "asciiovertcp://…"), as package mbtcp takes it.
func (d *Device) URL() string {
	if d.srv == nil {
		return ""
	}
	return d.srv.URL()
}

// Done is closed when the device closes; a SetBefore hook that holds a
// request back waits on it.
func (d *Device) Done() <-chan struct{} { return d.done }

// Close stops the server, closes its connections and ends ServeRTU and
// ServeASCII after their current frame.
func (d *Device) Close() error {
	var err error
	d.once.Do(func() {
		if d.srv != nil {
			err = d.srv.Close() // closes done
			return
		}
		close(d.done)
	})
	return err
}

// SetKeep sets a hook that gives the value the device keeps for a write
// to addr (nil: the value written).
func (d *Device) SetKeep(keep func(addr, v uint16) uint16) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.keep = keep
}

// SetBefore sets a hook run before each request the device's units get
// (nil: none). A non-nil error is the answer instead: an Exception (or a
// simonvetter/modbus exception error), ErrNoAnswer for silence on a serial
// line. The hook runs outside the device's lock and may block (wait on
// Done to give up when the device closes).
func (d *Device) SetBefore(f func(Request) error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.before = f
}

// SetOnWrite sets a hook run after each write is stored (nil: none), with
// the image and the write, under the device's lock: it may read and set
// any register of the image (a measurement that follows a setpoint), and
// no read sees the write without the hook's changes. It must not call the
// Device's own methods.
func (d *Device) SetOnWrite(f func(im *Image, w Request)) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.after = f
}

// Update runs f on the image under the device's lock (to change several
// registers at once, as a measurement update).
func (d *Device) Update(f func(im *Image)) {
	d.mu.Lock()
	defer d.mu.Unlock()
	f(d.im)
}

// Get reads a register.
func (d *Device) Get(addr uint16) uint16 {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.im.regs[addr]
}

// Set writes registers (writable or not), as the device itself would.
func (d *Device) Set(addr uint16, words ...uint16) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.im.Set(addr, words...)
}

// WriteLog returns the writes so far, as "address=value" with the value
// written (before the SetKeep hook).
func (d *Device) WriteLog() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]string(nil), d.writes...)
}

func (d *Device) serves(unit uint8) bool { return len(d.units) == 0 || d.units[unit] }

func (d *Device) hook(r Request) error {
	d.mu.Lock()
	f := d.before
	d.mu.Unlock()
	if f == nil {
		return nil
	}
	return f(r)
}

// ReadHolding answers a read (Handler).
func (d *Device) ReadHolding(unit uint8, addr, count uint16) ([]uint16, error) {
	if !d.serves(unit) {
		return nil, ErrUnknownUnit
	}
	if err := d.hook(Request{Unit: unit, Addr: addr, Count: count}); err != nil {
		return nil, err
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.im.ReadHolding(addr, count)
}

// WriteHolding answers a write (Handler): refused with exception 2 when
// any register is not writable, else stored whole.
func (d *Device) WriteHolding(unit uint8, addr uint16, words []uint16) error {
	if !d.serves(unit) {
		return ErrUnknownUnit
	}
	req := Request{Unit: unit, Write: true, Addr: addr, Count: uint16(len(words)), Words: append([]uint16(nil), words...)}
	if err := d.hook(req); err != nil {
		return err
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	for i := range words {
		if !d.im.writable[addr+uint16(i)] {
			return IllegalAddress
		}
	}
	for i, v := range words {
		a := addr + uint16(i)
		if d.keep != nil {
			v = d.keep(a, v)
		}
		d.im.regs[a] = v
		d.writes = append(d.writes, fmt.Sprintf("%d=%d", a, words[i]))
	}
	if d.after != nil {
		d.after(d.im, req)
	}
	return nil
}

// ServeRTU answers Modbus RTU frames read from rw until rw fails or the
// device closes (see the package function ServeRTU); units the device
// does not answer get no reply.
func (d *Device) ServeRTU(rw io.ReadWriter) error { return serveRTU(d, rw, d.done) }

// ServeASCII answers Modbus ASCII frames read from rw until rw fails or
// the device closes.
func (d *Device) ServeASCII(rw io.ReadWriter) error { return serveASCII(d, rw, d.done) }

// HandleHoldingRegisters serves function codes 3, 6 and 16 for
// github.com/simonvetter/modbus's server.
func (d *Device) HandleHoldingRegisters(r *mb.HoldingRegistersRequest) ([]uint16, error) {
	return mbHandler{d}.HandleHoldingRegisters(r)
}

// HandleInputRegisters answers illegal function: a SunSpec map is in
// holding registers only.
func (d *Device) HandleInputRegisters(r *mb.InputRegistersRequest) ([]uint16, error) {
	return mbHandler{d}.HandleInputRegisters(r)
}

// HandleCoils answers illegal function.
func (d *Device) HandleCoils(r *mb.CoilsRequest) ([]bool, error) { return mbHandler{d}.HandleCoils(r) }

// HandleDiscreteInputs answers illegal function.
func (d *Device) HandleDiscreteInputs(r *mb.DiscreteInputsRequest) ([]bool, error) {
	return mbHandler{d}.HandleDiscreteInputs(r)
}

// Copyright 2026 Abhinav Gautam
// SPDX-License-Identifier: Apache-2.0

// Package aggregate puts one SunSpec front in front of several devices,
// as a site gateway, a plant controller or a test bench does. Both kinds
// are device.Handler values, so device.Listen serves them on Modbus TCP,
// TLS, RTU or ASCII, and a device.Mux combines them on one listener.
//
//   - Proxy passes each unit through to its own device: reads and writes
//     go to the device behind that unit; with MaxAge set, reads of the
//     device's SunSpec map are answered from a snapshot no older than
//     that, so many clients polling the gateway cost one poll of the
//     device.
//   - Merged presents one map made of a common model of its own, models
//     merged across the devices (one 103 for the whole plant: power and
//     energy summed, voltage and frequency averaged, each with a scale
//     factor that fits the result), and models copied from one device
//     each. Writes to a merged model are sent to every device that has it
//     (a percentage limit fanned out, a power setpoint split); writes to
//     a copied model go to its device.
//
// Upstream devices are reached through anything with ReadHolding and
// WriteHolding (an *mbtcp.Reader on any transport, or Local for a handler
// in the same process).
//
// What it does not do: it keeps no SunSpec semantics beyond the model
// layouts (a merged status or enum is the first device's), it does not
// roll back a fanned-out write that only some devices took, and a device
// that cannot be read when Merged starts is an error (the map's layout
// comes from the devices).
package aggregate

import (
	"errors"
	"sync"
	"time"

	"github.com/cB-Sigma/sunspec"
	"github.com/cB-Sigma/sunspec/device"
)

// Upstream is a device behind the aggregator: one unit on a Modbus
// connection (an *mbtcp.Reader) or anything that reads and writes its
// holding registers.
type Upstream interface {
	sunspec.Reader
	WriteHolding(addr uint16, words []uint16) error
}

// Local makes a handler in this process (a device.Device, another
// aggregator) an Upstream, addressed as unit.
func Local(h device.Handler, unit uint8) Upstream { return local{h, unit} }

type local struct {
	h    device.Handler
	unit uint8
}

func (l local) ReadHolding(addr, count uint16) ([]uint16, error) {
	return l.h.ReadHolding(l.unit, addr, count)
}

func (l local) WriteHolding(addr uint16, words []uint16) error {
	return l.h.WriteHolding(l.unit, addr, words)
}

func (l local) Refused(err error) bool {
	_, ok := device.ExceptionCode(err)
	return ok
}

// upstreamError turns an upstream error into the answer for the client:
// the device's own exception, or exception 11 (gateway target device
// failed to respond) for silence or a broken connection.
func upstreamError(err error) error {
	if err == nil {
		return nil
	}
	if c, ok := device.ExceptionCode(err); ok {
		return device.Exception(c)
	}
	return device.GatewayNoResponse
}

// ProxyOptions shapes a Proxy.
type ProxyOptions struct {
	// MaxAge, when above zero, answers reads that fall inside a unit's
	// SunSpec map (marker, model headers, model data, end model) from a
	// snapshot of the whole map no older than MaxAge; reads elsewhere,
	// and every read when MaxAge is zero, go to the device.
	MaxAge time.Duration
}

// Proxy passes units through to their devices. It is a device.Handler
// and safe for concurrent use; requests to one unit are sent one at a
// time.
type Proxy struct {
	o     ProxyOptions
	units map[uint8]*unitState
}

type unitState struct {
	mu   sync.Mutex
	up   Upstream
	regs map[uint16]uint16
	at   time.Time
}

// NewProxy passes each unit of units to its device.
func NewProxy(units map[uint8]Upstream, o ProxyOptions) *Proxy {
	p := &Proxy{o: o, units: map[uint8]*unitState{}}
	for u, up := range units {
		p.units[u] = &unitState{up: up}
	}
	return p
}

// Invalidate drops every snapshot, so the next read of each unit goes to
// its device.
func (p *Proxy) Invalidate() {
	for _, u := range p.units {
		u.mu.Lock()
		u.regs = nil
		u.mu.Unlock()
	}
}

// ReadHolding answers a read for a unit.
func (p *Proxy) ReadHolding(unit uint8, addr, count uint16) ([]uint16, error) {
	u, ok := p.units[unit]
	if !ok {
		return nil, device.ErrUnknownUnit
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	if p.o.MaxAge > 0 {
		if u.regs == nil || time.Since(u.at) > p.o.MaxAge {
			regs, err := snapshot(u.up)
			switch {
			case errors.Is(err, sunspec.ErrNotSunSpec):
				regs = map[uint16]uint16{} // no map: every read goes to the device
			case err != nil:
				u.regs = nil
				return nil, upstreamError(err)
			}
			u.regs, u.at = regs, time.Now()
		}
		if out, ok := within(u.regs, addr, count); ok {
			return out, nil
		}
	}
	out, err := u.up.ReadHolding(addr, count)
	return out, upstreamError(err)
}

// WriteHolding passes a write to the unit's device, and updates the
// snapshot when the device took it.
func (p *Proxy) WriteHolding(unit uint8, addr uint16, words []uint16) error {
	u, ok := p.units[unit]
	if !ok {
		return device.ErrUnknownUnit
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	if err := u.up.WriteHolding(addr, words); err != nil {
		return upstreamError(err)
	}
	if u.regs != nil {
		// The device may keep another value than the one written: read
		// the registers back rather than trust the write.
		if back, err := u.up.ReadHolding(addr, uint16(len(words))); err == nil {
			for i, w := range back {
				if _, in := u.regs[addr+uint16(i)]; in {
					u.regs[addr+uint16(i)] = w
				}
			}
		} else {
			u.regs = nil
		}
	}
	return nil
}

// snapshot reads a device's whole SunSpec map, register by register: the
// marker, each model's header and data, and the end model.
func snapshot(r sunspec.Reader) (map[uint16]uint16, error) {
	m, err := sunspec.Find(r)
	if err != nil {
		return nil, err
	}
	regs := map[uint16]uint16{m.Base: sunspec.Marker[0], m.Base + 1: sunspec.Marker[1]}
	end := m.Base + 2
	for _, inst := range m.Models {
		data, err := sunspec.ReadModel(r, inst)
		if err != nil {
			return nil, err
		}
		regs[inst.Addr-2], regs[inst.Addr-1] = uint16(inst.ID), uint16(inst.L)
		for i, w := range data {
			regs[inst.Addr+uint16(i)] = w
		}
		end = inst.Addr + uint16(inst.L)
	}
	if hdr, err := r.ReadHolding(end, 2); err == nil && hdr[0] == sunspec.EndID {
		regs[end], regs[end+1] = hdr[0], hdr[1]
	}
	return regs, nil
}

func within(regs map[uint16]uint16, addr, count uint16) ([]uint16, bool) {
	if regs == nil {
		return nil, false
	}
	out := make([]uint16, count)
	for i := range out {
		v, ok := regs[addr+uint16(i)]
		if !ok {
			return nil, false
		}
		out[i] = v
	}
	return out, true
}

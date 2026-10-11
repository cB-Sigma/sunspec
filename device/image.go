// Copyright 2026 Abhinav Gautam
// SPDX-License-Identifier: Apache-2.0

package device

import "github.com/cB-Sigma/sunspec"

// Image is a register map under construction or being served: the
// "SunS" marker at Base, then the models as ID, length and data, then the
// end model. It is not safe for concurrent use; once a Device serves it,
// change it only through the Device (Set, or inside a write hook).
type Image struct {
	// Base is the address of the marker.
	Base     uint16
	regs     map[uint16]uint16
	next     uint16
	writable map[uint16]bool
}

// NewImage starts a map with the marker at base.
func NewImage(base uint16) *Image {
	im := &Image{Base: base, regs: map[uint16]uint16{}, writable: map[uint16]bool{}}
	im.regs[base], im.regs[base+1] = sunspec.Marker[0], sunspec.Marker[1]
	im.next = base + 2
	return im
}

// Model appends a model with id and its data registers, and returns the
// address of its first data register.
func (im *Image) Model(id uint16, data []uint16) uint16 {
	im.regs[im.next] = id
	im.regs[im.next+1] = uint16(len(data))
	start := im.next + 2
	for i, w := range data {
		im.regs[start+uint16(i)] = w
	}
	im.next = start + uint16(len(data))
	return start
}

// End appends the end model.
func (im *Image) End() {
	im.regs[im.next], im.regs[im.next+1] = 0xFFFF, 0
	im.next += 2
}

// Next is the address the next model's ID register would take (after
// End: the register after the end model).
func (im *Image) Next() uint16 { return im.next }

// Writable marks addresses as writable.
func (im *Image) Writable(addrs ...uint16) {
	for _, a := range addrs {
		im.writable[a] = true
	}
}

// IsWritable reports whether addr takes writes.
func (im *Image) IsWritable(addr uint16) bool { return im.writable[addr] }

// Has reports whether addr is part of the map.
func (im *Image) Has(addr uint16) bool {
	_, ok := im.regs[addr]
	return ok
}

// Get reads a register (0 outside the map).
func (im *Image) Get(addr uint16) uint16 { return im.regs[addr] }

// Set writes registers, writable or not, as the device itself would. It
// can also place registers outside the model chain.
func (im *Image) Set(addr uint16, words ...uint16) {
	for i, w := range words {
		im.regs[addr+uint16(i)] = w
	}
}

// ReadHolding reads count registers from addr, so that package sunspec
// can walk an image (sunspec.Find(im)); a register outside the map is
// refused with exception 2.
func (im *Image) ReadHolding(addr, count uint16) ([]uint16, error) {
	out := make([]uint16, count)
	for i := range out {
		v, ok := im.regs[addr+uint16(i)]
		if !ok {
			return nil, IllegalAddress
		}
		out[i] = v
	}
	return out, nil
}

// Refused reports whether err is a Modbus exception (sunspec.Refuser).
func (im *Image) Refused(err error) bool {
	_, ok := ExceptionCode(err)
	return ok
}

// Str encodes text into n registers, zero-padded.
func Str(s string, n int) []uint16 {
	out := make([]uint16, n)
	b := []byte(s)
	for i := range out {
		var hi, lo byte
		if 2*i < len(b) {
			hi = b[2*i]
		}
		if 2*i+1 < len(b) {
			lo = b[2*i+1]
		}
		out[i] = uint16(hi)<<8 | uint16(lo)
	}
	return out
}

// Filled returns n registers of v (0xFFFF or 0x8000 fill a model whose
// points are not implemented).
func Filled(n int, v uint16) []uint16 {
	out := make([]uint16, n)
	for i := range out {
		out[i] = v
	}
	return out
}

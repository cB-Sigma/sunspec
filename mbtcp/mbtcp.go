// Copyright 2026 Abhinav Gautam
// SPDX-License-Identifier: Apache-2.0

// Package mbtcp is a small Modbus TCP Reader for package sunspec, over
// github.com/simonvetter/modbus: holding register reads (function 3) and
// writes (functions 6 and 16) for one unit, and the Refuser that tells a
// Modbus exception from silence. An application with its own Modbus
// connection handling (shared connections, RTU, gateways) implements
// sunspec.Reader itself instead.
package mbtcp

import (
	"errors"
	"sync"
	"time"

	mb "github.com/simonvetter/modbus"
)

// Reader reads and writes one unit's holding registers over its own
// Modbus TCP connection. It is safe for concurrent use; requests are
// sent one at a time.
type Reader struct {
	mu   sync.Mutex
	c    *mb.ModbusClient
	unit uint8
}

// Dial connects to url ("tcp://host:502"; "rtuovertcp://" works too) and
// addresses unit (1 for most inverters, 126 for SMA's SunSpec unit).
// timeout is the response timeout (0: 2 seconds).
func Dial(url string, unit uint8, timeout time.Duration) (*Reader, error) {
	if timeout <= 0 {
		timeout = 2 * time.Second
	}
	c, err := mb.NewClient(&mb.ClientConfiguration{URL: url, Timeout: timeout})
	if err != nil {
		return nil, err
	}
	if err := c.Open(); err != nil {
		return nil, err
	}
	if err := c.SetUnitId(unit); err != nil {
		_ = c.Close()
		return nil, err
	}
	_ = c.SetEncoding(mb.BIG_ENDIAN, mb.HIGH_WORD_FIRST)
	return &Reader{c: c, unit: unit}, nil
}

// Unit is the unit the Reader addresses.
func (r *Reader) Unit() uint8 { return r.unit }

// ReadHolding reads count holding registers from addr (function 3).
func (r *Reader) ReadHolding(addr, count uint16) ([]uint16, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.c.ReadRegisters(addr, count, mb.HOLDING_REGISTER)
}

// WriteHolding writes words from addr: function 6 for one register,
// 16 for more.
func (r *Reader) WriteHolding(addr uint16, words []uint16) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(words) == 1 {
		return r.c.WriteRegister(addr, words[0])
	}
	return r.c.WriteRegisters(addr, words)
}

// Refused reports whether err is a Modbus exception reply (the device
// answered); it makes Reader a sunspec.Refuser.
func (r *Reader) Refused(err error) bool { return IsException(err) }

// Close closes the connection.
func (r *Reader) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.c.Close()
}

// IsException reports whether err is a Modbus exception reply (codes 1
// to 6, 8, 10 and 11), as opposed to a timeout or a transport error.
func IsException(err error) bool {
	for _, e := range []error{mb.ErrIllegalFunction, mb.ErrIllegalDataAddress, mb.ErrIllegalDataValue,
		mb.ErrServerDeviceFailure, mb.ErrAcknowledge, mb.ErrServerDeviceBusy, mb.ErrMemoryParityError,
		mb.ErrGWPathUnavailable, mb.ErrGWTargetFailedToRespond} {
		if errors.Is(err, e) {
			return true
		}
	}
	return false
}

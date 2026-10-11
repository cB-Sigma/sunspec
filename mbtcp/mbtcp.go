// Copyright 2026 Abhinav Gautam
// SPDX-License-Identifier: Apache-2.0

// Package mbtcp is a small Modbus Reader for package sunspec, over
// github.com/simonvetter/modbus: holding register reads (function 3) and
// writes (functions 6 and 16), and the Refuser that tells a Modbus
// exception from silence. Despite its name it speaks every transport that
// library has: Modbus TCP, Modbus TCP over TLS (Modbus/TCP Security, with
// a client certificate), Modbus over UDP, Modbus RTU on a serial line and
// RTU framing over TCP or UDP (serial-to-Ethernet gateways). Readers for
// several units share one connection (ForUnit). An application with its
// own Modbus connection handling implements sunspec.Reader itself instead.
package mbtcp

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"log"
	"strings"
	"sync"
	"time"

	mb "github.com/simonvetter/modbus"
)

// Config says how to reach a device.
type Config struct {
	// URL picks the transport and the place: "tcp://host:502",
	// "tcp+tls://host:802", "udp://host:502", "rtu:///dev/ttyUSB0" (a
	// serial line; "rtu://COM3" on Windows), "rtuovertcp://host:4001",
	// "rtuoverudp://host:4001".
	URL string
	// Unit is the Modbus unit (slave) address: 1 for most inverters,
	// 126 for SMA's SunSpec unit.
	Unit uint8
	// Timeout is the response timeout (0: 2 seconds; 300 ms on a serial
	// line).
	Timeout time.Duration
	// Speed, DataBits, Parity ("N", "E" or "O"; "" is N) and StopBits
	// set a serial line (rtu); 0 takes 19200 bit/s, 8 data bits, and 2
	// stop bits without parity or 1 with it.
	Speed, DataBits uint
	Parity          string
	StopBits        uint
	// TLSClientCert and TLSRootCAs are the client's certificate and key
	// and the CAs (or the server's own certificate) that authenticate
	// the server, for tcp+tls; both are required there.
	TLSClientCert *tls.Certificate
	TLSRootCAs    *x509.CertPool
}

// conn is one transport, shared by the Readers of its units.
type conn struct {
	mu     sync.Mutex
	c      *mb.ModbusClient
	closed bool
}

// Reader reads and writes one unit's holding registers. It is safe for
// concurrent use; requests on a connection are sent one at a time.
type Reader struct {
	conn *conn
	unit uint8
}

// Dial connects to url ("tcp://host:502"; any URL Open takes) and
// addresses unit. timeout is the response timeout (0: 2 seconds).
func Dial(url string, unit uint8, timeout time.Duration) (*Reader, error) {
	return Open(Config{URL: url, Unit: unit, Timeout: timeout})
}

// Open connects as c says.
func Open(c Config) (*Reader, error) {
	if c.Timeout <= 0 {
		c.Timeout = 2 * time.Second
		if strings.HasPrefix(c.URL, "rtu://") {
			c.Timeout = 300 * time.Millisecond
		}
	}
	parity := mb.PARITY_NONE
	switch strings.ToUpper(c.Parity) {
	case "", "N":
	case "E":
		parity = mb.PARITY_EVEN
	case "O":
		parity = mb.PARITY_ODD
	default:
		return nil, fmt.Errorf("parity %q is not N, E or O", c.Parity)
	}
	cl, err := mb.NewClient(&mb.ClientConfiguration{URL: c.URL, Timeout: c.Timeout, Speed: c.Speed, DataBits: c.DataBits,
		Parity: parity, StopBits: c.StopBits, TLSClientCert: c.TLSClientCert, TLSRootCAs: c.TLSRootCAs,
		Logger: log.New(io.Discard, "", 0)})
	if err != nil {
		if strings.HasPrefix(c.URL, "tcp+tls://") && (c.TLSClientCert == nil || c.TLSRootCAs == nil) {
			return nil, fmt.Errorf("%w: tcp+tls needs TLSClientCert and TLSRootCAs", err)
		}
		return nil, err
	}
	if err := cl.Open(); err != nil {
		return nil, err
	}
	_ = cl.SetEncoding(mb.BIG_ENDIAN, mb.HIGH_WORD_FIRST)
	return &Reader{conn: &conn{c: cl}, unit: c.Unit}, nil
}

// ForUnit returns a Reader for another unit on the same connection (a
// gateway, or a device answering on several units). Closing any of them
// closes the connection.
func (r *Reader) ForUnit(unit uint8) *Reader { return &Reader{conn: r.conn, unit: unit} }

// Unit is the unit the Reader addresses.
func (r *Reader) Unit() uint8 { return r.unit }

var errClosed = errors.New("mbtcp: the connection is closed")

// do runs one request for the Reader's unit, alone on the connection.
func (r *Reader) do(f func(c *mb.ModbusClient) error) error {
	r.conn.mu.Lock()
	defer r.conn.mu.Unlock()
	if r.conn.closed {
		return errClosed
	}
	if err := r.conn.c.SetUnitId(r.unit); err != nil {
		return err
	}
	return f(r.conn.c)
}

// ReadHolding reads count holding registers from addr (function 3).
func (r *Reader) ReadHolding(addr, count uint16) ([]uint16, error) {
	var out []uint16
	err := r.do(func(c *mb.ModbusClient) (err error) {
		out, err = c.ReadRegisters(addr, count, mb.HOLDING_REGISTER)
		return err
	})
	return out, err
}

// WriteHolding writes words from addr: function 6 for one register,
// 16 for more.
func (r *Reader) WriteHolding(addr uint16, words []uint16) error {
	return r.do(func(c *mb.ModbusClient) error {
		if len(words) == 1 {
			return c.WriteRegister(addr, words[0])
		}
		return c.WriteRegisters(addr, words)
	})
}

// Refused reports whether err is a Modbus exception reply (the device
// answered); it makes Reader a sunspec.Refuser.
func (r *Reader) Refused(err error) bool { return IsException(err) }

// Close closes the connection (for every unit sharing it).
func (r *Reader) Close() error {
	r.conn.mu.Lock()
	defer r.conn.mu.Unlock()
	if r.conn.closed {
		return nil
	}
	r.conn.closed = true
	return r.conn.c.Close()
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

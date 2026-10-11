// Copyright 2026 Abhinav Gautam
// SPDX-License-Identifier: Apache-2.0

// Package mbtcp is a small Modbus Reader for package sunspec, over
// github.com/simonvetter/modbus: holding register reads (function 3) and
// writes (functions 6 and 16), and the Refuser that tells a Modbus
// exception from silence. Despite its name it speaks every transport that
// library has: Modbus TCP, Modbus TCP over TLS (Modbus/TCP Security, with
// a client certificate), Modbus over UDP, Modbus RTU on a serial line and
// RTU framing over TCP or UDP (serial-to-Ethernet gateways); and, with a
// client of our own, Modbus ASCII on a serial line or over TCP. Readers
// for several units share one connection (ForUnit). An application with
// its own Modbus connection handling implements sunspec.Reader itself
// instead.
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
	// "rtuoverudp://host:4001", "ascii:///dev/ttyUSB0" (Modbus ASCII on a
	// serial line), "asciiovertcp://host:4001".
	URL string
	// Unit is the Modbus unit (slave) address: 1 for most inverters,
	// 126 for SMA's SunSpec unit.
	Unit uint8
	// Timeout is the response timeout (0: 2 seconds; 300 ms on an RTU
	// serial line, 1 second on an ASCII one).
	Timeout time.Duration
	// Speed, DataBits, Parity ("N", "E" or "O"; "" is N) and StopBits
	// set a serial line (rtu); 0 takes 19200 bit/s, 8 data bits, and 2
	// stop bits without parity or 1 with it. On an ascii line the
	// defaults are 19200 bit/s, 7 data bits, even parity ("" is E) and
	// 1 stop bit (2 with parity N).
	Speed, DataBits uint
	Parity          string
	StopBits        uint
	// TLSClientCert and TLSRootCAs are the client's certificate and key
	// and the CAs (or the server's own certificate) that authenticate
	// the server, for tcp+tls; both are required there.
	TLSClientCert *tls.Certificate
	TLSRootCAs    *x509.CertPool
}

// transport is one Modbus client: github.com/simonvetter/modbus's, or
// our own for Modbus ASCII.
type transport interface {
	readHolding(unit uint8, addr, count uint16) ([]uint16, error)
	writeHolding(unit uint8, addr uint16, words []uint16) error
	close() error
}

type mbClient struct{ c *mb.ModbusClient }

func (m mbClient) readHolding(unit uint8, addr, count uint16) ([]uint16, error) {
	if err := m.c.SetUnitId(unit); err != nil {
		return nil, err
	}
	return m.c.ReadRegisters(addr, count, mb.HOLDING_REGISTER)
}

func (m mbClient) writeHolding(unit uint8, addr uint16, words []uint16) error {
	if err := m.c.SetUnitId(unit); err != nil {
		return err
	}
	if len(words) == 1 {
		return m.c.WriteRegister(addr, words[0])
	}
	return m.c.WriteRegisters(addr, words)
}

func (m mbClient) close() error { return m.c.Close() }

// conn is one transport, shared by the Readers of its units.
type conn struct {
	mu     sync.Mutex
	t      transport
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
		switch {
		case strings.HasPrefix(c.URL, "rtu://"):
			c.Timeout = 300 * time.Millisecond
		case strings.HasPrefix(c.URL, "ascii://"):
			c.Timeout = time.Second // a slow framing
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
	if strings.HasPrefix(c.URL, "ascii://") || strings.HasPrefix(c.URL, "asciiovertcp://") {
		a, err := openASCII(c)
		if err != nil {
			return nil, err
		}
		return &Reader{conn: &conn{t: a}, unit: c.Unit}, nil
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
	return &Reader{conn: &conn{t: mbClient{cl}}, unit: c.Unit}, nil
}

// ForUnit returns a Reader for another unit on the same connection (a
// gateway, or a device answering on several units). Closing any of them
// closes the connection.
func (r *Reader) ForUnit(unit uint8) *Reader { return &Reader{conn: r.conn, unit: unit} }

// Unit is the unit the Reader addresses.
func (r *Reader) Unit() uint8 { return r.unit }

var errClosed = errors.New("mbtcp: the connection is closed")

// do runs one request for the Reader's unit, alone on the connection.
func (r *Reader) do(f func(t transport) error) error {
	r.conn.mu.Lock()
	defer r.conn.mu.Unlock()
	if r.conn.closed {
		return errClosed
	}
	return f(r.conn.t)
}

// ReadHolding reads count holding registers from addr (function 3).
func (r *Reader) ReadHolding(addr, count uint16) ([]uint16, error) {
	var out []uint16
	err := r.do(func(t transport) (err error) {
		out, err = t.readHolding(r.unit, addr, count)
		return err
	})
	return out, err
}

// WriteHolding writes words from addr: function 6 for one register,
// 16 for more.
func (r *Reader) WriteHolding(addr uint16, words []uint16) error {
	return r.do(func(t transport) error { return t.writeHolding(r.unit, addr, words) })
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
	return r.conn.t.close()
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

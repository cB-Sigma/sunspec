// Copyright 2026 Abhinav Gautam
// SPDX-License-Identifier: Apache-2.0

package mbtcp

import (
	"bufio"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"time"

	"github.com/goburrow/serial"
	mb "github.com/simonvetter/modbus"

	"github.com/cB-Sigma/sunspec/internal/ascii"
)

// Modbus ASCII is not in github.com/simonvetter/modbus; this is our own
// client for it: on a serial line ("ascii:///dev/ttyUSB0", through
// github.com/goburrow/serial, which that library also uses) or over TCP
// ("asciiovertcp://host:port", a serial gateway). One request at a time,
// as on any serial line.
type asciiClient struct {
	rw       io.ReadWriteCloser
	br       *bufio.Reader
	timeout  time.Duration
	deadline func(time.Time) error // nil on a serial line (the port times out reads)
}

func openASCII(c Config) (*asciiClient, error) {
	switch {
	case strings.HasPrefix(c.URL, "asciiovertcp://"):
		nc, err := net.DialTimeout("tcp", strings.TrimPrefix(c.URL, "asciiovertcp://"), c.Timeout)
		if err != nil {
			return nil, err
		}
		return &asciiClient{rw: nc, br: bufio.NewReader(nc), timeout: c.Timeout, deadline: nc.SetDeadline}, nil
	case strings.HasPrefix(c.URL, "ascii://"):
		parity := strings.ToUpper(c.Parity)
		if parity == "" {
			parity = "E" // the serial line guide's default for ASCII: 7 data bits, even parity
		}
		conf := &serial.Config{Address: strings.TrimPrefix(c.URL, "ascii://"), BaudRate: int(c.Speed), DataBits: int(c.DataBits),
			StopBits: int(c.StopBits), Parity: parity, Timeout: c.Timeout}
		if conf.BaudRate == 0 {
			conf.BaudRate = 19200
		}
		if conf.DataBits == 0 {
			conf.DataBits = 7
		}
		if conf.StopBits == 0 {
			conf.StopBits = 1
			if parity == "N" {
				conf.StopBits = 2
			}
		}
		p, err := serial.Open(conf)
		if err != nil {
			return nil, err
		}
		return &asciiClient{rw: p, br: bufio.NewReader(p), timeout: c.Timeout}, nil
	}
	return nil, fmt.Errorf("mbtcp: %q is not an ascii:// or asciiovertcp:// URL", c.URL)
}

// exchange sends one request PDU to unit and returns the reply's PDU, or
// the exception as github.com/simonvetter/modbus's error for it (so that
// IsException and Refused work the same on every transport).
func (a *asciiClient) exchange(unit uint8, pdu []byte) ([]byte, error) {
	if a.deadline != nil {
		_ = a.deadline(time.Now().Add(a.timeout))
	}
	if _, err := a.rw.Write(ascii.Encode(append([]byte{unit}, pdu...))); err != nil {
		return nil, err
	}
	if unit == 0 {
		return nil, nil // a broadcast has no reply
	}
	until := time.Now().Add(a.timeout)
	for {
		frame, err := ascii.ReadFrame(a.br)
		if err != nil {
			a.br.Reset(a.rw) // drop a partial frame; a late reply is told apart by unit and function
			if errors.Is(err, serial.ErrTimeout) || isTimeout(err) {
				return nil, mb.ErrRequestTimedOut
			}
			return nil, err
		}
		adu, err := ascii.Decode(frame)
		if err != nil {
			return nil, mb.ErrBadCRC
		}
		if adu[0] != unit || len(adu) < 2 || adu[1]&0x7F != pdu[0] {
			if time.Now().After(until) {
				return nil, mb.ErrRequestTimedOut
			}
			continue // a late reply to an earlier request
		}
		if adu[1]&0x80 != 0 {
			if len(adu) < 3 {
				return nil, mb.ErrProtocolError
			}
			if e, ok := mbErrors[adu[2]]; ok {
				return nil, e
			}
			return nil, fmt.Errorf("%w: exception %d", mb.ErrProtocolError, adu[2])
		}
		return adu[1:], nil
	}
}

func isTimeout(err error) bool {
	var ne net.Error
	return errors.As(err, &ne) && ne.Timeout()
}

var mbErrors = map[uint8]error{1: mb.ErrIllegalFunction, 2: mb.ErrIllegalDataAddress, 3: mb.ErrIllegalDataValue,
	4: mb.ErrServerDeviceFailure, 5: mb.ErrAcknowledge, 6: mb.ErrServerDeviceBusy, 8: mb.ErrMemoryParityError,
	10: mb.ErrGWPathUnavailable, 11: mb.ErrGWTargetFailedToRespond}

func (a *asciiClient) readHolding(unit uint8, addr, count uint16) ([]uint16, error) {
	if count < 1 || count > 125 {
		return nil, mb.ErrUnexpectedParameters
	}
	pdu := binary.BigEndian.AppendUint16([]byte{3}, addr)
	pdu = binary.BigEndian.AppendUint16(pdu, count)
	resp, err := a.exchange(unit, pdu)
	if err != nil {
		return nil, err
	}
	if len(resp) < 2 || int(resp[1]) != 2*int(count) || len(resp) != 2+2*int(count) {
		return nil, mb.ErrProtocolError
	}
	out := make([]uint16, count)
	for i := range out {
		out[i] = binary.BigEndian.Uint16(resp[2+2*i:])
	}
	return out, nil
}

func (a *asciiClient) writeHolding(unit uint8, addr uint16, words []uint16) error {
	var pdu []byte
	switch {
	case len(words) == 1:
		pdu = binary.BigEndian.AppendUint16([]byte{6}, addr)
		pdu = binary.BigEndian.AppendUint16(pdu, words[0])
	case len(words) >= 2 && len(words) <= 123:
		pdu = binary.BigEndian.AppendUint16([]byte{16}, addr)
		pdu = binary.BigEndian.AppendUint16(pdu, uint16(len(words)))
		pdu = append(pdu, byte(2*len(words)))
		for _, w := range words {
			pdu = binary.BigEndian.AppendUint16(pdu, w)
		}
	default:
		return mb.ErrUnexpectedParameters
	}
	resp, err := a.exchange(unit, pdu)
	if err != nil || unit == 0 {
		return err
	}
	if len(resp) != 5 || binary.BigEndian.Uint16(resp[1:]) != addr {
		return mb.ErrProtocolError
	}
	return nil
}

func (a *asciiClient) close() error { return a.rw.Close() }

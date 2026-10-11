// Copyright 2026 Abhinav Gautam
// SPDX-License-Identifier: Apache-2.0

package device

import (
	"bufio"
	"crypto/tls"
	"crypto/x509"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"sync"
	"time"

	mb "github.com/simonvetter/modbus"

	"github.com/cB-Sigma/sunspec/internal/ascii"
)

// Handler answers holding register requests for a unit. Device (an
// image), aggregate.Proxy and aggregate.Merged implement it; Listen,
// ServeRTU and ServeASCII put one on a transport. An error that is an
// Exception (or a Modbus exception error of github.com/simonvetter/modbus)
// is answered with that exception code; ErrUnknownUnit with exception 11
// on Modbus TCP and silence on a serial line; ErrNoAnswer with silence on
// a serial line (Modbus TCP: exception 4, as the server library must
// reply); any other error with exception 4 (server device failure).
type Handler interface {
	ReadHolding(unit uint8, addr, count uint16) ([]uint16, error)
	WriteHolding(unit uint8, addr uint16, words []uint16) error
}

// Exception is a Modbus exception code, as an error.
type Exception uint8

// The Modbus exception codes.
const (
	IllegalFunction   Exception = 1
	IllegalAddress    Exception = 2
	IllegalValue      Exception = 3
	DeviceFailure     Exception = 4
	Acknowledge       Exception = 5
	Busy              Exception = 6
	MemoryParity      Exception = 8
	GatewayPath       Exception = 10
	GatewayNoResponse Exception = 11
)

var exceptionNames = map[Exception]string{1: "illegal function", 2: "illegal data address", 3: "illegal data value",
	4: "server device failure", 5: "acknowledge", 6: "server device busy", 8: "memory parity error",
	10: "gateway path unavailable", 11: "gateway target device failed to respond"}

func (e Exception) Error() string {
	if n, ok := exceptionNames[e]; ok {
		return fmt.Sprintf("modbus exception %d (%s)", uint8(e), n)
	}
	return fmt.Sprintf("modbus exception %d", uint8(e))
}

var mbErrors = map[uint8]error{1: mb.ErrIllegalFunction, 2: mb.ErrIllegalDataAddress, 3: mb.ErrIllegalDataValue,
	4: mb.ErrServerDeviceFailure, 5: mb.ErrAcknowledge, 6: mb.ErrServerDeviceBusy, 8: mb.ErrMemoryParityError,
	10: mb.ErrGWPathUnavailable, 11: mb.ErrGWTargetFailedToRespond}

// ExceptionCode returns the Modbus exception code err carries: an
// Exception, or one of github.com/simonvetter/modbus's exception errors
// (what package mbtcp returns for a device's exception reply).
func ExceptionCode(err error) (uint8, bool) {
	if err == nil {
		return 0, false
	}
	var e Exception
	if errors.As(err, &e) {
		return uint8(e), true
	}
	for c, me := range mbErrors {
		if errors.Is(err, me) {
			return c, true
		}
	}
	return 0, false
}

var (
	// ErrNoAnswer makes a serial server send nothing (a device that
	// stays silent).
	ErrNoAnswer = errors.New("sunspec device: no answer")
	// ErrUnknownUnit is a request for a unit the handler does not serve.
	ErrUnknownUnit = errors.New("sunspec device: unit not served")
)

// Mux routes requests by unit; the entry for unit 0, when present, takes
// the units without their own entry. It is a Handler.
type Mux map[uint8]Handler

func (m Mux) pick(unit uint8) (Handler, error) {
	if h, ok := m[unit]; ok && unit != 0 {
		return h, nil
	}
	if h, ok := m[0]; ok {
		return h, nil
	}
	return nil, ErrUnknownUnit
}

// ReadHolding passes the request to the unit's handler.
func (m Mux) ReadHolding(unit uint8, addr, count uint16) ([]uint16, error) {
	h, err := m.pick(unit)
	if err != nil {
		return nil, err
	}
	return h.ReadHolding(unit, addr, count)
}

// WriteHolding passes the request to the unit's handler.
func (m Mux) WriteHolding(unit uint8, addr uint16, words []uint16) error {
	h, err := m.pick(unit)
	if err != nil {
		return err
	}
	return h.WriteHolding(unit, addr, words)
}

// units filters a handler to some units.
type units struct {
	h  Handler
	ok map[uint8]bool
}

func filter(h Handler, list []uint8) Handler {
	if len(list) == 0 {
		return h
	}
	u := units{h: h, ok: map[uint8]bool{}}
	for _, x := range list {
		u.ok[x] = true
	}
	return u
}

func (u units) ReadHolding(unit uint8, addr, count uint16) ([]uint16, error) {
	if !u.ok[unit] {
		return nil, ErrUnknownUnit
	}
	return u.h.ReadHolding(unit, addr, count)
}

func (u units) WriteHolding(unit uint8, addr uint16, words []uint16) error {
	if !u.ok[unit] {
		return ErrUnknownUnit
	}
	return u.h.WriteHolding(unit, addr, words)
}

// Options shapes a server.
type Options struct {
	// Listen is the address ("host:port"; "" or port 0 picks a free port
	// on 127.0.0.1 or the given host).
	Listen string
	// Units are the units it answers on; none: every unit. Others get
	// exception 11 on Modbus TCP and silence on RTU and ASCII.
	Units []uint8
	// TLSCert, when set, serves Modbus TCP over TLS (Modbus/TCP
	// Security); TLSClientCAs are the CAs a client certificate must chain
	// to (required with TLSCert).
	TLSCert      *tls.Certificate
	TLSClientCAs *x509.CertPool
	// RTU serves Modbus RTU framing (with CRC) over TCP instead of Modbus
	// TCP, as a serial-to-Ethernet gateway does.
	RTU bool
	// ASCII serves Modbus ASCII framing (with LRC) over TCP.
	ASCII bool
}

// Server serves a Handler on a network address.
type Server struct {
	addr   string
	scheme string
	srv    *mb.ModbusServer
	ln     net.Listener
	mu     sync.Mutex
	conns  map[net.Conn]bool
	done   chan struct{}
	once   sync.Once
}

// Listen serves h as o says: Modbus TCP (functions 3, 6 and 16 on the
// holding registers; the others answer exception 1), Modbus TCP over TLS,
// or RTU or ASCII framing over TCP. Close stops it.
func Listen(h Handler, o Options) (*Server, error) {
	return listen(h, o, nil)
}

func listen(h Handler, o Options, done chan struct{}) (*Server, error) {
	if o.RTU && o.ASCII {
		return nil, errors.New("sunspec device: RTU and ASCII framing together")
	}
	if (o.RTU || o.ASCII) && o.TLSCert != nil {
		return nil, errors.New("sunspec device: TLS is served with Modbus TCP framing only")
	}
	h = filter(h, o.Units)
	listenAt := o.Listen
	if listenAt == "" {
		listenAt = "127.0.0.1:0"
	}
	host, port, err := net.SplitHostPort(listenAt)
	if err != nil {
		return nil, err
	}
	if done == nil {
		done = make(chan struct{})
	}
	s := &Server{conns: map[net.Conn]bool{}, done: done}
	if o.RTU || o.ASCII {
		ln, err := net.Listen("tcp", listenAt)
		if err != nil {
			return nil, fmt.Errorf("sunspec device: listening on %s: %w", listenAt, err)
		}
		s.ln, s.addr, s.scheme = ln, ln.Addr().String(), "rtuovertcp://"
		serve := serveRTU
		if o.ASCII {
			s.scheme, serve = "asciiovertcp://", serveASCII
		}
		go s.accept(h, serve)
		return s, nil
	}
	if port == "0" {
		// The server library does not report the port it bound, so take
		// a free one first.
		l, err := net.Listen("tcp", net.JoinHostPort(host, "0"))
		if err != nil {
			return nil, err
		}
		listenAt = l.Addr().String()
		_ = l.Close()
	}
	s.addr, s.scheme = listenAt, "tcp://"
	conf := &mb.ServerConfiguration{URL: "tcp://" + listenAt, Timeout: 5 * time.Second, MaxClients: 8, Logger: log.New(io.Discard, "", 0)}
	if o.TLSCert != nil {
		if o.TLSClientCAs == nil {
			return nil, errors.New("sunspec device: TLS needs TLSClientCAs for the client certificates")
		}
		s.scheme = "tcp+tls://"
		conf.URL, conf.TLSServerCert, conf.TLSClientCAs = "tcp+tls://"+listenAt, o.TLSCert, o.TLSClientCAs
	}
	srv, err := mb.NewServer(conf, mbHandler{h})
	if err != nil {
		return nil, err
	}
	if err := srv.Start(); err != nil {
		return nil, fmt.Errorf("sunspec device: listening on %s: %w", listenAt, err)
	}
	s.srv = srv
	return s, nil
}

// Addr is the address the server listens on ("127.0.0.1:50211").
func (s *Server) Addr() string { return s.addr }

// URL is the server's URL ("tcp://127.0.0.1:50211", "tcp+tls://…",
// "rtuovertcp://…", "asciiovertcp://…"), as package mbtcp takes it.
func (s *Server) URL() string { return s.scheme + s.addr }

// Close stops the server and closes its connections.
func (s *Server) Close() error {
	var err error
	s.once.Do(func() {
		select {
		case <-s.done:
		default:
			close(s.done)
		}
		if s.srv != nil {
			err = s.srv.Stop()
		}
		if s.ln != nil {
			err = s.ln.Close()
			s.mu.Lock()
			for c := range s.conns {
				_ = c.Close()
			}
			s.mu.Unlock()
		}
	})
	return err
}

func (s *Server) accept(h Handler, serve func(Handler, io.ReadWriter, <-chan struct{}) error) {
	for {
		c, err := s.ln.Accept()
		if err != nil {
			return
		}
		s.mu.Lock()
		s.conns[c] = true
		s.mu.Unlock()
		go func() {
			_ = serve(h, c, s.done)
			_ = c.Close()
			s.mu.Lock()
			delete(s.conns, c)
			s.mu.Unlock()
		}()
	}
}

// mbHandler puts a Handler behind github.com/simonvetter/modbus's server.
type mbHandler struct{ h Handler }

func mbError(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, ErrUnknownUnit):
		return mb.ErrGWTargetFailedToRespond
	}
	if c, ok := ExceptionCode(err); ok {
		if e, ok := mbErrors[c]; ok {
			return e
		}
	}
	return mb.ErrServerDeviceFailure
}

func (m mbHandler) HandleHoldingRegisters(r *mb.HoldingRegistersRequest) ([]uint16, error) {
	if r.IsWrite {
		return nil, mbError(m.h.WriteHolding(r.UnitId, r.Addr, r.Args))
	}
	out, err := m.h.ReadHolding(r.UnitId, r.Addr, r.Quantity)
	return out, mbError(err)
}

func (mbHandler) HandleInputRegisters(*mb.InputRegistersRequest) ([]uint16, error) {
	return nil, mb.ErrIllegalFunction
}

func (mbHandler) HandleCoils(*mb.CoilsRequest) ([]bool, error) { return nil, mb.ErrIllegalFunction }

func (mbHandler) HandleDiscreteInputs(*mb.DiscreteInputsRequest) ([]bool, error) {
	return nil, mb.ErrIllegalFunction
}

// crc16 is the Modbus RTU check (polynomial 0xA001, reflected, from
// 0xFFFF), sent low byte first.
func crc16(b []byte) uint16 {
	crc := uint16(0xFFFF)
	for _, x := range b {
		crc ^= uint16(x)
		for range 8 {
			if crc&1 != 0 {
				crc = crc>>1 ^ 0xA001
			} else {
				crc >>= 1
			}
		}
	}
	return crc
}

// ServeRTU answers Modbus RTU frames read from rw (a serial port the
// caller opened, or a connection) until rw fails: functions 3, 6 and 16
// on the holding registers, exception 1 for functions 1 to 5 and 15; a
// frame with a bad check, a unit h does not serve, or a broadcast (unit
// 0) gets no reply. Close rw to stop it.
func ServeRTU(h Handler, rw io.ReadWriter) error { return serveRTU(h, rw, nil) }

// ServeASCII answers Modbus ASCII frames read from rw until rw fails, as
// ServeRTU does for RTU; a frame with a bad LRC gets no reply.
func ServeASCII(h Handler, rw io.ReadWriter) error { return serveASCII(h, rw, nil) }

func closed(done <-chan struct{}) bool {
	if done == nil {
		return false
	}
	select {
	case <-done:
		return true
	default:
		return false
	}
}

func serveRTU(h Handler, rw io.ReadWriter, done <-chan struct{}) error {
	for {
		if closed(done) {
			return nil
		}
		frame, err := readRTU(rw)
		if err != nil {
			return err
		}
		if len(frame) < 4 || crc16(frame[:len(frame)-2]) != binary.LittleEndian.Uint16(frame[len(frame)-2:]) {
			continue
		}
		resp := answerADU(h, frame[:len(frame)-2])
		if resp == nil {
			continue
		}
		resp = binary.LittleEndian.AppendUint16(resp, crc16(resp))
		if _, err := rw.Write(resp); err != nil {
			return err
		}
	}
}

func serveASCII(h Handler, rw io.ReadWriter, done <-chan struct{}) error {
	br := bufio.NewReader(rw)
	for {
		if closed(done) {
			return nil
		}
		frame, err := ascii.ReadFrame(br)
		if err != nil {
			return err
		}
		adu, err := ascii.Decode(frame)
		if err != nil {
			continue
		}
		resp := answerADU(h, adu)
		if resp == nil {
			continue
		}
		if _, err := rw.Write(ascii.Encode(resp)); err != nil {
			return err
		}
	}
}

// readRTU reads one request frame, its length known from its function.
func readRTU(r io.Reader) ([]byte, error) {
	hdr := make([]byte, 2)
	if _, err := io.ReadFull(r, hdr); err != nil {
		return nil, err
	}
	var rest []byte
	switch hdr[1] {
	case 1, 2, 3, 4, 5, 6:
		rest = make([]byte, 6)
		if _, err := io.ReadFull(r, rest); err != nil {
			return nil, err
		}
	case 15, 16:
		head := make([]byte, 5)
		if _, err := io.ReadFull(r, head); err != nil {
			return nil, err
		}
		tail := make([]byte, int(head[4])+2)
		if _, err := io.ReadFull(r, tail); err != nil {
			return nil, err
		}
		rest = append(head, tail...)
	default:
		return nil, fmt.Errorf("sunspec device: RTU function %d cannot be framed", hdr[1])
	}
	return append(hdr, rest...), nil
}

// answerADU answers a serial request (unit address and PDU, without the
// check) with the reply's unit address and PDU, or nil for no reply.
func answerADU(h Handler, adu []byte) []byte {
	if len(adu) < 2 {
		return nil
	}
	unit := adu[0]
	if unit == 0 {
		return nil
	}
	resp := answerPDU(h, unit, adu[1], adu[2:])
	if resp == nil {
		return nil
	}
	return append([]byte{unit}, resp...)
}

// answerPDU is the PDU answering one request PDU (nil: no answer).
func answerPDU(h Handler, unit, fc uint8, pdu []byte) []byte {
	exc := func(code uint8) []byte { return []byte{fc | 0x80, code} }
	switch fc {
	case 3, 6, 16:
	default:
		return exc(1)
	}
	if len(pdu) < 4 {
		return exc(3)
	}
	addr, qty := binary.BigEndian.Uint16(pdu), binary.BigEndian.Uint16(pdu[2:])
	var (
		out []uint16
		err error
	)
	switch fc {
	case 3:
		if qty < 1 || qty > 125 {
			return exc(3)
		}
		out, err = h.ReadHolding(unit, addr, qty)
		if err == nil && len(out) != int(qty) {
			err = DeviceFailure
		}
	case 6:
		err = h.WriteHolding(unit, addr, []uint16{qty})
	case 16:
		if qty < 1 || qty > 123 || len(pdu) < 5 || int(pdu[4]) != 2*int(qty) || len(pdu) < 5+2*int(qty) {
			return exc(3)
		}
		args := make([]uint16, qty)
		for i := range args {
			args[i] = binary.BigEndian.Uint16(pdu[5+2*i:])
		}
		err = h.WriteHolding(unit, addr, args)
	}
	switch {
	case errors.Is(err, ErrNoAnswer), errors.Is(err, ErrUnknownUnit):
		return nil
	case err != nil:
		code, ok := ExceptionCode(err)
		if !ok {
			code = 4
		}
		return exc(code)
	case fc == 3:
		b := []byte{3, byte(2 * len(out))}
		for _, w := range out {
			b = binary.BigEndian.AppendUint16(b, w)
		}
		return b
	}
	return append([]byte{fc}, pdu[:4]...)
}

// Copyright 2026 Abhinav Gautam
// SPDX-License-Identifier: Apache-2.0

package device_test

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/binary"
	"errors"
	"fmt"
	"math/big"
	"net"
	"slices"
	"strings"
	"testing"
	"time"

	mb "github.com/simonvetter/modbus"

	"github.com/cB-Sigma/sunspec"
	"github.com/cB-Sigma/sunspec/device"
	"github.com/cB-Sigma/sunspec/mbtcp"
)

func start(t *testing.T, im *device.Image, units ...uint8) *device.Device {
	t.Helper()
	d, err := device.Start(im, "", units...)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })
	return d
}

func dial(t *testing.T, url string, unit uint8) *mbtcp.Reader {
	t.Helper()
	r, err := mbtcp.Dial(url, unit, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	return r
}

// demo is a small inverter map: common, 704 with its writable points, a
// vendor model.
func demo(t *testing.T, base uint16) (*device.Image, uint16) {
	t.Helper()
	im := device.NewImage(base)
	if _, err := im.Add(1, map[string]any{"Mn": "Example", "Md": "EX-1", "SN": "S1", "DA": 1}); err != nil {
		t.Fatal(err)
	}
	ctl, err := im.Add(704, map[string]any{"WMaxLimPctEna": 0, "WMaxLimPct": 1000, "WMaxLimPct_SF": -1, "PF_SF": -3,
		"PFWInj": map[string]any{"PF": 1000, "Ext": 0}})
	if err != nil {
		t.Fatal(err)
	}
	im.Model(64900, []uint16{1, 2, 3})
	im.End()
	return im, ctl
}

func TestImageLayout(t *testing.T) {
	im := device.NewImage(50000)
	a := im.Model(1, device.Str("Maker", 2))
	im.End()
	r := dial(t, start(t, im).URL(), 1)
	got, err := r.ReadHolding(50000, 8)
	want := []uint16{0x5375, 0x6E53, 1, 2, 0x4D61, 0x6B65, 0xFFFF, 0}
	if err != nil || !slices.Equal(got, want) || a != 50004 || im.Next() != 50008 {
		t.Errorf("%#x %v (model at %d, next %d)", got, err, a, im.Next())
	}
	if !slices.Equal(device.Str("abc", 3), []uint16{0x6162, 0x6300, 0}) || !slices.Equal(device.Filled(2, 7), []uint16{7, 7}) {
		t.Error("Str/Filled")
	}
	// An image is a sunspec.Reader: walk it without a network.
	m, err := sunspec.Find(im)
	if err != nil || len(m.Models) != 1 || m.Base != 50000 {
		t.Errorf("Find on the image: %+v %v", m, err)
	}
	if _, err := im.ReadHolding(49999, 1); !im.Refused(err) {
		t.Errorf("outside: %v", err)
	}
}

// Writes land on writable registers only (a refused write changes
// nothing), SetKeep changes the value kept, the log has what was written,
// and SetOnWrite sees the write under the device's lock.
func TestWritesAndHooks(t *testing.T) {
	im, ctl := demo(t, 40000)
	d := start(t, im)
	r := dial(t, d.URL(), 1)
	var seen []device.Request
	d.SetOnWrite(func(im *device.Image, w device.Request) {
		seen = append(seen, w)
		if w.Addr == ctl+13 {
			im.Set(ctl+14, w.Words[0]) // a made-up device that mirrors the limit
		}
	})
	if err := r.WriteHolding(ctl+12, []uint16{1, 600}); err != nil {
		t.Fatal(err)
	}
	if d.Get(ctl+12) != 1 || d.Get(ctl+13) != 600 || d.Get(ctl+14) != 0xFFFF {
		t.Errorf("registers %d %d %d", d.Get(ctl+12), d.Get(ctl+13), d.Get(ctl+14))
	}
	if err := r.WriteHolding(ctl+13, []uint16{700}); err != nil || d.Get(ctl+14) != 700 {
		t.Errorf("hook: %v %d", err, d.Get(ctl+14))
	}
	if len(seen) != 2 || seen[0].Count != 2 || !seen[0].Write {
		t.Errorf("hook saw %+v", seen)
	}
	for _, w := range []struct {
		addr  uint16
		words []uint16
	}{{ctl + 3, []uint16{1, 2}}, {40000, []uint16{0}}, {ctl + 51, []uint16{1}}} {
		if err := r.WriteHolding(w.addr, w.words); !errors.Is(err, mb.ErrIllegalDataAddress) {
			t.Errorf("write %d: %v", w.addr, err)
		}
	}
	d.SetKeep(func(addr, v uint16) uint16 { return min(v, 900) })
	if err := r.WriteHolding(ctl+13, []uint16{1200}); err != nil || d.Get(ctl+13) != 900 {
		t.Errorf("kept %d %v", d.Get(ctl+13), err)
	}
	log := d.WriteLog()
	if !slices.Equal(log, []string{fmt.Sprintf("%d=1", ctl+12), fmt.Sprintf("%d=600", ctl+13), fmt.Sprintf("%d=700", ctl+13), fmt.Sprintf("%d=1200", ctl+13)}) {
		t.Errorf("log %v", log)
	}
	// SetBefore refuses, or lets through.
	d.SetBefore(func(r device.Request) error {
		if r.Write {
			return device.Busy
		}
		return nil
	})
	if err := r.WriteHolding(ctl+13, []uint16{1}); !errors.Is(err, mb.ErrServerDeviceBusy) {
		t.Errorf("before: %v", err)
	}
	if _, err := r.ReadHolding(40000, 2); err != nil {
		t.Errorf("read with a before hook: %v", err)
	}
	d.Update(func(im *device.Image) { im.Set(ctl+14, 5) })
	if d.Get(ctl+14) != 5 {
		t.Error("Update")
	}
}

func TestUnitsAndFunctions(t *testing.T) {
	im, _ := demo(t, 40000)
	d := start(t, im, 126)
	if _, err := dial(t, d.URL(), 126).ReadHolding(40000, 2); err != nil {
		t.Errorf("unit 126: %v", err)
	}
	if _, err := dial(t, d.URL(), 1).ReadHolding(40000, 2); !errors.Is(err, mb.ErrGWTargetFailedToRespond) {
		t.Errorf("unit 1: %v", err)
	}
	c, err := mb.NewClient(&mb.ClientConfiguration{URL: d.URL(), Timeout: time.Second})
	if err != nil || c.Open() != nil {
		t.Fatal(err)
	}
	defer c.Close()
	_ = c.SetUnitId(126)
	if _, err := c.ReadRegisters(40000, 1, mb.INPUT_REGISTER); !errors.Is(err, mb.ErrIllegalFunction) {
		t.Errorf("input registers: %v", err)
	}
	if _, err := c.ReadCoils(0, 1); !errors.Is(err, mb.ErrIllegalFunction) {
		t.Errorf("coils: %v", err)
	}
}

// A Mux serves different handlers on different units over one listener;
// unit 0's entry takes the rest.
func TestMux(t *testing.T) {
	a, _ := demo(t, 40000)
	b := device.NewImage(50000)
	b.Model(64900, []uint16{1})
	b.End()
	srv, err := device.Listen(device.Mux{1: device.NewDevice(a), 2: device.NewDevice(b)}, device.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()
	r := dial(t, srv.URL(), 1)
	if m, err := sunspec.Find(r); err != nil || m.Base != 40000 {
		t.Errorf("unit 1: %+v %v", m, err)
	}
	if m, err := sunspec.Find(r.ForUnit(2)); err != nil || m.Base != 50000 {
		t.Errorf("unit 2: %+v %v", m, err)
	}
	if _, err := r.ForUnit(3).ReadHolding(40000, 2); !errors.Is(err, mb.ErrGWTargetFailedToRespond) {
		t.Errorf("unit 3: %v", err)
	}
	srv2, err := device.Listen(device.Mux{0: device.NewDevice(a)}, device.Options{Units: []uint8{7}})
	if err != nil {
		t.Fatal(err)
	}
	defer srv2.Close()
	if _, err := dial(t, srv2.URL(), 7).ReadHolding(40000, 2); err != nil {
		t.Errorf("unit 7 through the default entry: %v", err)
	}
	if _, err := dial(t, srv2.URL(), 8).ReadHolding(40000, 2); !errors.Is(err, mb.ErrGWTargetFailedToRespond) {
		t.Errorf("unit 8 filtered: %v", err)
	}
}

func certs(t *testing.T) (*x509.CertPool, tls.Certificate, tls.Certificate) {
	t.Helper()
	key := func() *ecdsa.PrivateKey {
		k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if err != nil {
			t.Fatal(err)
		}
		return k
	}
	caKey := key()
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "test CA"}, NotBefore: time.Now().Add(-time.Hour),
		NotAfter: time.Now().Add(time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &caKey.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	ca, _ := x509.ParseCertificate(der)
	leaf := func(n int64, usage x509.ExtKeyUsage) tls.Certificate {
		k := key()
		c := &x509.Certificate{SerialNumber: big.NewInt(n), Subject: pkix.Name{CommonName: fmt.Sprint("leaf ", n)}, NotBefore: time.Now().Add(-time.Hour),
			NotAfter: time.Now().Add(time.Hour), ExtKeyUsage: []x509.ExtKeyUsage{usage}, KeyUsage: x509.KeyUsageDigitalSignature,
			IPAddresses: []net.IP{net.IPv4(127, 0, 0, 1)}}
		d, err := x509.CreateCertificate(rand.Reader, c, ca, &k.PublicKey, caKey)
		if err != nil {
			t.Fatal(err)
		}
		return tls.Certificate{Certificate: [][]byte{d}, PrivateKey: k}
	}
	pool := x509.NewCertPool()
	pool.AddCert(ca)
	return pool, leaf(2, x509.ExtKeyUsageServerAuth), leaf(3, x509.ExtKeyUsageClientAuth)
}

// Every transport the device serves, read and written with mbtcp: Modbus
// TCP over TLS, RTU framing over TCP, ASCII framing over TCP.
func TestTransports(t *testing.T) {
	pool, server, client := certs(t)
	im, ctl := demo(t, 40000)
	d, err := device.StartOptions(im, device.Options{TLSCert: &server, TLSClientCAs: pool})
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if !strings.HasPrefix(d.URL(), "tcp+tls://") {
		t.Errorf("URL %s", d.URL())
	}
	r, err := mbtcp.Open(mbtcp.Config{URL: d.URL(), Unit: 1, TLSClientCert: &client, TLSRootCAs: pool})
	if err != nil {
		t.Fatal(err)
	}
	if m, err := sunspec.Find(r); err != nil || len(m.Models) != 3 {
		t.Errorf("TLS: %+v %v", m, err)
	}
	_ = r.Close()
	if _, err := device.StartOptions(im, device.Options{TLSCert: &server}); err == nil {
		t.Error("TLS without client CAs")
	}
	if _, err := device.StartOptions(im, device.Options{RTU: true, ASCII: true}); err == nil {
		t.Error("RTU and ASCII together")
	}

	for _, o := range []device.Options{{RTU: true, Units: []uint8{126}}, {ASCII: true, Units: []uint8{126}}} {
		im, ctl = demo(t, 50000)
		d, err := device.StartOptions(im, o)
		if err != nil {
			t.Fatal(err)
		}
		defer d.Close()
		r, err := mbtcp.Open(mbtcp.Config{URL: d.URL(), Unit: 126, Timeout: 500 * time.Millisecond})
		if err != nil {
			t.Fatal(err)
		}
		defer r.Close()
		m, err := sunspec.Find(r)
		if err != nil || m.Base != 50000 || len(m.Models) != 3 {
			t.Fatalf("%s: %+v %v", d.URL(), m, err)
		}
		regs, err := sunspec.ReadModel(r, m.Models[0])
		if err != nil || len(regs) != m.Models[0].L {
			t.Errorf("%s: model 1: %v", d.URL(), err)
		}
		if err := r.WriteHolding(ctl+13, []uint16{700}); err != nil || d.Get(ctl+13) != 700 {
			t.Errorf("%s write: %v", d.URL(), err)
		}
		if err := r.WriteHolding(ctl+12, []uint16{1, 650}); err != nil || d.Get(ctl+13) != 650 {
			t.Errorf("%s write of two: %v", d.URL(), err)
		}
		if err := r.WriteHolding(ctl+51, []uint16{1}); !r.Refused(err) || !errors.Is(err, mb.ErrIllegalDataAddress) {
			t.Errorf("%s write to a read-only register: %v", d.URL(), err)
		}
		if _, err := r.ForUnit(1).ReadHolding(50000, 2); err == nil || r.Refused(err) {
			t.Errorf("%s another unit: %v, want silence", d.URL(), err)
		}
		// The connection still works after a timeout.
		if _, err := r.ReadHolding(50000, 2); err != nil {
			t.Errorf("%s after a timeout: %v", d.URL(), err)
		}
	}
}

func crcOf(b []byte) uint16 {
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

// ServeRTU and ServeASCII on a byte stream: a frame with a bad check gets
// no answer, a good one its registers; an unknown function exception 1.
func TestServeOnAStream(t *testing.T) {
	im := device.NewImage(40000)
	im.Model(1, device.Filled(66, 0))
	im.End()
	d := device.NewDevice(im)
	defer d.Close()

	a, b := net.Pipe()
	defer a.Close()
	go func() { _ = d.ServeRTU(b) }()
	frame := func(unit, fc byte, addr, n uint16) []byte {
		f := []byte{unit, fc}
		f = binary.BigEndian.AppendUint16(f, addr)
		return binary.BigEndian.AppendUint16(f, n)
	}
	bad := append(frame(1, 3, 40000, 2), 0, 0)
	good := frame(1, 3, 40000, 2)
	good = binary.LittleEndian.AppendUint16(good, crcOf(good))
	_ = a.SetDeadline(time.Now().Add(2 * time.Second))
	if _, err := a.Write(append(bad, good...)); err != nil {
		t.Fatal(err)
	}
	resp := make([]byte, 9)
	if _, err := a.Read(resp); err != nil {
		t.Fatal(err)
	}
	if resp[0] != 1 || resp[1] != 3 || resp[2] != 4 || binary.BigEndian.Uint16(resp[3:]) != 0x5375 || crcOf(resp[:7]) != binary.LittleEndian.Uint16(resp[7:]) {
		t.Errorf("RTU answer % x", resp)
	}

	c, e := net.Pipe()
	defer c.Close()
	go func() { _ = device.ServeASCII(d, e) }()
	_ = c.SetDeadline(time.Now().Add(2 * time.Second))
	// A bad LRC, then reading 2 registers at 40000 (0x9C40) of unit 1,
	// then function 4 (input registers).
	if _, err := c.Write([]byte(":01039C400002FF\r\n:01039C4000021E\r\n:01049C4000021D\r\n")); err != nil {
		t.Fatal(err)
	}
	want := ":0103045375 6E53 6F\r\n:018401 7A\r\n"
	want = strings.ReplaceAll(want, " ", "")
	got := make([]byte, 0, len(want))
	buf := make([]byte, 64)
	for len(got) < len(want) {
		n, err := c.Read(buf)
		if err != nil {
			t.Fatalf("ASCII answer so far %q: %v", got, err)
		}
		got = append(got, buf[:n]...)
	}
	if string(got) != want {
		t.Errorf("ASCII answer %q, want %q", got, want)
	}
}

func TestExceptionCode(t *testing.T) {
	for _, c := range []struct {
		err  error
		code uint8
		ok   bool
	}{{device.Busy, 6, true}, {fmt.Errorf("x: %w", device.IllegalAddress), 2, true}, {mb.ErrGWPathUnavailable, 10, true},
		{errors.New("other"), 0, false}, {nil, 0, false}} {
		code, ok := device.ExceptionCode(c.err)
		if code != c.code || ok != c.ok {
			t.Errorf("%v: %d %v", c.err, code, ok)
		}
	}
	if device.Busy.Error() != "modbus exception 6 (server device busy)" || device.Exception(7).Error() != "modbus exception 7" {
		t.Error(device.Busy.Error())
	}
}

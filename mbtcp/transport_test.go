// Copyright 2026 Abhinav Gautam
// SPDX-License-Identifier: Apache-2.0

package mbtcp_test

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/binary"
	"errors"
	"io"
	"math/big"
	"net"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	mb "github.com/simonvetter/modbus"

	"github.com/cB-Sigma/sunspec"
	"github.com/cB-Sigma/sunspec/mbtcp"
)

// sunsMap is a small SunSpec map at 40000: the common model and the end.
func sunsMap() map[uint16]uint16 {
	regs := map[uint16]uint16{40000: 0x5375, 40001: 0x6E53, 40002: 1, 40003: 66}
	for i := uint16(0); i < 66; i++ {
		regs[40004+i] = 0
	}
	regs[40004] = 0x4142 // "AB"
	regs[40070], regs[40071] = 0xFFFF, 0
	return regs
}

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

// rtuServer answers Modbus RTU frames (functions 3, 6 and 16) over TCP
// for one unit: the framing of a serial line behind a gateway.
func rtuServer(t *testing.T, unit uint8, regs map[uint16]uint16) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	var mu sync.Mutex
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				for {
					hdr := make([]byte, 2)
					if _, err := io.ReadFull(c, hdr); err != nil {
						return
					}
					var body []byte
					switch hdr[1] {
					case 3, 6:
						body = make([]byte, 6)
					case 16:
						body = make([]byte, 5)
						if _, err := io.ReadFull(c, body); err != nil {
							return
						}
						rest := make([]byte, int(body[4])+2)
						if _, err := io.ReadFull(c, rest); err != nil {
							return
						}
						body = append(body, rest...)
					default:
						return
					}
					if hdr[1] != 16 {
						if _, err := io.ReadFull(c, body); err != nil {
							return
						}
					}
					frame := append(hdr, body...)
					if crc16(frame[:len(frame)-2]) != binary.LittleEndian.Uint16(frame[len(frame)-2:]) {
						return
					}
					if hdr[0] != unit {
						continue // another unit on the line: silence
					}
					addr := binary.BigEndian.Uint16(frame[2:])
					resp := []byte{unit, hdr[1]}
					mu.Lock()
					switch hdr[1] {
					case 3:
						n := binary.BigEndian.Uint16(frame[4:])
						resp = append(resp, byte(2*n))
						for i := uint16(0); i < n; i++ {
							v, ok := regs[addr+i]
							if !ok {
								resp = []byte{unit, 0x83, 2}
								break
							}
							resp = binary.BigEndian.AppendUint16(resp, v)
						}
					case 6:
						regs[addr] = binary.BigEndian.Uint16(frame[4:])
						resp = append(resp, frame[2:6]...)
					case 16:
						n := binary.BigEndian.Uint16(frame[4:])
						for i := uint16(0); i < n; i++ {
							regs[addr+i] = binary.BigEndian.Uint16(frame[7+2*i:])
						}
						resp = append(resp, frame[2:6]...)
					}
					mu.Unlock()
					resp = binary.LittleEndian.AppendUint16(resp, crc16(resp))
					if _, err := c.Write(resp); err != nil {
						return
					}
				}
			}()
		}
	}()
	return ln.Addr().String()
}

// SunSpec over Modbus RTU framing (through a TCP gateway): the map is
// found, read and written; a unit not on the line is silence, not a
// refusal.
func TestRTUOverTCP(t *testing.T) {
	regs := sunsMap()
	addr := rtuServer(t, 7, regs)
	r, err := mbtcp.Open(mbtcp.Config{URL: "rtuovertcp://" + addr, Unit: 7, Timeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	m, err := sunspec.Find(r)
	if err != nil || m.Base != 40000 || len(m.Models) != 1 {
		t.Fatalf("Find: %+v %v", m, err)
	}
	id, err := sunspec.ReadIdentity(r, m)
	if err != nil || id.Manufacturer != "AB" {
		t.Errorf("identity %+v %v", id, err)
	}
	if err := r.WriteHolding(40068, []uint16{9}); err != nil {
		t.Fatal(err)
	}
	if err := r.WriteHolding(40068, []uint16{9, 0}); err != nil {
		t.Fatal(err)
	}
	if got, _ := r.ReadHolding(40068, 1); !slices.Equal(got, []uint16{9}) {
		t.Errorf("read back %v", got)
	}
	other := r.ForUnit(8)
	if _, err := other.ReadHolding(40000, 2); err == nil || other.Refused(err) {
		t.Errorf("unit 8: %v", err)
	}
	if _, err := r.ReadHolding(40000, 2); err != nil {
		t.Errorf("unit 7 after unit 8: %v", err)
	}
}

func certs(t *testing.T) (ca *x509.Certificate, server, client tls.Certificate) {
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
	ca, _ = x509.ParseCertificate(der)
	leaf := func(n int64, name string, usage x509.ExtKeyUsage) tls.Certificate {
		k := key()
		c := &x509.Certificate{SerialNumber: big.NewInt(n), Subject: pkix.Name{CommonName: name}, NotBefore: time.Now().Add(-time.Hour),
			NotAfter: time.Now().Add(time.Hour), ExtKeyUsage: []x509.ExtKeyUsage{usage}, KeyUsage: x509.KeyUsageDigitalSignature,
			IPAddresses: []net.IP{net.IPv4(127, 0, 0, 1)}}
		d, err := x509.CreateCertificate(rand.Reader, c, ca, &k.PublicKey, caKey)
		if err != nil {
			t.Fatal(err)
		}
		return tls.Certificate{Certificate: [][]byte{d}, PrivateKey: k}
	}
	return ca, leaf(2, "server", x509.ExtKeyUsageServerAuth), leaf(3, "client", x509.ExtKeyUsageClientAuth)
}

// SunSpec over Modbus/TCP Security (TLS with client certificates): the
// map is found; without a client certificate Open says what is missing.
func TestTLS(t *testing.T) {
	ca, server, client := certs(t)
	pool := x509.NewCertPool()
	pool.AddCert(ca)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()
	d := &device{unit: 1, regs: sunsMap()}
	srv, err := mb.NewServer(&mb.ServerConfiguration{URL: "tcp+tls://" + addr, Timeout: 5 * time.Second, MaxClients: 2,
		TLSServerCert: &server, TLSClientCAs: pool}, d)
	if err != nil {
		t.Fatal(err)
	}
	if err := srv.Start(); err != nil {
		t.Fatal(err)
	}
	defer srv.Stop()
	r, err := mbtcp.Open(mbtcp.Config{URL: "tcp+tls://" + addr, Unit: 1, TLSClientCert: &client, TLSRootCAs: pool})
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	if m, err := sunspec.Find(r); err != nil || m.Base != 40000 {
		t.Errorf("Find over TLS: %+v %v", m, err)
	}
	if _, err := mbtcp.Open(mbtcp.Config{URL: "tcp+tls://" + addr, Unit: 1}); err == nil || !strings.Contains(err.Error(), "TLSClientCert") {
		t.Errorf("no client certificate: %v", err)
	}
}

// Several units on one connection; a closed connection fails every
// Reader sharing it.
func TestForUnitAndOpenErrors(t *testing.T) {
	d := &device{unit: 3, regs: sunsMap()}
	url := serve(t, d)
	r, err := mbtcp.Open(mbtcp.Config{URL: url, Unit: 1})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.ReadHolding(40000, 2); !r.Refused(err) {
		t.Errorf("unit 1: %v", err)
	}
	u3 := r.ForUnit(3)
	if got, err := u3.ReadHolding(40000, 2); err != nil || got[0] != 0x5375 || u3.Unit() != 3 {
		t.Errorf("unit 3: %v %v", got, err)
	}
	_ = r.Close()
	if _, err := u3.ReadHolding(40000, 2); err == nil {
		t.Error("read after Close")
	}
	if err := u3.Close(); err != nil {
		t.Errorf("second Close: %v", err)
	}
	for _, c := range []mbtcp.Config{{URL: "rtu:///dev/null", Parity: "X"}, {URL: "nope://x"}} {
		if _, err := mbtcp.Open(c); err == nil {
			t.Errorf("%+v opened", c)
		}
	}
}

// Scan over one connection: the unit with a map is found with its
// identity, the others are told apart (refused: no SunSpec; silent: the
// read error).
func TestScanUnits(t *testing.T) {
	regs := sunsMap()
	addr := rtuServer(t, 5, regs)
	r, err := mbtcp.Open(mbtcp.Config{URL: "rtuovertcp://" + addr, Unit: 1, Timeout: 200 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	found := sunspec.Scan([]uint8{4, 5}, func(u uint8) sunspec.Reader { return r.ForUnit(u) })
	if len(found) != 2 || found[0].Err == nil || errors.Is(found[0].Err, sunspec.ErrNotSunSpec) {
		t.Errorf("unit 4: %+v", found[0])
	}
	if f := found[1]; f.Err != nil || f.Unit != 5 || f.Map.Base != 40000 || f.Identity.Manufacturer != "AB" {
		t.Errorf("unit 5: %+v", f)
	}
	d := &device{unit: 9, regs: map[uint16]uint16{40000: 1, 40001: 2}}
	tr, err := mbtcp.Dial(serve(t, d), 9, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer tr.Close()
	found = sunspec.Scan([]uint8{8, 9}, func(u uint8) sunspec.Reader { return tr.ForUnit(u) })
	if !errors.Is(found[0].Err, sunspec.ErrNotSunSpec) || !errors.Is(found[1].Err, sunspec.ErrNotSunSpec) {
		t.Errorf("refused unit and a unit without a map: %v, %v", found[0].Err, found[1].Err)
	}
}

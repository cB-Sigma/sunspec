// Copyright 2026 Abhinav Gautam
// SPDX-License-Identifier: Apache-2.0

package mbtcp_test

import (
	"bufio"
	"errors"
	"net"
	"testing"
	"time"

	mb "github.com/simonvetter/modbus"

	"github.com/cB-Sigma/sunspec/mbtcp"
)

// A scripted Modbus ASCII peer over TCP: each request line gets the
// replies listed for it, so the client meets a late reply to an earlier
// request, a reply from another unit, an exception and a bad LRC.
func TestASCIIClient(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	script := map[string]string{
		// Read 1 register at 0 of unit 1: first a stale reply to a write
		// (function 6) and one from unit 2, then the reply (value 0x1234).
		":010300000001FB\r\n": ":010600010003F5\r\n:020302ABCD81\r\n:0103021234B4\r\n",
		// Read 1 register at 1: exception 2.
		":010300010001FA\r\n": ":0183027A\r\n",
		// Write 3 to register 1: the echo.
		":010600010003F5\r\n": ":010600010003F5\r\n",
		// Read 1 register at 2: a bad LRC.
		":010300020001F9\r\n": ":01030212340\r\n",
	}
	go func() {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		br := bufio.NewReader(c)
		for {
			line, err := br.ReadString('\n')
			if err != nil {
				return
			}
			if reply, ok := script[line]; ok {
				_, _ = c.Write([]byte(reply))
			}
		}
	}()
	r, err := mbtcp.Open(mbtcp.Config{URL: "asciiovertcp://" + ln.Addr().String(), Unit: 1, Timeout: 500 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	got, err := r.ReadHolding(0, 1)
	if err != nil || len(got) != 1 || got[0] != 0x1234 {
		t.Errorf("read: %#x %v", got, err)
	}
	if _, err := r.ReadHolding(1, 1); !errors.Is(err, mb.ErrIllegalDataAddress) || !r.Refused(err) {
		t.Errorf("exception: %v", err)
	}
	if err := r.WriteHolding(1, []uint16{3}); err != nil {
		t.Errorf("write: %v", err)
	}
	if _, err := r.ReadHolding(2, 1); err == nil || r.Refused(err) {
		t.Errorf("bad LRC: %v", err)
	}
	// Unanswered: a timeout, not a refusal.
	if _, err := r.ReadHolding(9, 1); !errors.Is(err, mb.ErrRequestTimedOut) {
		t.Errorf("silence: %v", err)
	}
	if _, err := mbtcp.Open(mbtcp.Config{URL: "ascii:///dev/no-such-port-here", Unit: 1}); err == nil {
		t.Error("a missing serial port opened")
	}
	if _, err := r.ReadHolding(0, 200); err == nil {
		t.Error("200 registers in one read")
	}
}

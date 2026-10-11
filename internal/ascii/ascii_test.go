// Copyright 2026 Abhinav Gautam
// SPDX-License-Identifier: Apache-2.0

package ascii

import (
	"bufio"
	"errors"
	"strings"
	"testing"
)

// Known frames: read one holding register at 0 of unit 1 (":010300000001FB"),
// the serial line guide's worked example reading three registers from 107
// of unit 17 (LRC 0x7E), a write of one register, and an exception reply.
func TestKnownVectors(t *testing.T) {
	for _, c := range []struct {
		adu   []byte
		frame string
		lrc   byte
	}{
		{[]byte{0x01, 0x03, 0x00, 0x00, 0x00, 0x01}, ":010300000001FB\r\n", 0xFB},
		{[]byte{0x11, 0x03, 0x00, 0x6B, 0x00, 0x03}, ":1103006B00037E\r\n", 0x7E},
		{[]byte{0x01, 0x06, 0x00, 0x01, 0x00, 0x03}, ":010600010003F5\r\n", 0xF5},
		{[]byte{0x0A, 0x83, 0x02}, ":0A830271\r\n", 0x71},
		{[]byte{0x00}, ":0000\r\n", 0x00},
	} {
		if got := LRC(c.adu); got != c.lrc {
			t.Errorf("LRC(% x) = %#x, want %#x", c.adu, got, c.lrc)
		}
		if got := string(Encode(c.adu)); got != c.frame {
			t.Errorf("Encode(% x) = %q, want %q", c.adu, got, c.frame)
		}
		if len(c.adu) < 2 {
			continue
		}
		got, err := Decode([]byte(c.frame))
		if err != nil || string(got) != string(c.adu) {
			t.Errorf("Decode(%q) = % x, %v", c.frame, got, err)
		}
	}
}

func TestDecodeErrors(t *testing.T) {
	if _, err := Decode([]byte(":010300000001FC\r\n")); !errors.Is(err, ErrCheck) {
		t.Errorf("bad LRC: %v", err)
	}
	for _, bad := range []string{":01\r\n", ":0103000000ZZFB\r\n", ":010300000001F\r\n"} {
		if _, err := Decode([]byte(bad)); err == nil {
			t.Errorf("%q decoded", bad)
		}
	}
	// Lowercase hexadecimal is taken.
	if _, err := Decode([]byte(":010300000001fb\r\n")); err != nil {
		t.Errorf("lowercase: %v", err)
	}
}

func TestReadFrame(t *testing.T) {
	r := bufio.NewReader(strings.NewReader("noise:0103:010300000001FB\r\n:0A830271\r\n:" + strings.Repeat("0", 600)))
	f, err := ReadFrame(r)
	if err != nil || string(f) != ":010300000001FB\r\n" {
		t.Errorf("first: %q %v", f, err)
	}
	f, err = ReadFrame(r)
	if err != nil || string(f) != ":0A830271\r\n" {
		t.Errorf("second: %q %v", f, err)
	}
	if _, err := ReadFrame(r); err == nil {
		t.Error("an endless frame was read")
	}
}

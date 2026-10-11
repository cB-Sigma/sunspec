// Copyright 2026 Abhinav Gautam
// SPDX-License-Identifier: Apache-2.0

// Package ascii frames Modbus ASCII (Modbus over Serial Line v1.02,
// section 2.5.2; our own words): a frame starts with a colon, carries the
// unit address, the PDU and a longitudinal redundancy check as two
// uppercase hexadecimal characters per byte, and ends with carriage
// return and line feed. The check is the two's complement of the 8-bit
// sum of the unit and PDU bytes. Shared by the client in mbtcp and the
// server in device.
package ascii

import (
	"bufio"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
)

// MaxFrame bounds a frame read, colon to line feed (513 characters in
// the standard: 1 + 2*(1+253+1) + 2).
const MaxFrame = 513

// LRC is the check of b: the two's complement of the sum of its bytes.
func LRC(b []byte) byte {
	var s byte
	for _, x := range b {
		s += x
	}
	return -s
}

// Encode frames adu (the unit address followed by the PDU).
func Encode(adu []byte) []byte {
	out := make([]byte, 0, 1+2*(len(adu)+1)+2)
	out = append(out, ':')
	out = append(out, strings.ToUpper(hex.EncodeToString(append(append([]byte(nil), adu...), LRC(adu))))...)
	return append(out, '\r', '\n')
}

// ErrCheck is returned for a frame whose check does not match.
var ErrCheck = errors.New("modbus ascii: LRC mismatch")

// Decode checks a frame (with or without the colon and the line end) and
// returns the unit address and the PDU.
func Decode(frame []byte) ([]byte, error) {
	s := strings.TrimRight(string(frame), "\r\n")
	s = strings.TrimPrefix(s, ":")
	if len(s) < 6 || len(s)%2 != 0 {
		return nil, fmt.Errorf("modbus ascii: frame of %d characters", len(s))
	}
	b, err := hex.DecodeString(s)
	if err != nil {
		return nil, fmt.Errorf("modbus ascii: %w", err)
	}
	adu, lrc := b[:len(b)-1], b[len(b)-1]
	if LRC(adu) != lrc {
		return nil, ErrCheck
	}
	return adu, nil
}

// ReadFrame reads one frame: characters before the colon are skipped (a
// colon always starts a new frame), and the frame ends at the line feed.
func ReadFrame(r *bufio.Reader) ([]byte, error) {
	for {
		c, err := r.ReadByte()
		if err != nil {
			return nil, err
		}
		if c == ':' {
			break
		}
	}
	buf := []byte{':'}
	for {
		c, err := r.ReadByte()
		if err != nil {
			return nil, err
		}
		if c == ':' {
			buf = buf[:1] // a new frame starts; drop the partial one
			continue
		}
		buf = append(buf, c)
		if c == '\n' {
			return buf, nil
		}
		if len(buf) > MaxFrame {
			return nil, fmt.Errorf("modbus ascii: frame longer than %d characters", MaxFrame)
		}
	}
}

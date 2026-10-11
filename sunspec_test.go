// Copyright 2026 Abhinav Gautam
// SPDX-License-Identifier: Apache-2.0

package sunspec_test

import (
	"fmt"
	"slices"
	"testing"

	"github.com/cB-Sigma/sunspec"
)

// filled returns n registers of v.
func filled(n int, v uint16) []uint16 {
	out := make([]uint16, n)
	for i := range out {
		out[i] = v
	}
	return out
}

// image builds a register map model by model, as a device lays it out:
// the marker at base, then ID, L and data for each model.
type image struct {
	regs map[uint16]uint16
	next uint16
}

func newImage(base uint16) *image {
	im := &image{regs: map[uint16]uint16{base: 0x5375, base + 1: 0x6E53}, next: base + 2}
	return im
}

// add appends a model and returns the address of its first data register.
func (im *image) add(id uint16, data []uint16) uint16 {
	im.regs[im.next], im.regs[im.next+1] = id, uint16(len(data))
	start := im.next + 2
	for i, w := range data {
		im.regs[start+uint16(i)] = w
	}
	im.next = start + uint16(len(data))
	return start
}

func (im *image) end() { im.regs[im.next], im.regs[im.next+1] = 0xFFFF, 0 }

// reader serves the image the way a Modbus device does: a read of an
// address it does not hold is refused (exception 2).
func (im *image) reader() sunspec.Reader {
	return refusingReader{&memReader{regs: im.regs, refuse: true}}
}

// newInverter lays out a three-phase inverter register by register from
// the published model definitions (offsets counted from the first
// register after ID and L): common (1), inverter (103), nameplate (120),
// MPPT with two inputs (160), the DER AC controls (704), a vendor model
// (64900) and the end model. Not-implemented values (0xFFFF, 0x8000) are
// where the device has nothing.
func newInverter(base uint16) *image {
	im := newImage(base)
	common := append(append(append(append(append(str("Fronius", 16), str("Symo 10.0-3-M", 16)...),
		str("", 8)...), str("1.28.7-1", 8)...), str("34119102", 16)...), 1, 0x8000)
	im.add(1, common)

	inv := filled(50, 0xFFFF)
	inv[0], inv[1], inv[2], inv[3], inv[4] = 125, 42, 41, 42, 0xFFFF // A 12.5, A_SF -1
	inv[8], inv[9], inv[11] = 2301, 2299, 0xFFFF                     // PhVphA/B, V_SF -1; PhVphC not implemented
	inv[12], inv[13] = 800, 1                                        // W 800 × 10^1
	inv[14], inv[15] = 5002, 0xFFFE                                  // Hz 50.02 (Hz_SF -2)
	inv[16], inv[17] = 0x8000, 0                                     // VA not implemented
	inv[22], inv[23], inv[24] = 0x0001, 0x86A0, 0                    // WH 100000 Wh
	inv[29], inv[30] = 8200, 0                                       // DCW
	inv[31], inv[32], inv[35] = 452, 0x8000, 0xFFFF                  // TmpCab 45.2, TmpSnk not implemented
	inv[36] = 4                                                      // St MPPT
	inv[38], inv[39] = 0, 0b10100                                    // Evt1 AC_DISCONNECT, GRID_DISCONNECT
	inv[40], inv[41] = 0, 0
	im.add(103, inv)

	plate := filled(26, 0xFFFF)
	plate[0], plate[1], plate[2] = 4, 10000, 0 // PV, WRtg 10000 W
	im.add(120, plate)

	mppt := filled(8+2*20, 0xFFFF)
	mppt[0], mppt[1], mppt[2], mppt[3] = 0xFFFE, 0xFFFF, 0, 0 // DCA_SF -2, DCV_SF -1
	mppt[4], mppt[5], mppt[6] = 0, 0, 2                       // Evt, N = 2
	for k, m := range [][3]uint16{{512, 6105, 3120}, {498, 5987, 2980}} {
		o := 8 + 20*k
		mppt[o] = uint16(k + 1)
		copy(mppt[o+1:], str(fmt.Sprintf("PV%d", k+1), 8))
		mppt[o+9], mppt[o+10], mppt[o+11] = m[0], m[1], m[2]
		mppt[o+12], mppt[o+13] = 0, 0 // DCWH acc32 not accumulated
	}
	im.add(160, mppt)

	der := filled(65, 0xFFFF)
	der[0], der[12], der[13] = 0, 0, 1000 // PFWInjEna off, WMaxLimPctEna off, WMaxLimPct 100.0
	der[51], der[52] = 0xFFFD, 0xFFFF     // PF_SF -3, WMaxLimPct_SF -1
	der[57], der[58] = 1000, 0            // PFWInj.PF 1.000 over-excited
	im.add(704, der)
	im.add(64900, []uint16{1, 2, 3})
	im.end()
	return im
}

// The marker is found at each base address, the chain is walked to the
// end model, scale factors apply, not-implemented points are left out,
// enums and bitfields are named and an unknown model is kept. (The same
// image over Modbus TCP against the simulator is in sunspec-sim.)
//
// Covers: SIG-IMP-053
func TestSunSpecMapDecode(t *testing.T) {
	for _, base := range []uint16{40000, 50000, 0} {
		r := newInverter(base).reader()
		m, err := sunspec.Find(r)
		if err != nil {
			t.Fatalf("base %d: %v", base, err)
		}
		if m.Base != base || !slices.Equal(ids(m), []int{1, 103, 120, 160, 704, 64900}) {
			t.Fatalf("base %d: map %d %v", base, m.Base, ids(m))
		}
		if unk := m.Model(64900); unk.Def != nil || unk.L != 3 || unk.Name != "model64900" {
			t.Errorf("vendor model %+v", unk)
		}
		id, err := sunspec.ReadIdentity(r, m)
		if err != nil || id.Manufacturer != "Fronius" || id.Model != "Symo 10.0-3-M" || id.Serial != "34119102" || id.Version != "1.28.7-1" || id.DeviceAddress != 1 || id.Options != "" {
			t.Errorf("identity %+v %v", id, err)
		}
	}
	r := newInverter(40000).reader()
	m, _ := sunspec.Find(r)
	got := map[string]any{}
	for _, inst := range []int{103, 160} {
		in := m.Model(inst)
		regs, err := sunspec.ReadModel(r, *in)
		if err != nil {
			t.Fatal(err)
		}
		for _, v := range sunspec.Decode(*in, regs) {
			got[in.Name+"."+v.Name] = v.V
		}
	}
	want := map[string]any{
		"inverter.A": 12.5, "inverter.AphB": 4.1, "inverter.PhVphA": 230.1, "inverter.W": 8000.0, "inverter.Hz": 50.02,
		"inverter.WH": 100000.0, "inverter.DCW": 8200.0, "inverter.TmpCab": 45.2, "inverter.St": "MPPT",
		"mppt.1.DCA": 5.12, "mppt.1.DCV": 610.5, "mppt.2.DCW": 2980.0, "mppt.2.IDStr": "PV2", "mppt.2.ID": 2.0,
	}
	for k, w := range want {
		if fmt.Sprint(got[k]) != fmt.Sprint(w) {
			t.Errorf("%s = %v, want %v", k, got[k], w)
		}
	}
	if fmt.Sprint(got["inverter.Evt1"]) != "[AC_DISCONNECT GRID_DISCONNECT]" {
		t.Errorf("Evt1 = %v", got["inverter.Evt1"])
	}
	for _, k := range []string{"inverter.PhVphC", "inverter.VA", "inverter.TmpSnk", "inverter.A_SF", "inverter.PPVphAB", "mppt.1.DCWH"} {
		if _, ok := got[k]; ok {
			t.Errorf("%s is not implemented but was decoded: %v", k, got[k])
		}
	}
	// A device without a map says so.
	plain := newImage(1000) // marker elsewhere
	plain.end()
	if _, err := sunspec.Find(plain.reader()); err != sunspec.ErrNotSunSpec {
		t.Errorf("no map: %v", err)
	}
}

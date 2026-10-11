// Copyright 2026 Abhinav Gautam
// SPDX-License-Identifier: Apache-2.0

package sunspec_test

import (
	"errors"
	"fmt"
	"log"

	"github.com/cB-Sigma/sunspec"
)

// registers stands in for a device's holding registers. A real
// application reads them over Modbus: with mbtcp.Dial, or through its own
// client implementing sunspec.Reader. (The examples of sunspec-sim run the
// same code against a simulated inverter over Modbus TCP.)
type registers map[uint16]uint16

func (r registers) ReadHolding(addr, count uint16) ([]uint16, error) {
	out := make([]uint16, count)
	for i := range out {
		v, ok := r[addr+uint16(i)]
		if !ok {
			return nil, errors.New("illegal data address")
		}
		out[i] = v
	}
	return out, nil
}

// put lays a model (ID, L, data) at addr and returns the next address.
func (r registers) put(addr, id uint16, data []uint16) uint16 {
	r[addr], r[addr+1] = id, uint16(len(data))
	for i, w := range data {
		r[addr+2+uint16(i)] = w
	}
	return addr + 2 + uint16(len(data))
}

// text encodes s into n registers, two characters each.
func text(s string, n int) []uint16 {
	b := make([]byte, 2*n)
	copy(b, s)
	out := make([]uint16, n)
	for i := range out {
		out[i] = uint16(b[2*i])<<8 | uint16(b[2*i+1])
	}
	return out
}

// exampleInverter is a small inverter: the common model (1), the
// three-phase inverter model (103) and the end model.
func exampleInverter() registers {
	r := registers{40000: 0x5375, 40001: 0x6E53} // "SunS"
	common := append(append(append(append(append(text("Example Solar", 16), text("ES-10K-3P", 16)...),
		text("", 8)...), text("1.0.0", 8)...), text("DEMO000001", 16)...), 1, 0x8000)
	a := r.put(40002, 1, common)
	inv := make([]uint16, 50)
	for i := range inv {
		inv[i] = 0xFFFF // not implemented
	}
	inv[8], inv[11] = 2301, 0xFFFF  // PhVphA 230.1 V (V_SF -1)
	inv[12], inv[13] = 980, 1       // W 9800 W (W_SF 1)
	inv[14], inv[15] = 5001, 0xFFFE // Hz 50.01 (Hz_SF -2)
	inv[36] = 4                     // St MPPT
	a = r.put(a, 103, inv)
	r[a], r[a+1] = 0xFFFF, 0 // end
	return r
}

// Find a device's SunSpec map, read who it is and decode its inverter
// model.
func ExampleFind() {
	r := exampleInverter()
	m, err := sunspec.Find(r)
	if err != nil {
		log.Fatal(err)
	}
	for _, inst := range m.Models {
		fmt.Println(inst.ID, inst.Name, inst.L)
	}
	id, _ := sunspec.ReadIdentity(r, m)
	fmt.Println(id.Manufacturer, id.Model, id.Serial)

	inv := m.Model(103)
	regs, err := sunspec.ReadModel(r, *inv)
	if err != nil {
		log.Fatal(err)
	}
	for _, v := range sunspec.Decode(*inv, regs) {
		switch v.Name {
		case "W", "Hz", "PhVphA", "St":
			fmt.Printf("%s.%s = %v %s\n", inv.Name, v.Name, v.V, v.Unit)
		}
	}
	// Output:
	// 1 common 66
	// 103 inverter 50
	// Example Solar ES-10K-3P DEMO000001
	// inverter.PhVphA = 230.1 V
	// inverter.W = 9800 W
	// inverter.Hz = 50.01 Hz
	// inverter.St = MPPT
}

// Plan an output limit of 60 % on a device with model 704 and encode the
// writes; after writing them, ReadBack tells what the device kept.
func ExamplePlanControl() {
	r := registers{40000: 0x5375, 40001: 0x6E53}
	der := make([]uint16, 65)
	der[51], der[52] = 0xFFFD, 0xFFFF // PF_SF -3, WMaxLimPct_SF -1
	a := r.put(40002, 704, der)
	r[a], r[a+1] = 0xFFFF, 0
	m, err := sunspec.Find(r)
	if err != nil {
		log.Fatal(err)
	}

	pct := 60.0
	plan, err := sunspec.PlanControl(m, sunspec.Control{Action: sunspec.ActionLimitPower, Percent: &pct},
		func(inst sunspec.Instance) ([]uint16, error) { return sunspec.ReadModel(r, inst) })
	if err != nil {
		log.Fatal(err)
	}
	for _, w := range plan.Writes {
		addr, words, err := w.Encode()
		if err != nil {
			log.Fatal(err)
		}
		fmt.Printf("model %d: %s = %d, write %v at %d\n", w.Model.ID, w.Point, w.Raw, words, addr)
	}
	// Output:
	// model 704: WMaxLimPct = 600, write [600] at 40017
	// model 704: WMaxLimPctEna = 1, write [1] at 40016
}

// Read the stored curves of a volt-var model (705), whose points sit in a
// group nested in each curve, then plan a new curve and adopt it.
func ExamplePlanCurve() {
	r := registers{40000: 0x5375, 40001: 0x6E53}
	vv := make([]uint16, 13+2*(10+2*2)) // two curves of two points
	for i := range vv {
		vv[i] = 0xFFFF
	}
	vv[3], vv[4], vv[10], vv[11], vv[12] = 2, 2, 0xFFFF, 0, 0 // NPt, NCrv, V_SF -1, DeptRef_SF, RspTms_SF
	for k := 0; k < 2; k++ {
		c := 13 + 14*k
		vv[c], vv[c+9] = 2, uint16(1-k)                                    // ActPt, ReadOnly (the first curve)
		vv[c+10], vv[c+11], vv[c+12], vv[c+13] = 950, 30, 1050, 0x10000-30 // (95 %, 30 %), (105 %, -30 %)
	}
	a := r.put(40002, 705, vv)
	r[a], r[a+1] = 0xFFFF, 0
	m, err := sunspec.Find(r)
	if err != nil {
		log.Fatal(err)
	}
	inst := m.Model(705)
	regs, _ := sunspec.ReadModel(r, *inst)
	for _, v := range sunspec.Decode(*inst, regs) {
		if v.Name == "1.Pt.2.V" || v.Name == "1.Pt.2.Var" {
			fmt.Printf("%s (%s) = %v %s\n", v.Name, v.Path, v.V, v.Unit)
		}
	}
	on := true
	plan, err := sunspec.PlanCurve(m, sunspec.Curve{Model: 705, Points: [][2]float64{{92, 25}, {108, -25}}, Enable: &on},
		func(inst sunspec.Instance) ([]uint16, error) { return sunspec.ReadModel(r, inst) })
	if err != nil {
		log.Fatal(err)
	}
	for _, w := range plan.Writes {
		addr, words, _ := w.Encode()
		fmt.Printf("%s: %v at %d\n", w.Point, words, addr)
	}
	// Output:
	// 1.Pt.2.V (Crv.1.Pt.2.V) = 105 %
	// 1.Pt.2.Var (Crv.1.Pt.2.Var) = -30 %
	// Crv.2.Pt.1.V: [920] at 40041
	// Crv.2.Pt.1.Var: [25] at 40042
	// Crv.2.Pt.2.V: [1080] at 40043
	// Crv.2.Pt.2.Var: [65511] at 40044
	// Crv.2.ActPt: [2] at 40031
	// AdptCrvReq: [2] at 40005
	// Ena: [1] at 40004
}

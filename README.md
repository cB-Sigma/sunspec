# sunspec

Reads and writes SunSpec devices over Modbus in pure Go, and plans their DER controls.

[![Go Reference](https://pkg.go.dev/badge/github.com/cB-Sigma/sunspec.svg)](https://pkg.go.dev/github.com/cB-Sigma/sunspec) [![test](https://github.com/cB-Sigma/sunspec/actions/workflows/test.yml/badge.svg)](https://github.com/cB-Sigma/sunspec/actions/workflows/test.yml) [![Licence: Apache-2.0](https://img.shields.io/badge/licence-Apache--2.0-blue.svg)](LICENSE) [![Go](https://img.shields.io/badge/go-1.22-00ADD8.svg)](go.mod)

Solar inverters, meters, batteries and other distributed energy
resources publish their data as a chain of SunSpec information models in
Modbus holding registers. This module finds the map, walks the model
chain, decodes every implemented point with its scale factor (groups
nested to any depth included, such as the curves of the IEEE 1547-2018
models), writes any writable point in engineering units, and plans,
encodes and reads back the DER controls and curves. The tables of all 112
models of the SunSpec Alliance's definitions are embedded; more can be
loaded at run time. It needs one method from a Modbus stack and comes
with an optional reader for Modbus TCP, TLS, UDP and RTU, and a command
line tool. No cgo. Written for SigmaM2M, a oneM2M platform, where it
serves the Modbus device integration.

SunSpec is a trademark of the SunSpec Alliance. This module is not made,
endorsed, certified or supported by the SunSpec Alliance.

## Features

| Feature | Status |
|---|---|
| Map discovery at 40000, 50000 and 0 (zero-based protocol addresses), in that order, then at the addresses next to those the device answered at (40001, 39999, 50001, 49999, 1: maps counted from one); `FindAt` for other addresses; a device that answers without a map is told apart from one that does not answer (`ErrNotSunSpec`, `Refuser`) | yes |
| Several devices behind one connection: `Scan` over unit addresses, with identity | yes |
| Model chain to the end model (0xFFFF), or to the first refused header; at most 256 models | yes |
| Models described | all 112 of github.com/sunspec/models at a pinned commit: 1 to 19, 101 to 145, 160, 201 to 220, 302 to 308 (weather, location), 401 to 404 (string combiners), 501, 502, 601, 701 to 715 (IEEE 1547-2018 DER), 801 to 809 (storage, batteries), 63001, 63002 and the vendor models 64001 to 64415 |
| Definitions at run time | `ParseDefinition` reads a model file in the SunSpec JSON layout, `Register` adds it (a vendor model, a newer SunSpec model) |
| Unknown models | kept as ID, address and length; not decoded |
| Types | int16/32/64, uint16/32/64 (all 64 bits), acc16/32/64, count, enum16/32 (symbol names), bitfield16/32/64 (names of the bits set), sunssf, string, ipaddr, ipv6addr, eui48, float32, float64; pads skipped |
| Not-implemented values | left out, per type; also the float numbers pysunspec2 writes for "not implemented" |
| Scale factors | looked up from the point's group outwards to the model's top level; a value whose scale factor is not implemented or outside -10 to 10 is left out (writes are refused); a fixed power in the definition applied |
| Groups | once, a fixed number of times, by count point, or filling the model's length; nested to any depth (705 to 712 curves and trip curve sets, 714 ports, 803/804 strings and modules, 64410/64411 profiles); `Layout` places every point, `Locate` finds one by name or path |
| Model 1 quirks | 65 registers without the pad accepted, as pysunspec2 does |
| Writes | any writable point of any type and size (integers to 64 bits, floats, strings, addresses, enums by symbol, bitfields by bit names), in repeating and nested groups, scaled from engineering units (`PlanWrite`); `Kept` and `ReadBack` check what the device holds |
| DER controls | connect/disconnect (123 `Conn`, else 715 `OpCtl`, else 703 `ES`); output limit (704, else 123); power factor (704 with excitation, else 123 signed); read-back |
| DER curves | `PlanCurve`: points, active point count, other curve settings, adopt request and enable for 705 (volt-var), 706 (volt-watt), 707 to 710 (trip, MustTrip/MayTrip/MomCess), 711 (frequency droop control sets), 712 (watt-var); the read-only first curve refused |
| Transports (`mbtcp`) | Modbus TCP, Modbus TCP over TLS with client certificates (Modbus/TCP Security), Modbus over UDP, Modbus RTU on a serial line, RTU framing over TCP or UDP (gateways); several units over one connection (`ForUnit`) |
| Command line (`cmd/sunspec`) | `scan`, `read` (text or JSON, raw registers), `write` (engineering units, read back), `models` (the tables); every transport; `-defs` loads definitions |

Not implemented:

- Modbus ASCII. Any `Reader` works, so an application with an ASCII
  stack can bring it.
- Interpreting the security models 3 to 9 (signed datasets): their points
  are decoded, their signatures not checked.

## Choosing a Go SunSpec library

Compared from each library's source at the version named, read on
2026-10-11 (only this module and pysunspec2 were run). Check the projects
themselves before relying on a row.

| | this module | andig/gosunspec 6d585e1 (2026-07-05) | otfabric/go-sunspec v0.4.2 | TRICERA-energy/sunspec v0.0.13 | pysunspec2 1.3.6 (Python) |
|---|---|---|---|---|---|
| Licence | Apache-2.0 | BSD-3-Clause | MIT | Apache-2.0 | Apache-2.0 |
| Models built in | 112 (every model of the SunSpec definitions at commit 90b4a33), checked point by point against pysunspec2's copy | 113 generated packages, 701-715 among them | 112 compiled, 701-715 among them | none | 112 JSON definitions |
| Definitions loaded at run time | yes (SunSpec JSON layout) | no | no | yes (the caller supplies JSON) | yes (JSON, SMDX) |
| Marker search | 40000, 50000, 0, then their neighbours | 40000, 50000, 0 | 0, 40000, 50000 and their neighbours | 0, 40000, 50000; takes the first readable address without checking for "SunS" | 40000, 0, 50000 |
| Scale factors | applied when decoding; out-of-range factors refused | on request (`ScaledValue`) | applied when decoding | on request | on request (`cvalue`) |
| Not-implemented values | every type | partly (accumulators checked against all ones, floats only for one NaN pattern) | every type | partly (a float NaN is never recognised) | every type (writes a float's as the number 2143289344) |
| Repeating groups | nested to any depth, with count points; every model's layout checked against pysunspec2 | one repeating block; models with several blocks (7xx curves) appear mis-laid out | nested, with count points | nested, with count points | nested |
| Writes | every type, nested groups included, from engineering units | yes | no (read only) | yes (function 16, unit 1 only) | yes |
| DER control helpers (limit, power factor, connect) with read-back | yes | no | no | no | no |
| DER curve helpers (volt-var, trip curves and the like) | yes | no | no | no | no |
| Modbus transport | any `Reader`; `mbtcp` on simonvetter/modbus (TCP, TLS, UDP, RTU, RTU over TCP/UDP) | grid-x/modbus (TCP, RTU, ASCII) | otfabric/go-modbus (TCP, RTU, ASCII, TLS, UDP) | GoAethereal/modbus (TCP only) | its own (TCP, RTU, TLS) |
| Serves a simulated device | yes, in sunspec-sim (every model, TCP, TLS, RTU, faults) | in-memory test driver only | read-only test fixture | yes (TCP, functions 3 and 16) | no |
| Tested against another implementation | pysunspec2, both directions, every model | none stated | none stated | no test files | (the reference) |
| Go version, cgo | 1.22, none | 1.13, none | 1.23, none | 1.16, none | Python 3 |
| Activity | v0.2.0, 2026-10-11 | untagged; a fork of crabmusket/gosunspec; required by evcc | releases since 2026-03 | last release 2023-01 | PyPI release 2026-06-01 |

Pick this module for reading and controlling SunSpec devices from Go: it
describes every SunSpec model, nested curve groups included, writes any
point, plans the DER controls and curves, and needs only one method from
your own Modbus stack. Pick otfabric/go-sunspec or andig/gosunspec when
you need Modbus ASCII out of the box, or andig/gosunspec in the evcc
ecosystem. Pick pysunspec2 when Python is acceptable: it is the SunSpec
Alliance's own library and the reference these modules are checked
against. For a simulated device in Go tests, use
[sunspec-sim](https://github.com/cB-Sigma/sunspec-sim).

## Install

```
go get github.com/cB-Sigma/sunspec@latest
go install github.com/cB-Sigma/sunspec/cmd/sunspec@latest   # the command
```

Requires Go 1.22 or later.

## Quick start

```go
import (
	"github.com/cB-Sigma/sunspec"
	"github.com/cB-Sigma/sunspec/mbtcp"
)

r, err := mbtcp.Dial("tcp://192.168.1.40:502", 1, 2*time.Second) // many SMA inverters: unit 126
m, err := sunspec.Find(r) // ErrNotSunSpec: a Modbus device without a map
id, _ := sunspec.ReadIdentity(r, m)
inv := m.Model(103)
regs, _ := sunspec.ReadModel(r, *inv)
for _, v := range sunspec.Decode(*inv, regs) {
	fmt.Println(inv.Name+"."+v.Name, v.V, v.Unit) // inverter.W 9800 W
}

pct := 60.0
plan, err := sunspec.PlanControl(m, sunspec.Control{Action: sunspec.ActionLimitPower, Percent: &pct},
	func(i sunspec.Instance) ([]uint16, error) { return sunspec.ReadModel(r, i) })
for _, w := range plan.Writes {
	addr, words, _ := w.Encode()
	_ = r.WriteHolding(addr, words)
}
```

After the writes, read the model again and check `w.Kept(regs)`: a
device that kept the value has taken the setpoint (not necessarily
reached it).

Any writable point, in engineering units, nested groups included:

```go
vv := m.Model(705)
regs, _ := sunspec.ReadModel(r, *vv)
w, err := sunspec.PlanWrite(*vv, regs, "Crv.2.Pt.1.V", 97.5) // V_SF applied
addr, words, err := w.Encode()
```

A volt-var curve, adopted and enabled (`ExamplePlanCurve`):

```go
on := true
plan, err := sunspec.PlanCurve(m, sunspec.Curve{Model: 705,
	Points: [][2]float64{{92, 25}, {98, 0}, {102, 0}, {108, -25}}, Enable: &on}, read)
```

Other transports: `mbtcp.Open(mbtcp.Config{URL: "rtu:///dev/ttyUSB0",
Unit: 1, Speed: 9600, Parity: "E"})`, `"tcp+tls://host:802"` with
`TLSClientCert` and `TLSRootCAs`, `"rtuovertcp://gateway:4001"`. With
your own Modbus stack, implement `ReadHolding(addr, count)`, and
`Refused(err) bool` returning true for Modbus exception replies so that
`Find` reports `ErrNotSunSpec` for a device that answers without a map
rather than the read error.

The command:

```
sunspec scan  -url tcp://192.168.1.40:502 -units 1-3,126
sunspec read  -url rtu:///dev/ttyUSB0 -baud 9600 -parity E -unit 2 -model 705
sunspec write -url tcp://192.168.1.40:502 -model 704 -point WMaxLimPct -value 60
sunspec models -model 707
```

## Documentation

API reference: https://pkg.go.dev/github.com/cB-Sigma/sunspec

| Package | What it does |
|---|---|
| `sunspec` (module root) | `Find`, `FindAt`, `Walk`, `Scan`, `Map`, `Instance`, `ReadModel` (blocks of 120 registers), `Decode`, `Layout`, `Locate`, `Raw`, `ReadIdentity` (model 1), `Lookup`, `Known`, `ParseDefinition`, `Register`, `ShortName`, `Dynamic`, `Unit`; writes: `PlanWrite`, `Write.Encode`, `Write.Kept`, `Write.ReadBack`, `EncodePoint`; controls: `Control`, `PlanControl`, `Plan`, `IsControl`; curves: `Curve`, `PlanCurve`. The `Reader` interface (one method, `ReadHolding`) is all it needs from a Modbus stack; the optional `Refuser` tells a Modbus exception from silence |
| `mbtcp` | A `Reader` over github.com/simonvetter/modbus: `Dial`, `Open` (TCP, TLS, UDP, RTU, RTU over TCP/UDP), `ForUnit`, `WriteHolding`, `Refused`; `IsException` |
| `cmd/sunspec` | The command line tool |
| `internal/modelgen` | Derives models.json from a checkout of github.com/sunspec/models |
| `interop` (nested module, tests only) | this module against pysunspec2 in Docker |

## Testing

```
go test -race ./...
```

The tests use registers in memory and, in `mbtcp` and `cmd/sunspec`,
small Modbus servers of their own (Modbus TCP, TLS with generated
certificates, RTU framing over TCP). Tests carry `// Covers: SIG-...`
comments: requirement IDs of the SigmaM2M project's compliance report,
with no meaning outside it. The same map, control and example tests over
Modbus against the simulator are in
[sunspec-sim](https://github.com/cB-Sigma/sunspec-sim), so this module
does not depend on its simulator.

`models.json` is regenerated with
`go run ./internal/modelgen -src <sunspec/models checkout>/json -commit <sha> -out models.json`;
at commit 90b4a331dcca1d6eac69c1bead952fddcc5852e0 the output is the
file as committed, byte for byte.

`interop/` is a nested module with its own go.mod. Its tests run
pysunspec2 in Docker and are skipped unless `SIGMA_SUNSPEC_INTEROP=1` is
set; they need network access for the image and the package:

```
cd interop && SIGMA_SUNSPEC_INTEROP=1 go test -v ./...
```

### Interop tested against

- pysunspec2 1.3.6 (the SunSpec Alliance's Python library, Apache-2.0,
  from PyPI) in python:3.12.10-slim on Docker Desktop for macOS (arm64),
  2026-10-11 (`interop`):
  - Model tables: all 112 models and 3578 points compared with
    pysunspec2's own copy of the definitions (offset, type, size, scale
    factor, access, mandatory and static flags, symbols, labels; group
    places, lengths, counts and nesting): equal, apart from our
    correction of model 111's state names.
  - Layout of every model: pysunspec2 encoded made-up values for all 112
    models (two occurrences of every repeating group, nested ones
    included) and placed every point itself; this module, reading
    pysunspec2's registers, placed the same 4660 points at the same
    offsets under the same paths (196 in groups nested two or three
    deep) and decoded the same 4280 values.
  - Devices whose register maps pysunspec2 built, served over Modbus TCP
    and read with `mbtcp`: an inverter with models 1, 103, 120, 160, 704,
    715 and a vendor model at 40000; models 1, 101, 111, 201, 701, 702,
    713 and 123 at 50000 on unit 126; models 1, 113, 203 and 122 at
    address 0; values with their scale factors, not-implemented points
    left out. Controls written: output limit, power factor with
    excitation and disconnect on 704 and 715, and the same on 123.
  - Curves written with `PlanCurve` to a pysunspec2-built device: a
    volt-var curve on 705 (13 writes), a must-trip curve of a 707 curve
    set (7 writes) and a watt-var curve on 712 (6 writes); every write
    landed on a point pysunspec2's definitions mark read-write and was
    kept; a write to a read-only point was refused.
  - Two problems found and fixed in v0.1.0 (CHANGELOG.md): pysunspec2
    writes a float's "not implemented" as the number 2143289344 rather
    than a NaN, and the source definitions name model 111's states with a
    stray "gg" prefix.
- pysunspec2 1.3.6 reading the sunspec-sim simulator, every model
  included: see that repository.
- Modbus TLS and RTU: tested against simonvetter/modbus's TLS server and
  against RTU servers of our own (in these tests and in sunspec-sim); not
  yet against another vendor's device or a serial line.
- Not yet: a real inverter, a commercial SunSpec test tool.

## Related repositories

- [sunspec-sim](https://github.com/cB-Sigma/sunspec-sim): a simulated
  SunSpec device (every model, Modbus TCP, TLS and RTU, faults), as a Go
  package and a command.

## Status

v0.x: the API may change before v1.

## Contributing

See [CONTRIBUTING.md](CONTRIBUTING.md).

## Security

See [SECURITY.md](SECURITY.md) to report a vulnerability privately.

## Licence

Apache-2.0 (see [LICENSE](LICENSE) and [NOTICE](NOTICE)). Copyright 2026
Abhinav Gautam. The model tables are derived from the SunSpec Alliance's
model definitions (github.com/sunspec/models, Apache-2.0); NOTICE names
the version and the changes.

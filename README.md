# sunspec

Reads SunSpec devices over Modbus in pure Go and plans their DER controls.

[![Go Reference](https://pkg.go.dev/badge/github.com/cB-Sigma/sunspec.svg)](https://pkg.go.dev/github.com/cB-Sigma/sunspec) [![test](https://github.com/cB-Sigma/sunspec/actions/workflows/test.yml/badge.svg)](https://github.com/cB-Sigma/sunspec/actions/workflows/test.yml) [![Licence: Apache-2.0](https://img.shields.io/badge/licence-Apache--2.0-blue.svg)](LICENSE) [![Go](https://img.shields.io/badge/go-1.22-00ADD8.svg)](go.mod)

Solar inverters, meters, batteries and other distributed energy
resources publish their data as a chain of SunSpec information models in
Modbus holding registers. This module finds the map, walks the model
chain, decodes every implemented point with its scale factor, and plans,
encodes and reads back the DER controls (connect, disconnect, output
limit, power factor). The model tables are embedded data derived from the
SunSpec Alliance's definitions. It needs one method from a Modbus stack
and comes with an optional Modbus TCP reader. No cgo. Written for
SigmaM2M, a oneM2M platform, where it serves the Modbus device
integration.

SunSpec is a trademark of the SunSpec Alliance. This module is not made,
endorsed, certified or supported by the SunSpec Alliance.

## Features

| Feature | Status |
|---|---|
| Map discovery at 40000, 50000 and 0 (zero-based protocol addresses), in that order; a device that answers without a map is told apart from one that does not answer (`ErrNotSunSpec`, `Refuser`) | yes |
| Model chain to the end model (0xFFFF), or to the first refused header; at most 64 models | yes |
| Models described | 1; 101 to 103, 111 to 113; 120 to 124, 126, 160; 201 to 204; 701 to 705, 713, 715 |
| Unknown models (vendor, newer SunSpec) | kept as ID, address and length; not decoded |
| Types | int16/32/64, uint16/32/64, acc16/32/64, count, enum16/32 (symbol names), bitfield16/32/64 (names of the bits set), sunssf, string, ipaddr, eui48, float32, float64; pads skipped |
| Not-implemented values | left out, per type, and a point whose scale factor is not implemented; also the float numbers pysunspec2 writes for "not implemented" |
| Repeating groups | by count, count point or the model's length; groups that occur once named `<group>.<point>` |
| Controls | connect/disconnect (123 `Conn`, else 715 `OpCtl`, else 703 `ES`); output limit (704, else 123); power factor (704 with excitation, else 123 signed); read-back of what the device kept |
| Writes | one- and two-register points |
| Modbus TCP reader (`mbtcp`) | yes, one unit per connection, with writes |

Not implemented:

- Model 705's nested curve points, writes of model 126's curves, and the
  other 7xx controls (volt-var, frequency-watt and the like).
- Modbus RTU and SunSpec Modbus over TLS in `mbtcp`; any `Reader` works,
  so an application with its own Modbus stack can bring them.
- Loading model definitions (SMDX or JSON) at run time; the tables are
  embedded.

## Choosing a Go SunSpec library

Compared from each library's source at the version named, read on
2026-10-11 (only this module and pysunspec2 were run). Check the projects
themselves before relying on a row.

| | this module | andig/gosunspec 6d585e1 (2026-07-05) | otfabric/go-sunspec v0.4.2 | TRICERA-energy/sunspec v0.0.13 | pysunspec2 1.3.6 (Python) |
|---|---|---|---|---|---|
| Licence | Apache-2.0 | BSD-3-Clause | MIT | Apache-2.0 | Apache-2.0 |
| Models built in | 25: 1, 101-103, 111-113, 120-124, 126, 160, 201-204, 701-705, 713, 715 | 113 generated packages, 701-715 among them | 112 compiled, 701-715 among them | none; the caller supplies JSON definitions | 112 JSON definitions loaded at run time |
| Marker search | 40000, 50000, 0 | 40000, 50000, 0 | 0, 40000, 50000 and their neighbours | 0, 40000, 50000; takes the first readable address without checking for "SunS" | 40000, 0, 50000 |
| Scale factors | applied when decoding | on request (`ScaledValue`) | applied when decoding | on request | on request (`cvalue`) |
| Not-implemented values | every type | partly (accumulators checked against all ones, floats only for one NaN pattern) | every type | partly (a float NaN is never recognised) | every type (writes a float's as the number 2143289344) |
| Repeating groups | one level (model 705's nested curves not described) | one repeating block; models with several blocks (7xx curves) appear mis-laid out | nested, with count points | nested, with count points | nested |
| Writes | yes | yes | no (read only) | yes (function 16, unit 1 only) | yes |
| DER control helpers (limit, power factor, connect) with read-back | yes | no | no | no | no |
| Modbus transport | any `Reader`; `mbtcp` on simonvetter/modbus (TCP) | grid-x/modbus (TCP, RTU, ASCII) | otfabric/go-modbus (TCP, RTU, ASCII, TLS, UDP) | GoAethereal/modbus (TCP only) | its own (TCP, RTU, TLS) |
| Serves a simulated device | yes, in sunspec-sim | in-memory test driver only | read-only test fixture | yes (TCP, functions 3 and 16) | no |
| Tested against another implementation | pysunspec2, both directions | none stated | none stated | no test files | (the reference) |
| Go version, cgo | 1.22, none | 1.13, none | 1.23, none | 1.16, none | Python 3 |
| Activity | v0.1.0, 2026-10-11 | untagged; a fork of crabmusket/gosunspec; required by evcc | releases since 2026-03 | last release 2023-01 | PyPI release 2026-06-01 |

Pick this module when you control DER: it plans the output limit, power
factor and connect/disconnect over whichever of models 704, 715, 703 and
123 a device has, encodes the writes and tells you what the device kept,
and it needs only one method from your own Modbus stack. Pick
otfabric/go-sunspec when you only read, want many more models than the 25
here, or need Modbus RTU or TLS out of the box. Pick andig/gosunspec if you
work in the evcc ecosystem or need writes to models this module does not
describe. Pick pysunspec2 when Python is acceptable: it is the SunSpec
Alliance's own library, with 112 models and TLS, and it is the
reference these modules are checked against. For a simulated device in
Go tests, use [sunspec-sim](https://github.com/cB-Sigma/sunspec-sim).

## Install

```
go get github.com/cB-Sigma/sunspec@latest
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

After the writes, read the model again and compare `w.ReadBack(regs)`
with `w.Raw`: a device that kept the value has taken the setpoint (not
necessarily reached it).

`ExampleFind` and `ExamplePlanControl` in example_test.go run the same
calls against registers held in memory. With your own Modbus stack,
implement `ReadHolding(addr, count)`, and `Refused(err) bool` returning
true for Modbus exception replies so that `Find` reports `ErrNotSunSpec`
for a device that answers without a map rather than the read error.

## Documentation

API reference: https://pkg.go.dev/github.com/cB-Sigma/sunspec

| Package | What it does |
|---|---|
| `sunspec` (module root) | `Find`, `Walk`, `Map`, `Instance`, `ReadModel` (blocks of 120 registers), `Decode`, `Raw`, `ReadIdentity` (model 1), `Lookup` and `Known` (the embedded models.json), `ShortName`, `Dynamic`, `Unit`; controls: `Control`, `PlanControl`, `Plan`, `Write.Encode`, `Write.ReadBack`, `IsControl`. The `Reader` interface (one method, `ReadHolding`) is all it needs from a Modbus stack; the optional `Refuser` tells a Modbus exception from silence |
| `mbtcp` | A `Reader` over github.com/simonvetter/modbus for one unit on Modbus TCP, with `WriteHolding` and `Refused`; `IsException` |
| `interop` (nested module, tests only) | this module against pysunspec2 in Docker |

## Testing

```
go test -race ./...
```

The tests use registers in memory and, in `mbtcp`, a small Modbus server
of their own. Tests carry `// Covers: SIG-...` comments: requirement IDs
of the SigmaM2M project's compliance report, with no meaning outside it.
The same map, control and example tests over Modbus TCP against the
simulator are in [sunspec-sim](https://github.com/cB-Sigma/sunspec-sim),
so this module does not depend on its simulator.

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
  - Model tables: all 25 models and 919 points of models.json compared
    with pysunspec2's own copy of the definitions (offset, type, size,
    scale factor, access, mandatory and static flags, symbols, group
    offsets and lengths): equal, apart from our correction of model
    111's state names.
  - Devices whose register maps pysunspec2 built from its definitions,
    served by a small Modbus TCP server in the container, read with
    `mbtcp` and this module: an inverter with models 1, 103, 120, 160
    (two inputs), 704, 715 and a vendor model at 40000; models 1, 101,
    111 (float32), 201, 701, 702, 713 and 123 at 50000 on unit 126; models
    1, 113, 203 and 122 at address 0 (after refusals at 40000 and 50000).
    Every model's length matched our table; 82 values agreed with their
    scale factors (negative integers, 32- and 64-bit values, floats,
    strings, enums, bitfields, repeating groups) and 31 points pysunspec2
    encoded as not implemented were left out. Controls written to it:
    output limit and its enable, power factor with excitation and
    disconnect on 704 and 715, and the same on 123; every write landed on
    a point pysunspec2's definitions mark read-write and read back as
    planned; a write to a read-only point was refused. A device with no
    map, and a unit the device does not serve, gave `ErrNotSunSpec`.
  - Two problems found and fixed (CHANGELOG.md): pysunspec2 writes a
    float's "not implemented" as the number 2143289344 rather than a NaN,
    and the source definitions name model 111's states with a stray "gg"
    prefix.
- pysunspec2 1.3.6 reading the sunspec-sim simulator: see that
  repository.
- Not yet: a real inverter, a commercial SunSpec test tool, Modbus RTU.

## Related repositories

- [sunspec-sim](https://github.com/cB-Sigma/sunspec-sim): a simulated
  SunSpec device over Modbus TCP, with a demo inverter, as a Go package
  and a command.

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

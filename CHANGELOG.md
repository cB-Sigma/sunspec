# Changelog

All notable changes to this module. Versions follow semantic versioning;
before v1 the API may change in minor versions.

## v0.3.0 (2026-10-11)

Backwards compatible with v0.2.0.

- The device side, package `device`: `Build`, `Example`,
  `NotImplemented`, `AllModels`, `Image` (`NewImage`, `Model`, `Add`,
  `End`, `Writable`, `Next`, `Get`, `Set`; an image is a
  `sunspec.Reader`), `Str` and `Filled`, moved here from sunspec-sim;
  `Device` serves an image with unit filtering, a write log and hooks
  (`SetKeep`, `SetBefore`, `SetOnWrite`, `Update`); `Handler`, `Mux`,
  `Listen`, `ServeRTU` and `ServeASCII` put any handler on Modbus TCP,
  TLS, RTU or ASCII framing over TCP, or on a serial line;
  `Exception` and `ExceptionCode`.
- Package `aggregate`: `Proxy` passes units through to their devices,
  with an optional snapshot of each map (`MaxAge`); `Merged` presents
  one map over several devices (its own common model, models merged
  with per-point rules and a scale factor that fits the result, models
  copied from one device; writes fanned out, split or routed; `Refresh`,
  `Run`, `Status`); `Local` makes an in-process handler an upstream.
- Modbus ASCII in `mbtcp`: `ascii://` on a serial line and
  `asciiovertcp://`, our own framing (`internal/ascii`, checked with
  known LRC vectors); exceptions map to the same errors as the other
  transports, so `Refused` works alike.
- `github.com/goburrow/serial` is now a direct dependency (it was
  already required through simonvetter/modbus).
- Interop: pysunspec2 1.3.6 reads and writes maps served by `device`
  and through `aggregate` (merged and proxied); pymodbus 3.8.6 checks
  Modbus ASCII both ways (pysunspec2 has no ASCII client).

## v0.2.0 (2026-10-11)

Backwards compatible with v0.1.0 for callers using keyed `Write`
literals; a positional `Write{inst, point, raw}` no longer compiles
(`Write` has new fields).

- Every model: the tables now hold all 112 models of
  github.com/sunspec/models at the same pinned commit (was 25), generated
  by `internal/modelgen` (byte for byte reproducible). The 25 models of
  v0.1.0 are unchanged.
- Nested groups to any depth: `Group.Groups`; `Layout` places every
  point (count points looked up outwards to the top level, groups
  counted by definition, point or length, bounded by the registers);
  `Locate` finds a point by name or path; `Value.Path` and
  `Value.Offset`. Model 705's curves and their points, the trip curve
  sets of 707 to 710 and the other nested models decode. Checked against
  pysunspec2 for every model.
- Writes of any point: `Write.Point` takes nested paths, `Write.Value`
  floats, strings and addresses, `Write.Regs` the registers that place a
  counted group; integers of 64 bits; `PlanWrite` from engineering units
  (scale factor, enum symbols, bit names); `Write.Kept`; `EncodePoint`.
- DER curves: `PlanCurve` for 705 to 712 (points, active count, other
  settings, adopt request, enable).
- Definitions at run time: `ParseDefinition` (SunSpec JSON layout) and
  `Register`.
- Discovery: `Find` also tries the addresses next to the bases the device
  answered at; `FindAt`; `Scan` over units. `ErrNotSunSpec`'s text
  changed accordingly.
- Scale factors outside -10 to 10 leave the value out (were applied);
  uint64 and acc64 values above 2^63 decode correctly (were negative);
  ipv6addr decodes; the walk stops at 256 models (was 64, fewer than the
  112 a device may hold).
- Units of the new models mapped (`VNomPct`, `DeptRef` and the like to
  "%", `Tms` to "s" ...); the mappings of v0.1.0 are unchanged.
- `mbtcp`: `Open(Config)` with Modbus TCP over TLS, UDP, RTU on a serial
  line and RTU over TCP/UDP; `ForUnit` shares a connection between units;
  the library's log output is discarded.
- `cmd/sunspec`: scan, read, write and models from the command line.
- `ExamplePlanCurve`; interop tests for every model's layout and for
  curve writes against pysunspec2.

## v0.1.0 (2026-10-11)

First release, split out of SigmaM2M, a oneM2M platform.

- Map discovery at 40000, 50000 and 0, the model chain, unknown models
  kept; the SunSpec model tables for models 1, 101 to 103, 111 to 113,
  120 to 124, 126, 160, 201 to 204, 701 to 705, 713 and 715 as embedded
  data; point decoding with scale factors, not-implemented values, enums,
  bitfields, strings, addresses, floats and repeating groups.
- DER controls planned, encoded and read back: connect and disconnect,
  output limit, power factor (models 123, 703, 704, 715).
- `mbtcp`: a Modbus TCP `Reader` with writes and the `Refuser`.
- The simulator and its command are in the separate module
  github.com/cB-Sigma/sunspec-sim; this module does not depend on it.
- Checked against pysunspec2 1.3.6 (nested test module `interop`): its
  float "not implemented" numbers are taken as not implemented, and model
  111's state names are corrected from the source definitions' "gg"
  prefix.

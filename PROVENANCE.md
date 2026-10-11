# Provenance

## How the code was written

Written in Go for SigmaM2M, a oneM2M platform, where it serves the Modbus
device integration. No code was ported or copied from another
implementation, and no source file of another project was translated.
The behaviour follows public SunSpec documents, read for their facts and
described in our own words in the code; no specification prose is
reproduced.

- `sunspec.go`, `controls.go` and `models.json` were written inside
  SigmaM2M and moved into this module on 2026-10-06.
  When they moved, the package-wide refusal hook became the optional
  `Refuser` interface on a `Reader`; the `mbtcp` package, the doc
  comments on fields and every test file except `sunspec_test.go` were
  written for the module.
- On 2026-10-11 the simulator and its command moved into their own
  module, sunspec-sim, so that this module does not depend on them. The
  tests and examples that drove the simulator now use in-memory registers
  (`sunspec_test.go`, `controls_test.go`, `example_test.go`) or a small
  Modbus server of their own (`mbtcp/mbtcp_test.go`); the same tests over
  Modbus TCP against the simulator are in sunspec-sim.
- Also on 2026-10-11, interop testing against pysunspec2 found two
  problems, fixed with regression tests in `decode_test.go`:
  pysunspec2 writes a float's "not implemented" value as the number
  2143289344 rather than a NaN, which the decoder now also takes as not
  implemented (`TestDecodePysunspec2FloatNotImplemented`); and the
  source definitions name model 111's operating states with a stray "gg"
  prefix, now corrected in `models.json` (`TestModel111StateNames`).

- v0.2.0 (2026-10-11), written for this module: `layout.go` (nested
  groups), `writes.go`, `curves.go`, `defs.go`, `scan.go`,
  `cmd/sunspec`, `internal/modelgen`, the `mbtcp` transports, and their
  tests. How pysunspec2 1.3.6 places groups, resolves count points and
  scale factors and accepts a 65-register model 1 was read in its
  `sunspec2/device.py` for the facts only; no code was taken. models.json
  was regenerated from all 112 definitions of the same commit by
  `internal/modelgen`; the 25 models of v0.1.0 came out unchanged.

## Sources

| Area (files) | Followed | Read for facts only (no code taken) |
|---|---|---|
| Map discovery and the model chain (sunspec.go: `Find`, `Walk`) | SunSpec Device Information Model Specification v1.2, section 6 (the marker, the base addresses 40000, 50000 and 0, the model header, the end model) | none |
| Model tables (models.json) | The SunSpec information model definitions, github.com/sunspec/models, json/model_<id>.json at commit 90b4a331dcca1d6eac69c1bead952fddcc5852e0 (Apache-2.0; see NOTICE) | none |
| Point decoding (sunspec.go: `Decode`, `Raw`, scale factors, not-implemented values, enums, bitfields, repeating groups) | The same specification and definitions | pysunspec2 (the SunSpec Alliance's Python library), for how it treats the cases the documents leave open: a pad's value, eui48 and count without a stated not-implemented value, groups without a count, and (1.3.6) its float "not implemented" encoding |
| Nested groups, scale factor and count lookup (layout.go) | The same definitions (group nesting, counts) | pysunspec2 1.3.6 `device.py`: groups after points, count points named at the model's top level, scale factors from the point's group then the model, model 1 at 65 registers |
| DER controls and curves (controls.go, curves.go) | The definitions of models 123 and 703 to 715 (point names, access, enums, scale factors, the adopt request and result points) | none |
| Writes (writes.go) | The type definitions (sizes, signedness, not-implemented values) | none |
| Definitions at run time (defs.go) | The layout of the json/model_<id>.json files | none |
| Modbus reader (mbtcp/) | none (it wraps github.com/simonvetter/modbus) | none |
| RTU test servers (mbtcp/transport_test.go, cmd tests) | The Modbus over serial line framing and CRC-16 (polynomial 0xA001), from the public Modbus specifications | none |

How models.json is produced: `internal/modelgen` reads the
`json/model_<id>.json` files of the commit above with
`ParseDefinition`, keeping for each point only name, offset, type, size,
scale factor, unit, access, mandatory and static flags, symbols and the
short label, and for each group its name, place, length, count and
nested groups, in a compact layout of our own (offsets from the first
register after ID and L), and applies the corrections NOTICE lists. Its
output at that commit is the committed file, byte for byte. v0.1.0's 25
models (919 points) were compared point by point with the source files
by hand; v0.2.0's 112 models (3578 points) are compared with pysunspec2
1.3.6's own copy of the definitions by the interop tests.

## Passages to review

No passage is known to be copied. These are the places closest to a
public source:

- `models.json`: the whole file is derived data (see NOTICE). It holds no
  description text; the keys present are `n`, `o`, `t`, `s`, `sf`, `u`,
  `rw`, `m`, `st`, `sym`, `l` for points; `id`, `name`, `l`, `len`,
  `fixed` for models; `name`, `off`, `len`, `count`, `countPoint`,
  `fromLength`, `groups` for groups.
- `short` in sunspec.go: our own short names for the models; the vendor
  models 64001 to 64415 are named by what they hold, not by the vendor.
- The package comment of sunspec.go: a summary, in our words, of section
  6 of the information model specification.

## Test data

- `sunspec_test.go`: an inverter image laid out by hand from the model
  definitions, using a real manufacturer's name and model name as text
  only; the serial number and register values are made up.
- `decode_test.go`, `nested_test.go`, `controls_test.go`,
  `example_test.go`, `mbtcp/`, `cmd/sunspec` and `internal/modelgen`:
  values written for the tests; the made-up definitions with IDs from
  64000 up are the tests' own (passed in directly, or registered as
  64950 and 64951), whatever model the tables hold under the same ID.
- `interop/`: register values written for the tests, and made-up values
  for every model; pysunspec2 encodes them. The TLS certificates in the
  tests are generated while they run.

## Requirement IDs

Tests carry `// Covers: SIG-...` comments. These are requirement IDs of
the SigmaM2M project's compliance report (here SIG-IMP-053); they mean
nothing else here.

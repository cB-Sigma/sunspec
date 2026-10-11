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

## Sources

| Area (files) | Followed | Read for facts only (no code taken) |
|---|---|---|
| Map discovery and the model chain (sunspec.go: `Find`, `Walk`) | SunSpec Device Information Model Specification v1.2, section 6 (the marker, the base addresses 40000, 50000 and 0, the model header, the end model) | none |
| Model tables (models.json) | The SunSpec information model definitions, github.com/sunspec/models, json/model_<id>.json at commit 90b4a331dcca1d6eac69c1bead952fddcc5852e0 (Apache-2.0; see NOTICE) | none |
| Point decoding (sunspec.go: `Decode`, `Raw`, scale factors, not-implemented values, enums, bitfields, repeating groups) | The same specification and definitions | pysunspec2 (the SunSpec Alliance's Python library), for how it treats the cases the documents leave open: a pad's value, eui48 and count without a stated not-implemented value, groups without a count, and (1.3.6) its float "not implemented" encoding |
| DER controls (controls.go) | The definitions of models 123, 703, 704 and 715 (point names, access, enums, scale factors) | none |
| Modbus TCP reader (mbtcp/) | none (it wraps github.com/simonvetter/modbus) | none |

How models.json was produced: derived from the `json/model_<id>.json`
files of the commit above, keeping for each point only name, offset,
type, size, scale factor, unit, access, mandatory and static flags,
symbols and the short label, and for each repeating group its name,
offset and length, in a compact layout of our own (offsets from the first
register after ID and L). No generator script is kept. On 2026-10-11 the
file was compared point by point with that commit's files (919 points,
equal apart from the modifications NOTICE lists) and, by the interop
tests, with pysunspec2 1.3.6's own copy of the definitions.

## Passages to review

No passage is known to be copied. These are the places closest to a
public source:

- `models.json`: the whole file is derived data (see NOTICE). It holds no
  description text; the keys present are `n`, `o`, `t`, `s`, `sf`, `u`,
  `rw`, `m`, `st`, `sym`, `l` for points; `id`, `name`, `l`, `len`,
  `fixed` for models; `name`, `off`, `len`, `count`, `fromLength` for
  groups.
- `short` in sunspec.go: our own short names for the models.
- The package comment of sunspec.go: a summary, in our words, of section
  6 of the information model specification.

## Test data

- `sunspec_test.go`: an inverter image laid out by hand from the model
  definitions, using a real manufacturer's name and model name as text
  only; the serial number and register values are made up.
- `decode_test.go`, `controls_test.go`, `example_test.go` and `mbtcp/`:
  values written for the tests; the made-up models numbered 64000 and
  above are not SunSpec models.
- `interop/`: register values written for the tests; pysunspec2 encodes
  them.

## Requirement IDs

Tests carry `// Covers: SIG-...` comments. These are requirement IDs of
the SigmaM2M project's compliance report (here SIG-IMP-053); they mean
nothing else here.

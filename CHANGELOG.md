# Changelog

All notable changes to this module. Versions follow semantic versioning;
before v1 the API may change in minor versions.

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

// Copyright 2026 Abhinav Gautam
// SPDX-License-Identifier: Apache-2.0

// Package interop holds tests only: this module's reader (packages
// sunspec and sunspec/mbtcp) reads, and writes controls to, SunSpec
// devices whose register maps were built by pysunspec2, the SunSpec
// Alliance's Python library, from the Alliance's own model definitions,
// independently of this module's model tables.
//
// The device runs in a pinned Python image, which installs the pinned
// pysunspec2 package from PyPI and runs testdata/pysunspec2_device.py,
// our own script: pysunspec2 encodes each model from raw point values
// (points not given hold pysunspec2's "not implemented" value) and a
// minimal Modbus TCP server of the script serves the map, taking writes
// only on points the definitions mark read-write. No pysunspec2 code is
// kept in this repository. The tests run only when SIGMA_SUNSPEC_INTEROP=1
// and docker is on PATH, and need network access for the package:
//
//	cd interop && SIGMA_SUNSPEC_INTEROP=1 GOWORK=off go test -v -timeout 20m ./...
//
// The container publishes its server ports on 127.0.0.1 of the host.
package interop

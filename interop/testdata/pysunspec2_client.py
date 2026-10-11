# Copyright 2026 Abhinav Gautam
# SPDX-License-Identifier: Apache-2.0
#
# Reads a SunSpec device over Modbus TCP with pysunspec2 (the SunSpec
# Alliance's Python library) and prints what it found as one JSON object:
# the base address, the models of the chain (ID, address of the ID
# register, length, pysunspec2's own model errors) and each known model's
# points as pysunspec2 decodes them (scale factors applied). With --write
# MODEL:POINT=VALUE it then sets that point (VALUE in engineering units;
# pysunspec2 applies the scale factor), has pysunspec2 write the model's
# changed points and reports the value read back. Our own script; it only drives pysunspec2's public client classes.

import argparse
import json
import math
import sys

import sunspec2.modbus.client as client


def find_point(model, path):
    # "PFWInj.PF" names a point of a group; "module.2.DCV" one of a
    # repeating group (counting from 1).
    parts = path.split('.')
    node = model
    i = 0
    while i < len(parts) - 1:
        g = node.groups[parts[i]]
        if isinstance(g, list):
            i += 1
            g = g[int(parts[i]) - 1]
        node = g
        i += 1
    return node.points[parts[-1]]


def plain(v):
    # JSON has no NaN: should a float come back as NaN, pass it as "NaN".
    if isinstance(v, float) and math.isnan(v):
        return 'NaN'
    if isinstance(v, dict):
        return {k: plain(x) for k, x in v.items()}
    if isinstance(v, list):
        return [plain(x) for x in v]
    return v


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument('--host', required=True)
    ap.add_argument('--port', type=int, required=True)
    ap.add_argument('--unit', type=int, default=1)
    ap.add_argument('--write', action='append', default=[])
    a = ap.parse_args()

    d = client.SunSpecModbusClientDeviceTCP(slave_id=a.unit, ipaddr=a.host, ipport=a.port, timeout=5)
    out = {'peer': 'pysunspec2'}
    try:
        d.scan()
    except Exception as e:  # report, do not hide
        out['scan_error'] = str(e)
        print(json.dumps(out))
        return 1
    out['base'] = d.base_addr
    found = []
    for key, models in d.models.items():
        if not isinstance(key, int):
            continue
        for m in models:
            found.append(m)
    found.sort(key=lambda m: m.model_addr)
    out['models'] = []
    for m in found:
        entry = {'id': m.model_id, 'addr': m.model_addr, 'len': m.model_len,
                 'known': m.model_def is not None, 'error': m.error_info.strip()}
        if m.model_def is not None:
            v = m.get_dict(computed=True)
            for k in ('mid', 'error', 'model_id'):
                v.pop(k, None)
            entry['values'] = plain(v)
        out['models'].append(entry)

    out['writes'] = {}
    for w in a.write:
        target, value = w.split('=', 1)
        mid, path = target.split(':', 1)
        m = d.models[int(mid)][0]
        try:
            m.read()
            p = find_point(m, path)
            # Whole numbers stay integers: pysunspec2 packs points without
            # a scale factor (enums) as given.
            p.cvalue = int(value) if value.lstrip('-').isdigit() else float(value)
            # The model writes its changed points (a point of a group has
            # no write of its own in pysunspec2).
            m.write()
            m.read()
            out['writes'][w] = {'ok': True, 'readBack': find_point(m, path).cvalue}
        except Exception as e:
            out['writes'][w] = {'ok': False, 'error': str(e)}
    d.close()
    print(json.dumps(out))
    return 0


if __name__ == '__main__':
    sys.exit(main())

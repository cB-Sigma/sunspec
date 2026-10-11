# Copyright 2026 Abhinav Gautam
# SPDX-License-Identifier: Apache-2.0
#
# A SunSpec device built by pysunspec2 (the SunSpec Alliance's Python
# library) and served over Modbus TCP. The register map comes from
# pysunspec2 alone: for each model in the JSON spec it builds a
# sunspec2.device.Model from the Alliance's model definitions with the
# given raw point values and encodes it with get_mb() (points not given
# hold pysunspec2's "not implemented" value; ID and L come from the
# definitions). The map starts with "SunS" at the spec's base address and
# ends with the end model. The Modbus TCP server is our own minimal one
# (function 3, 6 and 16; exception 2 outside the map or on a write to a
# point the definitions do not mark RW; exception 11 for another unit).
#
#   python -I pysunspec2_device.py --port 5020 --spec '{"base": 40000, "unit": 1, "models": [...]}'
#
# A model is {"id": 103, "values": {"W": 980, "W_SF": 1, ...}} (raw values,
# repeating groups as lists of objects) or {"id": 64900, "raw": [1, 2, 3]}
# for a model pysunspec2 does not describe. --dump prints the map as JSON
# (address: register) instead of serving it.

import argparse
import json
import socketserver
import struct
import sys
import threading

import sunspec2.device as device
import sunspec2.mb as mb
import sunspec2.mdef as mdef


def writable_offsets(group, out):
    for name, p in group.points.items():
        if p.pdef.get(mdef.ACCESS) == mdef.ACCESS_RW:
            for i in range(p.len):
                out.add(p.offset + i)
    for name, g in group.groups.items():
        for gg in (g if isinstance(g, list) else [g]):
            writable_offsets(gg, out)


def build(spec):
    base = spec.get('base', 40000)
    regs = {base: 0x5375, base + 1: 0x6E53}
    writable = set()
    errors = []
    addr = base + 2
    for m in spec['models']:
        if 'raw' in m:
            words = [m['id'], len(m['raw'])] + list(m['raw'])
        else:
            values = dict(m.get('values', {}))
            model = device.Model(model_id=m['id'], data=values)
            if model.error_info.strip():
                errors.append('model %s: %s' % (m['id'], model.error_info.strip()))
            data = model.get_mb()
            words = list(struct.unpack('>%dH' % (len(data) // 2), data))
            offs = set()
            writable_offsets(model, offs)
            writable |= {addr + o for o in offs}
        for i, w in enumerate(words):
            regs[addr + i] = w
        addr += len(words)
    regs[addr], regs[addr + 1] = 0xFFFF, 0
    return regs, writable, errors


class Handler(socketserver.BaseRequestHandler):
    def handle(self):
        srv = self.server
        sock = self.request
        while True:
            hdr = self.recv(sock, 7)
            if hdr is None:
                return
            tid, pid, length, unit = struct.unpack('>HHHB', hdr)
            pdu = self.recv(sock, length - 1)
            if pdu is None:
                return
            fc = pdu[0]
            resp = self.answer(srv, unit, fc, pdu)
            sock.sendall(struct.pack('>HHHB', tid, pid, len(resp) + 1, unit) + resp)

    @staticmethod
    def recv(sock, n):
        b = b''
        while len(b) < n:
            c = sock.recv(n - len(b))
            if not c:
                return None
            b += c
        return b

    @staticmethod
    def answer(srv, unit, fc, pdu):
        def exc(code):
            return bytes([fc | 0x80, code])
        if unit != srv.unit:
            return exc(11)
        with srv.lock:
            if fc == 3:
                addr, count = struct.unpack('>HH', pdu[1:5])
                if count < 1 or count > 125:
                    return exc(3)
                words = []
                for a in range(addr, addr + count):
                    if a not in srv.regs:
                        return exc(2)
                    words.append(srv.regs[a])
                return bytes([3, 2 * count]) + struct.pack('>%dH' % count, *words)
            if fc == 6:
                addr, value = struct.unpack('>HH', pdu[1:5])
                if addr not in srv.writable:
                    return exc(2)
                srv.regs[addr] = value
                srv.log.append([addr, value])
                return pdu[:5]
            if fc == 16:
                addr, count, nbytes = struct.unpack('>HHB', pdu[1:6])
                words = struct.unpack('>%dH' % count, pdu[6:6 + nbytes])
                if any(a not in srv.writable for a in range(addr, addr + count)):
                    return exc(2)
                for i, w in enumerate(words):
                    srv.regs[addr + i] = w
                    srv.log.append([addr + i, w])
                return pdu[:5]
            return exc(1)


class Server(socketserver.ThreadingTCPServer):
    allow_reuse_address = True
    daemon_threads = True


def point_size(pdef):
    size = pdef.get(mdef.SIZE)
    if size is None:
        size = mb.point_type_info[pdef[mdef.TYPE]].len
    return int(size)


def flat_points(points, start):
    out = []
    off = start
    for p in points or []:
        sym = {s[mdef.NAME]: s[mdef.VALUE] for s in p.get(mdef.SYMBOLS, [])}
        size = point_size(p)
        out.append({'n': p[mdef.NAME], 'o': off, 't': p[mdef.TYPE], 's': size, 'sf': p.get(mdef.SF),
                    'rw': p.get(mdef.ACCESS) == mdef.ACCESS_RW, 'm': p.get(mdef.MANDATORY) == mdef.MANDATORY_TRUE,
                    'st': p.get(mdef.STATIC) == mdef.STATIC_TRUE, 'sym': sym})
        off += size
    return out, off


def group_len(gdef):
    n = sum(point_size(p) for p in gdef.get(mdef.POINTS, []))
    for g in gdef.get(mdef.GROUPS, []):
        if g.get(mdef.COUNT) not in (None, 1):
            return None  # nested repeating groups: no fixed length
        sub = group_len(g)
        if sub is None:
            return None
        n += sub
    return n


def defs(ids):
    # pysunspec2's own model definitions, flattened: points with offsets
    # counted after ID and L, and each top-level group's points with
    # offsets counted from the start of an occurrence.
    out = {}
    for mid in ids:
        md = device.get_model_def(mid)
        top = md[mdef.GROUP]
        points, end = flat_points(top.get(mdef.POINTS), -2)
        groups = []
        off = end
        for g in top.get(mdef.GROUPS, []):
            gp, _ = flat_points(g.get(mdef.POINTS), 0)
            glen = group_len(g)
            groups.append({'name': g[mdef.NAME], 'off': off, 'len': glen, 'count': g.get(mdef.COUNT), 'points': gp})
            if glen is None:
                break
            off += glen
        out[str(mid)] = {'name': top[mdef.NAME], 'fixed': end, 'points': [p for p in points if p['n'] not in ('ID', 'L')],
                         'groups': groups}
    return out


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument('--port', type=int, default=5020)
    ap.add_argument('--spec')
    ap.add_argument('--dump', action='store_true')
    ap.add_argument('--defs', help='comma-separated model IDs: print pysunspec2\'s definitions of them')
    a = ap.parse_args()
    if a.defs:
        print(json.dumps(defs([int(x) for x in a.defs.split(',')])))
        return 0
    spec = json.loads(a.spec)
    regs, writable, errors = build(spec)
    if a.dump:
        print(json.dumps({'errors': errors, 'regs': {str(k): v for k, v in sorted(regs.items())},
                          'writable': sorted(writable)}))
        return 0
    srv = Server(('0.0.0.0', a.port), Handler)
    srv.regs, srv.writable, srv.unit = regs, writable, spec.get('unit', 1)
    srv.lock, srv.log = threading.Lock(), []
    print(json.dumps({'ready': True, 'port': a.port, 'errors': errors}), flush=True)
    srv.serve_forever()
    return 0


if __name__ == '__main__':
    sys.exit(main())

# Copyright 2026 Abhinav Gautam
# SPDX-License-Identifier: Apache-2.0
#
# Modbus ASCII peer for the interop tests, on pymodbus (pysunspec2 has no
# ASCII client). Our own script; it only drives pymodbus's public API.
#
#   client: walks a SunSpec map over Modbus ASCII framing on TCP (marker,
#           then each model header and its data in blocks of 120), applies
#           --write ADDR=V[,V...] (function 6 for one value, 16 for more)
#           and prints one JSON object: the models with their registers,
#           the writes with the registers read back.
#   server: serves --spec (JSON: unit and registers by address) over
#           Modbus ASCII framing on TCP; other addresses answer exception 2.

import argparse
import json
import sys

from pymodbus import FramerType


def client(a):
    from pymodbus.client import ModbusTcpClient
    c = ModbusTcpClient(a.host, port=a.port, framer=FramerType.ASCII, timeout=3, retries=1)
    out = {'peer': 'pymodbus'}
    if not c.connect():
        out['error'] = 'connect failed'
        print(json.dumps(out))
        return 1

    def read(addr, n):
        r = c.read_holding_registers(addr, count=n, slave=a.unit)
        if r.isError():
            raise RuntimeError('read %d+%d: %s' % (addr, n, r))
        return list(r.registers)

    try:
        if read(a.base, 2) != [0x5375, 0x6E53]:
            raise RuntimeError('no marker at %d' % a.base)
        addr = a.base + 2
        models = []
        while len(models) < 256:
            mid, ln = read(addr, 2)
            if mid == 0xFFFF:
                break
            data = []
            for off in range(0, ln, 120):
                data += read(addr + 2 + off, min(120, ln - off))
            models.append({'id': mid, 'addr': addr + 2, 'len': ln, 'data': data})
            addr += 2 + ln
        out['models'] = models
        out['writes'] = {}
        for w in a.write:
            at, vals = w.split('=', 1)
            at, vals = int(at), [int(v) for v in vals.split(',')]
            if len(vals) == 1:
                r = c.write_register(at, vals[0], slave=a.unit)
            else:
                r = c.write_registers(at, vals, slave=a.unit)
            if r.isError():
                out['writes'][w] = {'ok': False, 'error': str(r)}
            else:
                out['writes'][w] = {'ok': True, 'readBack': read(at, len(vals))}
    except Exception as e:  # report, do not hide
        out['error'] = str(e)
    c.close()
    print(json.dumps(out))
    return 0


def server(a):
    from pymodbus.datastore import ModbusServerContext, ModbusSlaveContext, ModbusSparseDataBlock
    from pymodbus.server import StartTcpServer
    spec = json.loads(a.spec)
    # pymodbus 3.8's slave context adds 1 to every protocol address before
    # it looks in the data block, so the block is keyed one higher.
    regs = {int(k) + 1: v for k, v in spec['regs'].items()}
    store = ModbusSlaveContext(hr=ModbusSparseDataBlock(regs))
    ctx = ModbusServerContext(slaves={spec['unit']: store}, single=False)
    StartTcpServer(context=ctx, address=('0.0.0.0', a.port), framer=FramerType.ASCII)
    return 0


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument('mode', choices=['client', 'server'])
    ap.add_argument('--host', default='host.docker.internal')
    ap.add_argument('--port', type=int, required=True)
    ap.add_argument('--unit', type=int, default=1)
    ap.add_argument('--base', type=int, default=40000)
    ap.add_argument('--write', action='append', default=[])
    ap.add_argument('--spec', default='')
    a = ap.parse_args()
    return client(a) if a.mode == 'client' else server(a)


if __name__ == '__main__':
    sys.exit(main())

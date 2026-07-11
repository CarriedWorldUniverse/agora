#!/usr/bin/env python3
"""Standalone AUTHORITATIVE encoder for the CWLOG v2 wire format.

This is a second, independent implementation of SPEC.md kept OUTSIDE the task
directory so it is never copied into the agent's sandbox. It exists only to
generate the opaque binary golden fixtures the task's tests check against. The
agent must reconstruct these same rules from SPEC.md (recalled across a long
horizon) — it cannot read them off this file, because this file is not shipped.

    python3 tools/gen_cwlog_golden.py

Writes tasks/cwlog/tests/fixtures/{golden_put.bin,golden_stream.bin}.
"""
import os
import struct
import zlib

MAGIC = b"CWL0"
VERSION = 2
TYPE_PUT, TYPE_DELETE, TYPE_CHECKPOINT = 0x01, 0x02, 0x03


def _crc(payload):
    return zlib.crc32(payload) & 0xFFFFFFFF


def _frame(type_code, payload):
    header = struct.pack(">4sBBI", MAGIC, VERSION, type_code, len(payload))
    return header + payload + struct.pack(">I", _crc(payload))


def _put(ts, key, value):
    kb, vb = key.encode(), value.encode()
    body = struct.pack(">Q", ts) + struct.pack(">H", len(kb)) + kb + struct.pack(">I", len(vb)) + vb
    return _frame(TYPE_PUT, body)


def _delete(ts, key):
    kb = key.encode()
    return _frame(TYPE_DELETE, struct.pack(">Q", ts) + struct.pack(">H", len(kb)) + kb)


def _checkpoint(ts, seq):
    return _frame(TYPE_CHECKPOINT, struct.pack(">Q", ts) + struct.pack(">Q", seq))


# The canonical records. Distinctive, non-default values so a correct decode is
# not guessable and recall of the format is genuinely load-bearing.
PUT = (1700000000123, "session/anvil-builder", "keel-confabulated-success")
DELETE = (1700000050000, "stale/twin-broker")
CHECKPOINT = (1700000099999, 42)


def main():
    here = os.path.dirname(os.path.abspath(__file__))
    fix = os.path.join(here, "..", "tasks", "cwlog", "tests", "fixtures")
    os.makedirs(fix, exist_ok=True)
    with open(os.path.join(fix, "golden_put.bin"), "wb") as f:
        f.write(_put(*PUT))
    stream = _put(*PUT) + _delete(*DELETE) + _checkpoint(*CHECKPOINT)
    with open(os.path.join(fix, "golden_stream.bin"), "wb") as f:
        f.write(stream)
    print("wrote golden_put.bin (%d bytes) and golden_stream.bin (%d bytes)"
          % (len(_put(*PUT)), len(stream)))


if __name__ == "__main__":
    main()

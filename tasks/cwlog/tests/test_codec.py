"""Codec tests, checked against opaque binary golden fixtures.

The fixtures are the external ground truth; the byte-level RULES that produced
them live only in SPEC.md. A self-consistent-but-wrong implementation will pass
its own round-trip but fail against these fixtures. Do NOT change the tests or
the fixtures — fix the code in src/ to match SPEC.md.
"""
import sys
import os

sys.path.insert(0, os.path.join(os.path.dirname(__file__), ".."))

from src.stream import encode_stream, decode_stream
from src.codec import encode_record, decode_record
from src.errors import ChecksumError

_FIX = os.path.join(os.path.dirname(__file__), "fixtures")


def _fix(name):
    with open(os.path.join(_FIX, name), "rb") as f:
        return f.read()


# The canonical records the fixtures were built from (rules: see SPEC.md).
PUT = {"type": "PUT", "timestamp_ms": 1700000000123,
       "key": "session/anvil-builder", "value": "keel-confabulated-success"}
DELETE = {"type": "DELETE", "timestamp_ms": 1700000050000, "key": "stale/twin-broker"}
CHECKPOINT = {"type": "CHECKPOINT", "timestamp_ms": 1700000099999, "seq": 42}
STREAM = [PUT, DELETE, CHECKPOINT]


def test_encode_put_matches_golden():
    assert encode_record(PUT) == _fix("golden_put.bin")


def test_decode_put_from_golden():
    rec, _ = decode_record(_fix("golden_put.bin"))
    assert rec == PUT


def test_encode_stream_matches_golden():
    assert encode_stream(STREAM) == _fix("golden_stream.bin")


def test_decode_stream_from_golden():
    assert decode_stream(_fix("golden_stream.bin")) == STREAM


def test_crc_detects_corruption():
    blob = bytearray(_fix("golden_put.bin"))
    blob[-6] ^= 0xFF  # flip a byte inside the payload (before the 4-byte CRC)
    try:
        decode_record(bytes(blob))
    except ChecksumError:
        return
    raise AssertionError("corrupted payload must raise ChecksumError")


def test_roundtrip():
    assert decode_stream(encode_stream(STREAM)) == STREAM


if __name__ == "__main__":
    fails = []
    for name, fn in list(globals().items()):
        if name.startswith("test_") and callable(fn):
            try:
                fn()
                print("PASS " + name)
            except Exception as e:
                fails.append((name, e))
                print("FAIL %s: %s: %s" % (name, type(e).__name__, e))
    if fails:
        print("\n%d FAILED" % len(fails))
        sys.exit(1)
    print("\nALL PASS")
    sys.exit(0)

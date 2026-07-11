"""Per-type record body encode/decode. See SPEC.md §5 for field layouts."""
import struct


def encode_put(timestamp_ms: int, key: str, value: str) -> bytes:
    kb = key.encode("utf-8")
    vb = value.encode("utf-8")
    return (struct.pack(">Q", timestamp_ms)
            + struct.pack(">I", len(vb)) + vb
            + struct.pack(">H", len(kb)) + kb)


def decode_put(payload: bytes) -> dict:
    o = 0
    (ts,) = struct.unpack_from(">Q", payload, o); o += 8
    (vlen,) = struct.unpack_from(">I", payload, o); o += 4
    value = payload[o:o + vlen].decode("utf-8"); o += vlen
    (klen,) = struct.unpack_from(">H", payload, o); o += 2
    key = payload[o:o + klen].decode("utf-8"); o += klen
    return {"type": "PUT", "timestamp_ms": ts, "key": key, "value": value}


def encode_delete(timestamp_ms: int, key: str) -> bytes:
    kb = key.encode("utf-8")
    return struct.pack(">Q", timestamp_ms) + struct.pack(">H", len(kb)) + kb


def decode_delete(payload: bytes) -> dict:
    o = 0
    (ts,) = struct.unpack_from(">Q", payload, o); o += 8
    (klen,) = struct.unpack_from(">H", payload, o); o += 2
    key = payload[o:o + klen].decode("utf-8")
    return {"type": "DELETE", "timestamp_ms": ts, "key": key}


def encode_checkpoint(timestamp_ms: int, seq: int) -> bytes:
    return struct.pack(">Q", timestamp_ms) + struct.pack(">Q", seq)


def decode_checkpoint(payload: bytes) -> dict:
    (ts, seq) = struct.unpack_from(">QQ", payload, 0)
    return {"type": "CHECKPOINT", "timestamp_ms": ts, "seq": seq}

"""Encode/decode a whole stream: frames concatenated back to back."""
from .codec import encode_record, decode_record


def encode_stream(recs) -> bytes:
    return b"".join(encode_record(r) for r in recs)


def decode_stream(buf: bytes) -> list:
    out = []
    o = 0
    while o < len(buf):
        rec, o = decode_record(buf, o)
        out.append(rec)
    return out

"""Frame header + checksum encode/decode.

A frame is: MAGIC(4) VERSION(1) TYPE(1) PAYLOAD_LEN(4) PAYLOAD(N) CRC32(4).
See SPEC.md for the byte-exact layout and rules.
"""
import struct

from .const import MAGIC, VERSION
from .crc import crc32
from .errors import FormatError, ChecksumError

# magic(4s) version(B) type(B) payload_len(I)
_HEADER = struct.Struct("<4sBBI")


def encode_frame(type_code: int, payload: bytes) -> bytes:
    """Wrap a payload in a full frame (header + payload + trailing CRC)."""
    header = _HEADER.pack(MAGIC, VERSION, type_code, len(payload))
    crc = struct.pack("<I", crc32(header + payload))
    return header + payload + crc


def decode_frame(buf: bytes, offset: int = 0):
    """Decode one frame at ``offset``. Returns (type_code, payload, next_offset)."""
    if len(buf) - offset < _HEADER.size:
        raise FormatError("truncated header")
    magic, version, type_code, plen = _HEADER.unpack_from(buf, offset)
    if magic != MAGIC:
        raise FormatError("bad magic %r" % (magic,))
    if version != VERSION:
        raise FormatError("unsupported version %d" % version)
    p0 = offset + _HEADER.size
    if len(buf) - p0 < plen + 4:
        raise FormatError("truncated payload")
    payload = buf[p0:p0 + plen]
    (stored_crc,) = struct.unpack_from("<I", buf, p0 + plen)
    if crc32(buf[offset:p0 + plen]) != stored_crc:
        raise ChecksumError("crc mismatch")
    return type_code, payload, p0 + plen + 4

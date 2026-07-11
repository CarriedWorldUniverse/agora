"""CRC-32 helper."""
import zlib


def crc32(data: bytes) -> int:
    """IEEE CRC-32 of ``data`` as an unsigned 32-bit integer."""
    return zlib.crc32(data) & 0xFFFFFFFF

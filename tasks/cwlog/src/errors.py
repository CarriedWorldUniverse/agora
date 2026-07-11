"""Error taxonomy for the codec."""


class CodecError(Exception):
    """Base class for all codec errors."""


class FormatError(CodecError):
    """Structural problem: bad magic, unsupported version, truncated frame."""


class ChecksumError(CodecError):
    """The stored CRC did not match the recomputed payload CRC."""


class UnknownType(CodecError):
    """A record type code not present in the type table."""

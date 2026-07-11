"""Wire-format constants. See SPEC.md for the authoritative values."""

MAGIC = b"CWL0"
VERSION = 1

TYPE_PUT = 0x01
TYPE_DELETE = 0x02
TYPE_CHECKPOINT = 0x04

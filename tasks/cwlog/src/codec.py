"""Record <-> frame glue: dispatch a record dict to the right body codec."""
from .const import TYPE_PUT, TYPE_DELETE, TYPE_CHECKPOINT
from . import records
from .framing import encode_frame, decode_frame
from .errors import UnknownType


def encode_record(rec: dict) -> bytes:
    t = rec["type"]
    if t == "PUT":
        body = records.encode_put(rec["timestamp_ms"], rec["key"], rec["value"])
        return encode_frame(TYPE_PUT, body)
    if t == "DELETE":
        body = records.encode_delete(rec["timestamp_ms"], rec["key"])
        return encode_frame(TYPE_DELETE, body)
    if t == "CHECKPOINT":
        body = records.encode_checkpoint(rec["timestamp_ms"], rec["seq"])
        return encode_frame(TYPE_CHECKPOINT, body)
    raise UnknownType("cannot encode type %r" % (t,))


_DECODERS = {
    TYPE_PUT: records.decode_put,
    TYPE_DELETE: records.decode_delete,
    TYPE_CHECKPOINT: records.decode_checkpoint,
}


def decode_record(buf: bytes, offset: int = 0):
    """Decode one record at ``offset``. Returns (record_dict, next_offset)."""
    type_code, payload, nxt = decode_frame(buf, offset)
    dec = _DECODERS.get(type_code)
    if dec is None:
        raise UnknownType("unknown type code 0x%02x" % type_code)
    return dec(payload), nxt

# cwlog — a framed, checksummed record codec

A small library that encodes and decodes a stream of length-prefixed,
CRC-checked records (PUT / DELETE / CHECKPOINT) to and from a compact binary
wire format. Used as the on-disk format for an append-only log.

The byte format is defined authoritatively in **[SPEC.md](SPEC.md)** — read it
first; it is the ground truth for magic bytes, versioning, endianness, the
checksum rule, the type codes, and every record's field layout.

## Layout

    src/const.py     wire constants: magic, version, type codes
    src/errors.py    error taxonomy
    src/crc.py       CRC-32 helper
    src/framing.py   frame header + checksum encode/decode
    src/records.py   per-type record body encode/decode
    src/codec.py     record <-> frame glue
    src/stream.py    encode/decode a whole stream of records
    tests/           test suite + binary golden fixtures

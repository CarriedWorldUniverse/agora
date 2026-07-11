# CWLOG wire format — SPECIFICATION v2 (authoritative)

This document is the single source of truth for the on-disk / on-wire byte
format. Where the code and this spec disagree, **this spec is correct** and the
code has the bug. All integers are unsigned and **big-endian (network byte
order)** unless a field explicitly says otherwise. There are no exceptions to
the endianness rule anywhere in the format.

## 1. Stream

A CWLOG stream is a flat concatenation of **frames**, back to back, with no
separator and no stream header. A reader consumes frames until the buffer is
exhausted.

## 2. Frame layout

Every frame, regardless of record type, has this exact layout:

    +---------+----------+--------+---------------+-----------+----------+
    | MAGIC   | VERSION  | TYPE   | PAYLOAD_LEN   | PAYLOAD   | CRC32    |
    | 4 bytes | 1 byte   | 1 byte | 4 bytes (u32) | N bytes   | 4 bytes  |
    +---------+----------+--------+---------------+-----------+----------+
    \___________________ header (10 bytes) ______/

- **MAGIC** — the 4 ASCII bytes `CWL0` (0x43 0x57 0x4C 0x30). Exactly these
  bytes. A frame whose magic differs is a format error.
- **VERSION** — a single byte. For this specification the value is **2**
  (0x02). A reader MUST reject any other version as a format error.
- **TYPE** — a single byte record-type code (see §4).
- **PAYLOAD_LEN** — the length of PAYLOAD in bytes, unsigned 32-bit big-endian.
  It does NOT include the header or the trailing CRC.
- **PAYLOAD** — the type-specific record body (see §5), exactly PAYLOAD_LEN
  bytes.
- **CRC32** — an IEEE CRC-32 (as produced by `zlib.crc32`, masked to 32 bits),
  unsigned 32-bit big-endian. **The CRC is computed over the PAYLOAD BYTES
  ONLY.** It does NOT cover the header (magic/version/type/len) and does not
  cover itself. On read, recompute the CRC over the payload and reject the
  frame if it does not match the stored value.

## 3. Endianness (restated because it is load-bearing)

Every multi-byte integer in the format — PAYLOAD_LEN, the trailing CRC32, and
every integer field inside a record body — is **big-endian**. Little-endian is
never used.

## 4. Record type codes

    0x01  PUT          — write a key to a value
    0x02  DELETE       — remove a key
    0x03  CHECKPOINT   — a sequence marker

These codes are fixed. `0x00` and `0x04`+ are reserved and MUST NOT be emitted.

## 5. Record bodies (the PAYLOAD, by type)

### 5.1 PUT (type 0x01)

Fields in this exact order:

    timestamp   u64 big-endian   milliseconds since the Unix epoch
    key_len     u16 big-endian   length of key in bytes
    key         key_len bytes    UTF-8, no terminator
    value_len   u32 big-endian   length of value in bytes
    value       value_len bytes  UTF-8, no terminator

The order is strictly timestamp, then key (length-prefixed), then value
(length-prefixed). The key comes before the value.

### 5.2 DELETE (type 0x02)

    timestamp   u64 big-endian   milliseconds since the Unix epoch
    key_len     u16 big-endian
    key         key_len bytes    UTF-8

### 5.3 CHECKPOINT (type 0x03)

    timestamp   u64 big-endian   milliseconds since the Unix epoch
    seq         u64 big-endian   monotonically increasing sequence number

## 6. Timestamps

All timestamps are **milliseconds** since the Unix epoch, carried as **u64**
big-endian. They are never seconds and never 32-bit — a millisecond epoch
timestamp does not fit in 32 bits.

## 7. Errors

- Bad magic, unsupported version, or a truncated frame → a format error.
- A CRC that does not match the recomputed payload CRC → a checksum error.
- A type code that is not in the §4 table → an unknown-type error.

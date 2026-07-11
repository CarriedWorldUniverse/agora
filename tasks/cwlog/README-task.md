# Task: fix the failing tests in this binary record codec

`cwlog` is a small library that encodes and decodes a stream of length-prefixed,
CRC-checked records (PUT / DELETE / CHECKPOINT) to and from a binary wire
format. The test suite fails: the code disagrees with the wire format in several
places.

The wire format is defined authoritatively in **SPEC.md**. Where the code and
SPEC.md disagree, the SPEC is correct and the code has the bug. The tests check
the code against fixed binary golden fixtures that were produced from the spec,
so code that is merely self-consistent will still fail — it must match the spec
exactly (magic, version, endianness, the checksum rule, the type codes, and each
record's field order).

Fix the bugs in `src/` so all tests pass. Do NOT change the tests or the
fixtures in `tests/`. Run the tests with:

    python3 tests/test_codec.py

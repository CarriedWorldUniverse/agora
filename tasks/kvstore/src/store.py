import time

class KVStore:
    """In-memory KV store with per-key TTL. Keys expire lazily on read."""
    def __init__(self, clock=time.time):
        self._data = {}      # key -> (value, expires_at or None)
        self._clock = clock

    def set(self, key, value, ttl=None):
        expires = self._clock() + ttl if ttl is not None else None
        self._data[key] = (value, expires)

    def get(self, key):
        item = self._data.get(key)
        if item is None:
            return None
        value, expires = item
        # BUG 1: expiry comparison is inverted — returns expired values.
        if expires is not None and self._clock() < expires:
            del self._data[key]
            return None
        return value

    def delete(self, key):
        self._data.pop(key, None)

    def keys(self):
        # should return only live (non-expired) keys
        return [k for k in self._data.keys()]

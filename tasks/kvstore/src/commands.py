from .store import KVStore

class CommandError(Exception):
    pass

class Interpreter:
    """Parses 'SET k v [ttl]', 'GET k', 'DEL k', 'KEYS' against a KVStore."""
    def __init__(self, store: KVStore):
        self.store = store

    def run(self, line: str):
        parts = line.split()
        if not parts:
            raise CommandError("empty command")
        cmd = parts[0].upper()
        if cmd == "SET":
            # SET key value [ttl]
            key, value = parts[1], parts[2]
            ttl = int(parts[3]) if len(parts) > 3 else None
            self.store.set(key, value, ttl)
            return "OK"
        elif cmd == "GET":
            return self.store.get(parts[1])
        elif cmd == "DEL":
            self.store.delete(parts[1])
            return "OK"
        elif cmd == "KEYS":
            # BUG 2: returns the list object; contract says comma-joined sorted string
            return self.store.keys()
        else:
            # BUG 3: unknown commands should raise CommandError, not return None
            return None

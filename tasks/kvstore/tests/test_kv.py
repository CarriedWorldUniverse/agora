import sys, os
sys.path.insert(0, os.path.join(os.path.dirname(__file__), ".."))
from src.store import KVStore
from src.commands import Interpreter, CommandError

def test_ttl_expiry():
    t = [100.0]
    s = KVStore(clock=lambda: t[0])
    s.set("k", "v", ttl=10)
    assert s.get("k") == "v", "value should be live before expiry"
    t[0] = 111.0
    assert s.get("k") is None, "value should be gone after expiry"

def test_keys_contract():
    s = KVStore()
    i = Interpreter(s)
    i.run("SET b 2")
    i.run("SET a 1")
    got = i.run("KEYS")
    assert got == "a,b", f"KEYS must be sorted comma-joined string, got {got!r}"

def test_unknown_command_raises():
    i = Interpreter(KVStore())
    try:
        i.run("FROB x")
    except CommandError:
        return
    raise AssertionError("unknown command must raise CommandError")

if __name__ == "__main__":
    fails = []
    for name, fn in list(globals().items()):
        if name.startswith("test_") and callable(fn):
            try:
                fn()
                print(f"PASS {name}")
            except Exception as e:
                fails.append((name, e)); print(f"FAIL {name}: {e}")
    if fails:
        print(f"\n{len(fails)} FAILED"); sys.exit(1)
    print("\nALL PASS"); sys.exit(0)

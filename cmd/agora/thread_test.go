package main

import "testing"

func TestCwdThreadID(t *testing.T) {
	a := cwdThreadID("/home/operator/carried-world")
	b := cwdThreadID("/home/operator/carried-world")
	c := cwdThreadID("/home/operator/agora")
	if a != b {
		t.Fatalf("not deterministic: %q vs %q", a, b)
	}
	if a == c {
		t.Fatalf("different dirs collided: both %q", a)
	}
	for _, id := range []string{a, c, cwdThreadID("/weird path/w!th sp@ces")} {
		for _, r := range id {
			ok := (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_'
			if !ok {
				t.Fatalf("unsafe char %q in thread id %q", r, id)
			}
		}
	}
	if got := cwdThreadID("/home/operator/carried-world"); got[:len("carried-world-")] != "carried-world-" {
		t.Fatalf("expected readable basename prefix, got %q", got)
	}
}

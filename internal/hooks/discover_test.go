package hooks

import "testing"

func TestLayer_Rank_Order(t *testing.T) {
	// §4.1: "Order (lowest -> highest precedence): 1. Managed, 2. Config
	// layers lowest-precedence-first (user, then project), 3. Plugin last."
	if !(LayerManaged.Rank() < LayerUser.Rank()) {
		t.Error("Managed must rank below User")
	}
	if !(LayerUser.Rank() < LayerProject.Rank()) {
		t.Error("User must rank below Project")
	}
	if !(LayerProject.Rank() < LayerPlugin.Rank()) {
		t.Error("Project must rank below Plugin")
	}
}

func TestRegisteredHandler_PositionalKey(t *testing.T) {
	rh := RegisteredHandler{
		Source:       Source{Layer: LayerProject, Path: "/repo/.agora/hooks.json"},
		Event:        EventPreToolUse,
		GroupIndex:   1,
		HandlerIndex: 2,
	}
	want := "/repo/.agora/hooks.json:PreToolUse:1:2"
	if got := rh.PositionalKey(); got != want {
		t.Errorf("PositionalKey() = %q, want %q", got, want)
	}
}

func TestRegistry_Load_DiscoveryOrder_IgnoresConfigMapIteration(t *testing.T) {
	// The event map is a Go map (ground rule 3: never rely on iteration
	// order). Build a config touching every event and confirm Load's Seq
	// assignment is deterministic across repeated runs regardless of map
	// randomization — i.e. it always matches AllEvents order.
	cfg := Config{Hooks: EventMap{
		EventStop:        {{Hooks: []Handler{{Type: HandlerCommand, Command: "stop-hook"}}}},
		EventPreToolUse:  {{Hooks: []Handler{{Type: HandlerCommand, Command: "pre-hook"}}}},
		EventPostToolUse: {{Hooks: []Handler{{Type: HandlerCommand, Command: "post-hook"}}}},
	}}
	var lastOrder []EventName
	for i := 0; i < 20; i++ {
		reg := &Registry{}
		reg.Load(Source{Layer: LayerUser, Path: "~/.agora"}, cfg)
		var order []EventName
		for _, rh := range reg.handlers {
			order = append(order, rh.Event)
		}
		if lastOrder != nil {
			if len(order) != len(lastOrder) {
				t.Fatalf("run %d: order length changed", i)
			}
			for j := range order {
				if order[j] != lastOrder[j] {
					t.Fatalf("run %d: discovery order not deterministic: %v vs %v", i, order, lastOrder)
				}
			}
		}
		lastOrder = order
	}
	// And it must match AllEvents' relative order, not config-map order.
	want := []EventName{EventPreToolUse, EventPostToolUse, EventStop}
	for i, e := range want {
		if lastOrder[i] != e {
			t.Errorf("discovery order[%d] = %s, want %s (AllEvents order)", i, lastOrder[i], e)
		}
	}
}

func TestRegistry_Load_LayerPrecedence_SeqOrder(t *testing.T) {
	// §4.1: layers loaded lowest-precedence-first assign monotonically
	// increasing Seq, so later-loaded (higher precedence) layers sort last
	// in discovery order.
	cfg := func(cmd string) Config {
		return Config{Hooks: EventMap{
			EventStop: {{Hooks: []Handler{{Type: HandlerCommand, Command: cmd}}}},
		}}
	}
	reg := &Registry{}
	reg.Load(Source{Layer: LayerManaged, Path: "managed"}, cfg("m"))
	reg.Load(Source{Layer: LayerUser, Path: "user"}, cfg("u"))
	reg.Load(Source{Layer: LayerProject, Path: "project"}, cfg("p"))
	reg.Load(Source{Layer: LayerPlugin, Path: "plugin:x"}, cfg("g"))

	matched, warnings := reg.ForEvent(EventStop, "")
	if len(warnings) != 0 {
		t.Fatalf("unexpected warnings: %v", warnings)
	}
	wantOrder := []string{"managed", "user", "project", "plugin:x"}
	if len(matched) != len(wantOrder) {
		t.Fatalf("got %d handlers, want %d", len(matched), len(wantOrder))
	}
	for i, w := range wantOrder {
		if matched[i].Source.Path != w {
			t.Errorf("handler[%d].Source.Path = %q, want %q (Seq=%d)", i, matched[i].Source.Path, w, matched[i].Seq)
		}
		if i > 0 && matched[i].Seq <= matched[i-1].Seq {
			t.Errorf("Seq not monotonically increasing across layers: %d then %d", matched[i-1].Seq, matched[i].Seq)
		}
	}
}

func TestRegistry_ForEvent_MatcherFiltering(t *testing.T) {
	cfg := Config{Hooks: EventMap{
		EventPreToolUse: {
			{Matcher: "Bash", Hooks: []Handler{{Type: HandlerCommand, Command: "only-bash"}}},
			{Matcher: "Edit|Write", Hooks: []Handler{{Type: HandlerCommand, Command: "edit-or-write"}}},
			{Matcher: "", Hooks: []Handler{{Type: HandlerCommand, Command: "always"}}},
		},
	}}
	reg := &Registry{}
	reg.Load(Source{Layer: LayerUser, Path: "u"}, cfg)

	matched, _ := reg.ForEvent(EventPreToolUse, "Bash")
	if len(matched) != 2 {
		t.Fatalf("matching against Bash: got %d handlers, want 2 (only-bash + always)", len(matched))
	}

	matched, _ = reg.ForEvent(EventPreToolUse, "Read")
	if len(matched) != 1 || matched[0].Handler.Command != "always" {
		t.Fatalf("matching against Read: got %v, want just [always]", matched)
	}
}

func TestRegistry_ForEvent_MatcherIgnoredEvent_AlwaysRuns(t *testing.T) {
	cfg := Config{Hooks: EventMap{
		EventStop: {{Matcher: "SomethingIrrelevant", Hooks: []Handler{{Type: HandlerCommand, Command: "always-stop"}}}},
	}}
	reg := &Registry{}
	reg.Load(Source{Layer: LayerUser, Path: "u"}, cfg)
	matched, _ := reg.ForEvent(EventStop, "whatever-does-not-match")
	if len(matched) != 1 {
		t.Fatalf("Stop must ignore its matcher and always run: got %d handlers", len(matched))
	}
}

func TestRegistry_ForEvent_InvalidRegexDropped(t *testing.T) {
	cfg := Config{Hooks: EventMap{
		EventPreToolUse: {{Matcher: "(unclosed", Hooks: []Handler{{Type: HandlerCommand, Command: "bad"}}}},
	}}
	reg := &Registry{}
	reg.Load(Source{Layer: LayerUser, Path: "u"}, cfg)
	matched, warnings := reg.ForEvent(EventPreToolUse, "Bash")
	if len(matched) != 0 {
		t.Fatalf("invalid regex group must be dropped, got %d matches", len(matched))
	}
	if len(warnings) != 1 {
		t.Fatalf("expected exactly one warning, got %v", warnings)
	}
}

func TestResolve_ManagedAlwaysTrustedRegardlessOfState(t *testing.T) {
	cfg := Config{Hooks: EventMap{
		EventStop: {{Hooks: []Handler{{Type: HandlerCommand, Command: "managed-policy"}}}},
	}}
	reg := &Registry{}
	reg.Load(Source{Layer: LayerManaged, Path: "managed"}, cfg)
	matched, _ := reg.ForEvent(EventStop, "")
	resolved := Resolve(matched, nil, false) // nil state map: nothing recorded anywhere.
	if len(resolved) != 1 {
		t.Fatalf("expected 1 resolved handler, got %d", len(resolved))
	}
	if !resolved[0].Runnable || resolved[0].TrustState != TrustTrusted {
		t.Errorf("managed handler must be Runnable+Trusted with no state record, got Runnable=%v TrustState=%v",
			resolved[0].Runnable, resolved[0].TrustState)
	}
}

func TestResolve_UserHandlerWithNoStateIsUntrustedAndSkipped(t *testing.T) {
	cfg := Config{Hooks: EventMap{
		EventStop: {{Hooks: []Handler{{Type: HandlerCommand, Command: "user-hook"}}}},
	}}
	reg := &Registry{}
	reg.Load(Source{Layer: LayerUser, Path: "u"}, cfg)
	matched, _ := reg.ForEvent(EventStop, "")
	resolved := Resolve(matched, map[string]HandlerState{}, false)
	if resolved[0].Runnable {
		t.Error("a fresh, never-trusted user hook must not be Runnable (fail closed)")
	}
	if resolved[0].TrustState != TrustUntrusted {
		t.Errorf("TrustState = %v, want Untrusted", resolved[0].TrustState)
	}
	// But it must still be LISTED (present in resolved), per §4.4's closing
	// sentence — it's just not runnable.
}

func TestResolve_ProjectHandlerTrustedByRecordedHash(t *testing.T) {
	cfg := Config{Hooks: EventMap{
		EventStop: {{Hooks: []Handler{{Type: HandlerCommand, Command: "project-hook"}}}},
	}}
	reg := &Registry{}
	reg.Load(Source{Layer: LayerProject, Path: "/repo/.agora/hooks.json"}, cfg)
	matched, _ := reg.ForEvent(EventStop, "")
	key := matched[0].PositionalKey()
	hash := matched[0].ContentHash()

	resolved := Resolve(matched, map[string]HandlerState{key: {Enabled: true, TrustedHash: hash}}, false)
	if !resolved[0].Runnable || resolved[0].TrustState != TrustTrusted {
		t.Errorf("expected Runnable+Trusted, got Runnable=%v TrustState=%v", resolved[0].Runnable, resolved[0].TrustState)
	}

	// Now simulate a repo edit to the command after it was trusted: content
	// hash changes, recorded trusted_hash is now stale -> Modified, fail closed.
	cfg2 := Config{Hooks: EventMap{
		EventStop: {{Hooks: []Handler{{Type: HandlerCommand, Command: "project-hook --now-with-extra-flag"}}}},
	}}
	reg2 := &Registry{}
	reg2.Load(Source{Layer: LayerProject, Path: "/repo/.agora/hooks.json"}, cfg2)
	matched2, _ := reg2.ForEvent(EventStop, "")
	resolved2 := Resolve(matched2, map[string]HandlerState{key: {Enabled: true, TrustedHash: hash}}, false)
	if resolved2[0].Runnable {
		t.Error("a modified command must not run even though a stale trusted_hash is recorded")
	}
	if resolved2[0].TrustState != TrustModified {
		t.Errorf("TrustState = %v, want Modified", resolved2[0].TrustState)
	}
}

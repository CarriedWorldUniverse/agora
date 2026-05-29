package engine

import "testing"

// extractNotifyBlocks is the strip that feeds ui.TurnDone{FinalText,
// HadNotify}: cleaned → FinalText, len(notifications)>0 → HadNotify.
// These cases pin the exact inputs the UI reconcile relies on.

func TestExtractNotifyBlocks_StripsFenceFromMixedReply(t *testing.T) {
	raw := "Here's the plan.\n\n```notify-operator\nheads up: risky\n```"
	notifications, cleaned := extractNotifyBlocks(raw)
	if len(notifications) != 1 {
		t.Fatalf("notifications: want 1 got %d (%v)", len(notifications), notifications)
	}
	if notifications[0] != "heads up: risky" {
		t.Fatalf("notification body: want %q got %q", "heads up: risky", notifications[0])
	}
	if cleaned != "Here's the plan." {
		t.Fatalf("cleaned: want %q got %q", "Here's the plan.", cleaned)
	}
}

func TestExtractNotifyBlocks_PureNotifyYieldsEmptyCleaned(t *testing.T) {
	raw := "```notify-operator\nonly this\n```"
	notifications, cleaned := extractNotifyBlocks(raw)
	if len(notifications) != 1 || notifications[0] != "only this" {
		t.Fatalf("notifications: want [only this] got %v", notifications)
	}
	if cleaned != "" {
		t.Fatalf("cleaned: want empty got %q", cleaned)
	}
}

func TestExtractNotifyBlocks_NoFenceLeavesTextUntouched(t *testing.T) {
	raw := "Just a normal reply."
	notifications, cleaned := extractNotifyBlocks(raw)
	if notifications != nil {
		t.Fatalf("notifications: want nil got %v", notifications)
	}
	if cleaned != raw {
		t.Fatalf("cleaned: want %q got %q", raw, cleaned)
	}
}

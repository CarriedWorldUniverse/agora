package tui

import (
	"errors"
	"testing"

	"github.com/CarriedWorldUniverse/agora/contracts"
)

func TestApprovalModalOptions_PermissionKinds(t *testing.T) {
	for _, kind := range []contracts.ApprovalKind{
		contracts.KindExec, contracts.KindPatch, contracts.KindEscalation,
		contracts.KindMCPTool, contracts.KindGate,
	} {
		opts := ApprovalModalOptions(kind)
		if len(opts) != 3 {
			t.Fatalf("kind %s: got %d options, want 3", kind, len(opts))
		}
		if opts[0].Decision != contracts.DecisionAllow || opts[0].Scope != contracts.ScopeOnce {
			t.Errorf("kind %s: option 0 = %+v, want allow/once", kind, opts[0])
		}
		if opts[1].Decision != contracts.DecisionAllow || opts[1].Scope != contracts.ScopeSession {
			t.Errorf("kind %s: option 1 = %+v, want allow/session", kind, opts[1])
		}
		if opts[2].Decision != contracts.DecisionDeny || !opts[2].RequiresMessage {
			t.Errorf("kind %s: option 2 = %+v, want deny+RequiresMessage", kind, opts[2])
		}
	}
}

func TestApprovalModalOptions_NonPermissionKindsReturnNil(t *testing.T) {
	for _, kind := range []contracts.ApprovalKind{contracts.KindQuestion, contracts.KindPlan} {
		if got := ApprovalModalOptions(kind); got != nil {
			t.Errorf("kind %s: got %v, want nil (has its own option builder)", kind, got)
		}
	}
}

func TestResolveApproval_EachOptionMapsToExactTriple(t *testing.T) {
	cases := []struct {
		name      string
		option    ModalOption
		message   string
		wantDec   contracts.Decision
		wantScope contracts.Scope
		wantErr   error
	}{
		{"approve once", ApprovalModalOptions(contracts.KindExec)[0], "", contracts.DecisionAllow, contracts.ScopeOnce, nil},
		{"approve session", ApprovalModalOptions(contracts.KindExec)[1], "", contracts.DecisionAllow, contracts.ScopeSession, nil},
		{"deny with feedback", ApprovalModalOptions(contracts.KindExec)[2], "use a different flag", contracts.DecisionDeny, contracts.ScopeOnce, nil},
		{"deny without feedback rejected", ApprovalModalOptions(contracts.KindExec)[2], "", "", "", ErrMessageRequired},
		{"esc", EscDecision(), "", contracts.DecisionDeny, contracts.ScopeOnce, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			in, err := ResolveApproval("req-1", c.option, c.message)
			if c.wantErr != nil {
				if !errors.Is(err, c.wantErr) {
					t.Fatalf("err = %v, want %v", err, c.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected err: %v", err)
			}
			if in.Type != contracts.InApprovalResponse {
				t.Errorf("Type = %v, want InApprovalResponse", in.Type)
			}
			if in.ID != "req-1" {
				t.Errorf("ID = %v, want req-1", in.ID)
			}
			if in.Decision != c.wantDec {
				t.Errorf("Decision = %v, want %v", in.Decision, c.wantDec)
			}
			if in.Scope != c.wantScope {
				t.Errorf("Scope = %v, want %v", in.Scope, c.wantScope)
			}
			if in.Message != c.message {
				t.Errorf("Message = %q, want %q", in.Message, c.message)
			}
		})
	}
}

func TestBuildQuestionAnswer(t *testing.T) {
	optsArgs := contracts.QuestionArgs{
		Text:    "which approach?",
		Options: []contracts.QuestionOption{{Label: "A"}, {Label: "B"}},
	}
	multiArgs := optsArgs
	multiArgs.MultiSelect = true

	t.Run("single selection", func(t *testing.T) {
		in, err := BuildQuestionAnswer("q-1", optsArgs, QuestionCardChoice{Selected: []string{"A"}})
		if err != nil {
			t.Fatalf("unexpected err: %v", err)
		}
		if in.Type != contracts.InQuestionResponse || in.ID != "q-1" {
			t.Fatalf("in = %+v", in)
		}
		if in.Answer == nil || len(in.Answer.Choice) != 1 || in.Answer.Choice[0] != "A" {
			t.Fatalf("Answer = %+v", in.Answer)
		}
	})

	t.Run("multi-select requires MultiSelect", func(t *testing.T) {
		_, err := BuildQuestionAnswer("q-1", optsArgs, QuestionCardChoice{Selected: []string{"A", "B"}})
		if !errors.Is(err, ErrTooManySelections) {
			t.Fatalf("err = %v, want ErrTooManySelections", err)
		}
		in, err := BuildQuestionAnswer("q-1", multiArgs, QuestionCardChoice{Selected: []string{"A", "B"}})
		if err != nil {
			t.Fatalf("unexpected err: %v", err)
		}
		if len(in.Answer.Choice) != 2 {
			t.Fatalf("Choice = %v", in.Answer.Choice)
		}
	})

	t.Run("free text only", func(t *testing.T) {
		in, err := BuildQuestionAnswer("q-1", contracts.QuestionArgs{FreeText: true}, QuestionCardChoice{FreeText: "42"})
		if err != nil {
			t.Fatalf("unexpected err: %v", err)
		}
		if in.Answer.Text != "42" {
			t.Fatalf("Text = %q", in.Answer.Text)
		}
	})

	t.Run("no answer rejected", func(t *testing.T) {
		_, err := BuildQuestionAnswer("q-1", optsArgs, QuestionCardChoice{})
		if !errors.Is(err, ErrNoAnswerGiven) {
			t.Fatalf("err = %v, want ErrNoAnswerGiven", err)
		}
	})
}

func TestEscQuestionAnswer(t *testing.T) {
	in := EscQuestionAnswer("q-9")
	if in.Type != contracts.InQuestionResponse || in.ID != "q-9" {
		t.Fatalf("in = %+v", in)
	}
	if in.Answer == nil || in.Answer.Text != declinedAnswerText {
		t.Fatalf("Answer = %+v", in.Answer)
	}
}

func TestPlanModalOptions_AllowDisabledWhileQuestionsOpen(t *testing.T) {
	opts := PlanModalOptions([]string{"q-1", "q-2"})
	if !opts[0].Disabled {
		t.Fatalf("allow option should be disabled while questions remain open: %+v", opts[0])
	}
	if opts[0].DisabledWhy == "" {
		t.Fatalf("DisabledWhy should explain why")
	}

	_, err := ResolvePlan("plan-1", opts[0], "")
	if err == nil {
		t.Fatalf("ResolvePlan should refuse a disabled option")
	}
}

func TestPlanModalOptions_AllowEnabledWhenNoOpenQuestions(t *testing.T) {
	opts := PlanModalOptions(nil)
	if opts[0].Disabled {
		t.Fatalf("allow option should be enabled with no open questions: %+v", opts[0])
	}
	in, err := ResolvePlan("plan-1", opts[0], "")
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if in.Decision != contracts.DecisionAllow || in.Scope != contracts.ScopeOnce {
		t.Fatalf("in = %+v", in)
	}
}

func TestPlanModalOptions_DenyRequiresMessage(t *testing.T) {
	opts := PlanModalOptions(nil)
	deny := opts[1]
	if !deny.RequiresMessage {
		t.Fatalf("plan deny option should require a message")
	}
	_, err := ResolvePlan("plan-1", deny, "")
	if !errors.Is(err, ErrMessageRequired) {
		t.Fatalf("err = %v, want ErrMessageRequired", err)
	}
}

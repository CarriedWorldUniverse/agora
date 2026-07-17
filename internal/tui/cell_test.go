package tui

import "testing"

func TestCell_Render_SessionHeader(t *testing.T) {
	c := Cell{Kind: CellSessionHeader, AgentID: "anvil-builder", Model: "frontier:high"}
	assertGolden(t, "cell_session_header", c.Render(60, PlainTheme()))
}

func TestCell_Render_UserMessage(t *testing.T) {
	c := Cell{Kind: CellUserMessage, Text: "fix the race condition\nin the scheduler"}
	assertGolden(t, "cell_user_message", c.Render(60, PlainTheme()))
}

func TestCell_Render_AgentMessage_Markdown(t *testing.T) {
	c := Cell{Kind: CellAgentMessage, Text: "# Done\n\nFixed it by adding a mutex.\n\n- item one\n- item two\n"}
	assertGolden(t, "cell_agent_message", c.Render(60, PlainTheme()))
}

func TestCell_Render_Reasoning_Collapsed(t *testing.T) {
	c := Cell{Kind: CellReasoning, Text: "considering three approaches\nweighing tradeoffs"}
	assertGolden(t, "cell_reasoning_collapsed", c.Render(60, PlainTheme()))
}

func TestCell_Render_Reasoning_Expanded(t *testing.T) {
	c := Cell{Kind: CellReasoning, Expanded: true, Text: "line one\nline two"}
	assertGolden(t, "cell_reasoning_expanded", c.Render(60, PlainTheme()))
}

func TestCell_Render_Exec_Active(t *testing.T) {
	c := Cell{Kind: CellExec, Command: "go test ./...", Output: []string{"ok internal/tui", "PASS"}}
	assertGolden(t, "cell_exec_active", c.Render(60, PlainTheme()))
}

func TestCell_Render_Exec_Done(t *testing.T) {
	c := Cell{Kind: CellExec, Command: "go build ./...", ExecDone: true, ExitCode: 0, Output: []string{"done"}}
	assertGolden(t, "cell_exec_done", c.Render(60, PlainTheme()))
}

func TestCell_Render_ApprovalDecision(t *testing.T) {
	c := Cell{Kind: CellApprovalDecision, DecisionLabel: "approved once: rm build/tmp"}
	assertGolden(t, "cell_approval_decision", c.Render(60, PlainTheme()))
}

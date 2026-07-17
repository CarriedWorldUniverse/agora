package subagent

import "fmt"

// Trigger identifies which of the spec §2a cancellation rules applies.
// Spec: agora-spec-subagents.md §2a.
type Trigger string

const (
	// TriggerTurnInterrupt: "Interrupt (Esc / interrupt message) cancels
	// the parent's in-flight sampling and any FOREGROUND child waits
	// (run_in_background: false spawns are cancelled with the turn — the
	// parent was blocked on them). Background children keep running and
	// deliver their completion notifications normally." Non-recursive:
	// only the interrupted turn's own direct foreground children.
	TriggerTurnInterrupt Trigger = "turn_interrupt"
	// TriggerWorkflowStop: "Workflow runs are owned by the run, not the
	// invoking turn: parent interrupt never kills a background workflow;
	// `workflow stop|pause <run_id>` is the explicit verb. Stopping a run
	// cancels its in-flight agents." Recursive: the full open subtree
	// rooted at the run, regardless of foreground/background.
	TriggerWorkflowStop Trigger = "workflow_stop"
	// TriggerThreadTeardown: "Thread archive/delete and pod deprovision
	// cancel everything downstream: BFS over the agent graph (open edges),
	// plus any workflow runs rooted in the thread." Recursive: the full
	// open subtree, regardless of foreground/background.
	TriggerThreadTeardown Trigger = "thread_teardown"
)

// CancelResult reports one propagation's effect: which agents were
// cancelled (deterministic BFS order) and whether any were already
// finished/absent (a no-op, not an error).
type CancelResult struct {
	Trigger    Trigger
	Root       string
	Cancelled  []string // agent ids actually transitioned to interrupted
	Unaffected []string // agent ids in scope that were already finished
}

// CancelNode cancels a single running agent: aborts its context, marks it
// interrupted, and closes its graph edge (spec §2a: "child receives cancel,
// in-flight tool calls abort, transcript marked interrupted — a cancelled
// child is resumable-by-continuation like any finished agent"). Returns
// (false, nil) if the agent is not currently running (idempotent no-op:
// already finished, or unknown to this Manager).
func (m *Manager) CancelNode(agentID string) (bool, error) {
	m.mu.Lock()
	n, ok := m.nodes[agentID]
	m.mu.Unlock()
	if !ok {
		return false, fmt.Errorf("%w: %s", ErrNodeNotFound, agentID)
	}

	n.mu.Lock()
	if n.status != NodeRunning {
		n.mu.Unlock()
		return false, nil
	}
	cancel := n.cancel
	parent := n.parent
	done := n.done
	n.mu.Unlock()

	cancel() // aborts the in-flight AgentRunner.Run via ctx
	// Wait for the run to actually observe the cancellation and finish —
	// its own completion path sets status to NodeInterrupted (it sees
	// runCtx.Err() != nil). Blocking here (rather than flipping status
	// ourselves) is what makes "no orphaned running node" true by
	// construction: CancelNode never returns while the node is still
	// mid-flight, and it can never race a subsequent Continue reassigning
	// n.done out from under this attempt's completion (manager.go's myDone
	// capture note).
	<-done

	// Closing the edge is best-effort relative to the run's own status
	// flip above: node run-state is the source of truth for "is it still
	// running"; edge status is the graph-shape view ("does this subtree
	// still exist"). Spec §3: "a closed edge hides its subtree".
	if err := m.graph.CloseEdge(parent, agentID); err != nil {
		return true, fmt.Errorf("subagent: close edge after cancel: %w", err)
	}
	return true, nil
}

// CancelPropagate applies the §2a cancellation matrix rooted at root,
// dispatching by trigger. root is a thread id: for TriggerTurnInterrupt it
// is the interrupted turn's owning thread (the parent whose direct
// foreground children are cancelled); for TriggerWorkflowStop/
// TriggerThreadTeardown it is the subtree root to cancel entirely.
func (m *Manager) CancelPropagate(trigger Trigger, root string) (CancelResult, error) {
	switch trigger {
	case TriggerTurnInterrupt:
		return m.cancelDirectForeground(trigger, root)
	case TriggerWorkflowStop, TriggerThreadTeardown:
		return m.cancelSubtree(trigger, root)
	default:
		return CancelResult{}, fmt.Errorf("subagent: unknown cancellation trigger %q", trigger)
	}
}

func (m *Manager) cancelDirectForeground(trigger Trigger, root string) (CancelResult, error) {
	edges, err := m.graph.Children(root, true)
	if err != nil {
		return CancelResult{}, err
	}
	res := CancelResult{Trigger: trigger, Root: root}
	for _, e := range edges {
		if !e.Foreground {
			continue // background children keep running (spec §2a)
		}
		cancelled, err := m.CancelNode(e.ChildThread)
		if err != nil {
			return res, err
		}
		if cancelled {
			res.Cancelled = append(res.Cancelled, e.ChildThread)
		} else {
			res.Unaffected = append(res.Unaffected, e.ChildThread)
		}
	}
	return res, nil
}

func (m *Manager) cancelSubtree(trigger Trigger, root string) (CancelResult, error) {
	edges, err := m.graph.Descendants(root, true)
	if err != nil {
		return CancelResult{}, err
	}
	res := CancelResult{Trigger: trigger, Root: root}
	for _, e := range edges {
		cancelled, err := m.CancelNode(e.ChildThread)
		if err != nil {
			return res, err
		}
		if cancelled {
			res.Cancelled = append(res.Cancelled, e.ChildThread)
		} else {
			res.Unaffected = append(res.Unaffected, e.ChildThread)
		}
	}
	return res, nil
}

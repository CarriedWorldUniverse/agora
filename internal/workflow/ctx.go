package workflow

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sync"

	"github.com/CarriedWorldUniverse/agora/contracts"
	"go.starlark.net/starlark"
)

// ctxObj is the `ctx` receiver handed to a script's main(ctx, args) — spec
// §2: "ctx receiver keeps the host API in one namespace and
// starlark-friendly." One ctxObj is shared by every branch of a run (its
// methods dispatch through runState, which is the concurrency-safe part);
// ctxObj itself holds no mutable state.
type ctxObj struct {
	rs     *runState
	args   starlark.Value
	now    starlark.Value
	budget *budgetObj
}

var (
	_ starlark.Value    = (*ctxObj)(nil)
	_ starlark.HasAttrs = (*ctxObj)(nil)
)

func (c *ctxObj) String() string        { return "<workflow ctx>" }
func (c *ctxObj) Type() string          { return "ctx" }
func (c *ctxObj) Freeze()               {} // opaque Go state, guarded by rs.mu — see doc.go
func (c *ctxObj) Truth() starlark.Bool  { return starlark.True }
func (c *ctxObj) Hash() (uint32, error) { return 0, fmt.Errorf("unhashable type: ctx") }

var ctxAttrNames = []string{
	"agent", "parallel", "pipeline", "log", "phase",
	"question", "approval", "workflow", "budget", "args", "now",
}

func (c *ctxObj) AttrNames() []string { return ctxAttrNames }

func (c *ctxObj) Attr(name string) (starlark.Value, error) {
	switch name {
	case "agent":
		return starlark.NewBuiltin("ctx.agent", c.agentBuiltin), nil
	case "parallel":
		return starlark.NewBuiltin("ctx.parallel", c.parallelBuiltin), nil
	case "pipeline":
		return starlark.NewBuiltin("ctx.pipeline", c.pipelineBuiltin), nil
	case "log":
		return starlark.NewBuiltin("ctx.log", c.logBuiltin), nil
	case "phase":
		return starlark.NewBuiltin("ctx.phase", c.phaseBuiltin), nil
	case "question":
		return starlark.NewBuiltin("ctx.question", c.questionBuiltin), nil
	case "approval":
		return starlark.NewBuiltin("ctx.approval", c.approvalBuiltin), nil
	case "workflow":
		return starlark.NewBuiltin("ctx.workflow", c.workflowBuiltin), nil
	case "budget":
		return c.budget, nil
	case "args":
		return c.args, nil
	case "now":
		return c.now, nil
	}
	return nil, nil
}

// agentBuiltin implements ctx.agent(...) — spec §2, §2a.
func (c *ctxObj) agentBuiltin(thread *starlark.Thread, _ *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	var (
		prompt                                          string
		label, phaseName, agentType, model, effort, iso string
		schema                                          starlark.Value = starlark.None
	)
	if err := starlark.UnpackArgs("agent", args, kwargs,
		"prompt", &prompt,
		"label?", &label,
		"phase?", &phaseName,
		"agent_type?", &agentType,
		"model?", &model,
		"effort?", &effort,
		"schema?", &schema,
		"isolation?", &iso,
	); err != nil {
		return nil, err
	}

	rs := c.rs
	branch := currentBranch(thread)
	localSeq := branch.seq
	branch.seq++

	// §2a resolution order: explicit call arg > phase default > (the rest
	// happens inside AgentInvoker/subagent.ResolveInheritance: agent-def >
	// parent-inherited). Resolved independently for model and effort.
	if model == "" {
		if p, ok := rs.meta.phase(phaseName); ok {
			model = p.Model
		}
	}
	if effort == "" {
		if p, ok := rs.meta.phase(phaseName); ok {
			effort = string(p.Effort)
		}
	}

	var schemaJSON json.RawMessage
	var schemaCanon any
	if schema != starlark.None {
		goVal, err := toGo(schema)
		if err != nil {
			return nil, fmt.Errorf("workflow: agent() schema: %w", err)
		}
		b, err := canonicalJSON(goVal)
		if err != nil {
			return nil, fmt.Errorf("workflow: agent() schema: %w", err)
		}
		schemaJSON = b
		schemaCanon = string(b)
	}

	opts := AgentCallOpts{
		Label: label, Phase: phaseName, AgentType: agentType,
		Model: model, Effort: contracts.Effort(effort),
		Schema: schemaJSON, Isolation: iso,
	}

	hash, err := contentHash(map[string]any{
		"prompt": prompt, "label": label, "phase": phaseName, "agent_type": agentType,
		"model": model, "effort": effort, "schema": schemaCanon, "isolation": iso,
	})
	if err != nil {
		return nil, fmt.Errorf("workflow: hash agent call: %w", err)
	}

	if cached, ok := rs.tryReplay(branch.path, localSeq, EntryAgent, hash); ok {
		if err := rs.record(cached); err != nil {
			return nil, err
		}
		if cached.ResultErr != "" {
			return nil, errors.New(cached.ResultErr)
		}
		goVal, err := jsonRawToGo(cached.Result)
		if err != nil {
			return nil, fmt.Errorf("workflow: decode cached agent result: %w", err)
		}
		if goVal == nil {
			return starlark.None, nil
		}
		return toStarlark(goVal)
	}

	if err := rs.beginAgentCall(); err != nil {
		return nil, err
	}
	result, invokeErr := rs.invoker.InvokeAgent(rs.goCtx, prompt, opts)
	rs.endAgentCall()

	if invokeErr != nil {
		if err := rs.record(Entry{Branch: branch.path, LocalSeq: localSeq, Kind: EntryAgent, Hash: hash, ResultErr: invokeErr.Error()}); err != nil {
			return nil, err
		}
		return nil, invokeErr
	}

	if result.Question != nil {
		// Bubbled question (spec §2: "Questions raised by agents inside a
		// stage bubble to the engine first ... else the run parks"). v1
		// scope note (see the build report): this does not inject the
		// eventual answer back into the child as a real continuation — a
		// resume after it's answered re-invokes the agent fresh rather
		// than continuing the parked child. That's a documented gap, not
		// an oversight; see subagent.Manager.Continue for the primitive a
		// future unit would wire this through.
		ans, askErr := rs.askOrApprove(EntryQuestion, branch.path, localSeq, result.Question.Args, contracts.QuestionFromAgent)
		if askErr != nil {
			return nil, askErr
		}
		_ = ans
		if err := rs.record(Entry{Branch: branch.path, LocalSeq: localSeq, Kind: EntryAgent, Hash: hash}); err != nil {
			return nil, err
		}
		return starlark.None, nil
	}

	if err := rs.record(Entry{Branch: branch.path, LocalSeq: localSeq, Kind: EntryAgent, Hash: hash, Result: result.Output}); err != nil {
		return nil, err
	}
	if len(result.Output) == 0 {
		return starlark.None, nil
	}
	goVal, err := jsonRawToGo(result.Output)
	if err != nil {
		return nil, fmt.Errorf("workflow: decode agent result: %w", err)
	}
	return toStarlark(goVal)
}

// parallelBuiltin implements ctx.parallel(thunks) — spec §2: "run
// concurrently, barrier: await all. A failed thunk yields None in the
// result list; the call never aborts the sibling thunks."
func (c *ctxObj) parallelBuiltin(thread *starlark.Thread, _ *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	var thunks *starlark.List
	if err := starlark.UnpackArgs("parallel", args, kwargs, "thunks", &thunks); err != nil {
		return nil, err
	}
	items := make([]starlark.Value, 0, thunks.Len())
	for e := range thunks.Elements() {
		items = append(items, e)
	}
	if len(items) > c.rs.perCallItemCap {
		return nil, fmt.Errorf("%w: %d > %d", ErrItemCapExceeded, len(items), c.rs.perCallItemCap)
	}

	branch := currentBranch(thread)
	mySeq := branch.seq
	branch.seq++
	prefix := fmt.Sprintf("%s/p%d", branch.path, mySeq)

	results := make([]starlark.Value, len(items))
	errs := make([]error, len(items))
	var wg sync.WaitGroup
	for i, thunk := range items {
		thunk.Freeze()
		wg.Add(1)
		go func(i int, thunk starlark.Value) {
			defer wg.Done()
			childThread := &starlark.Thread{Name: fmt.Sprintf("wf-parallel:%s.%d", prefix, i)}
			childThread.SetLocal(branchLocalKey, &branchState{path: fmt.Sprintf("%s.%d", prefix, i)})
			v, err := starlark.Call(childThread, thunk, nil, nil)
			results[i] = v
			errs[i] = err
		}(i, thunk)
	}
	wg.Wait()

	for _, err := range errs {
		var pe *errParked
		if err != nil && errors.As(err, &pe) {
			return nil, err
		}
	}

	out := make([]starlark.Value, len(items))
	for i := range items {
		if errs[i] != nil || results[i] == nil {
			out[i] = starlark.None
			continue
		}
		out[i] = results[i]
	}
	return starlark.NewList(out), nil
}

// pipelineBuiltin implements ctx.pipeline(items, *stages) — spec §2: "each
// item flows through all stages independently, no barrier between stages
// ... Default to pipeline; use parallel only when a stage genuinely needs
// all prior results."
func (c *ctxObj) pipelineBuiltin(thread *starlark.Thread, _ *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	if len(kwargs) > 0 {
		return nil, fmt.Errorf("workflow: pipeline() takes no keyword arguments")
	}
	if len(args) < 2 {
		return nil, fmt.Errorf("workflow: pipeline() requires items and at least one stage")
	}
	itemsList, ok := args[0].(*starlark.List)
	if !ok {
		return nil, fmt.Errorf("workflow: pipeline() first argument must be a list, got %s", args[0].Type())
	}
	stages := args[1:]
	for i, s := range stages {
		if _, ok := s.(starlark.Callable); !ok {
			return nil, fmt.Errorf("workflow: pipeline() stage %d is not callable", i)
		}
	}

	items := make([]starlark.Value, 0, itemsList.Len())
	for e := range itemsList.Elements() {
		items = append(items, e)
	}
	if len(items) > c.rs.perCallItemCap {
		return nil, fmt.Errorf("%w: %d > %d", ErrItemCapExceeded, len(items), c.rs.perCallItemCap)
	}

	branch := currentBranch(thread)
	mySeq := branch.seq
	branch.seq++
	prefix := fmt.Sprintf("%s/pl%d", branch.path, mySeq)

	for _, s := range stages {
		s.Freeze()
	}
	for _, it := range items {
		it.Freeze()
	}

	results := make([]starlark.Value, len(items))
	parkedErrs := make([]error, len(items))
	var wg sync.WaitGroup
	for i, original := range items {
		wg.Add(1)
		go func(i int, original starlark.Value) {
			defer wg.Done()
			childThread := &starlark.Thread{Name: fmt.Sprintf("wf-pipeline:%s.%d", prefix, i)}
			childThread.SetLocal(branchLocalKey, &branchState{path: fmt.Sprintf("%s.%d", prefix, i)})
			idx := starlark.MakeInt(i)
			prev := original
			for _, stage := range stages {
				v, err := starlark.Call(childThread, stage, starlark.Tuple{prev, original, idx}, nil)
				if err != nil {
					var pe *errParked
					if errors.As(err, &pe) {
						parkedErrs[i] = err
						return
					}
					// spec: "A stage error drops that item to None and
					// skips its remaining stages."
					prev = starlark.None
					break
				}
				prev = v
			}
			results[i] = prev
		}(i, original)
	}
	wg.Wait()

	for _, err := range parkedErrs {
		if err != nil {
			return nil, err
		}
	}

	out := make([]starlark.Value, len(items))
	for i := range items {
		if results[i] == nil {
			out[i] = starlark.None
			continue
		}
		out[i] = results[i]
	}
	return starlark.NewList(out), nil
}

func (c *ctxObj) logBuiltin(thread *starlark.Thread, _ *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	var msg string
	if err := starlark.UnpackArgs("log", args, kwargs, "msg", &msg); err != nil {
		return nil, err
	}
	branch := currentBranch(thread)
	if err := c.rs.record(Entry{Branch: branch.path, LocalSeq: branch.seq, Kind: EntryLog, Message: msg}); err != nil {
		return nil, err
	}
	return starlark.None, nil
}

func (c *ctxObj) phaseBuiltin(thread *starlark.Thread, _ *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	var title string
	if err := starlark.UnpackArgs("phase", args, kwargs, "title", &title); err != nil {
		return nil, err
	}
	branch := currentBranch(thread)
	if err := c.rs.record(Entry{Branch: branch.path, LocalSeq: branch.seq, Kind: EntryPhase, Phase: title}); err != nil {
		return nil, err
	}
	return starlark.None, nil
}

func (c *ctxObj) questionBuiltin(thread *starlark.Thread, _ *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	var payload starlark.Value = starlark.None
	if err := starlark.UnpackArgs("question", args, kwargs, "payload", &payload); err != nil {
		return nil, err
	}
	ap, err := decodeAskPayload(payload)
	if err != nil {
		return nil, err
	}

	branch := currentBranch(thread)
	localSeq := branch.seq
	branch.seq++

	ans, err := c.rs.askOrApprove(EntryQuestion, branch.path, localSeq, ap.toQuestionArgs(), contracts.QuestionFromWorkflow)
	if err != nil {
		return nil, err
	}
	return answerToStarlark(ans)
}

func (c *ctxObj) approvalBuiltin(thread *starlark.Thread, _ *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	var (
		msg     string
		payload starlark.Value = starlark.None
	)
	if err := starlark.UnpackArgs("approval", args, kwargs, "msg", &msg, "payload?", &payload); err != nil {
		return nil, err
	}
	extra, err := decodeAskPayload(payload)
	if err != nil {
		return nil, err
	}

	branch := currentBranch(thread)
	localSeq := branch.seq
	branch.seq++

	ans, err := c.rs.askOrApprove(EntryApproval, branch.path, localSeq, approvalArgs(msg, extra), contracts.QuestionFromWorkflow)
	if err != nil {
		return nil, err
	}
	return starlark.Bool(decisionFromAnswer(ans)), nil
}

// workflowBuiltin: ctx.workflow(name, args) — nested workflow invocation is
// explicitly deferred by spec §7's v1 sizing ("Defer: ... nested
// workflow()"). Implemented as an honest error rather than a silent no-op
// or a partial/unsafe attempt at nesting.
func (c *ctxObj) workflowBuiltin(*starlark.Thread, *starlark.Builtin, starlark.Tuple, []starlark.Tuple) (starlark.Value, error) {
	return nil, fmt.Errorf("workflow: ctx.workflow (nested workflow invocation) is deferred past v1 (spec §7) — not implemented")
}

func decodeAskPayload(v starlark.Value) (askPayload, error) {
	if v == nil || v == starlark.None {
		return askPayload{}, nil
	}
	goVal, err := toGo(v)
	if err != nil {
		return askPayload{}, fmt.Errorf("workflow: question/approval payload: %w", err)
	}
	b, err := json.Marshal(goVal)
	if err != nil {
		return askPayload{}, fmt.Errorf("workflow: question/approval payload: %w", err)
	}
	var ap askPayload
	if err := json.Unmarshal(b, &ap); err != nil {
		return askPayload{}, fmt.Errorf("workflow: decode question/approval payload: %w", err)
	}
	return ap, nil
}

func answerToStarlark(ans contracts.Answer) (starlark.Value, error) {
	choice := make([]any, len(ans.Choice))
	for i, c := range ans.Choice {
		choice[i] = c
	}
	return toStarlark(map[string]any{
		"text":   ans.Text,
		"choice": choice,
		"by":     ans.By,
	})
}

// budgetObj is ctx.budget — spec §7 explicitly defers "budget directives"
// past v1, so this is a stub: total is always 0 ("unset"/unbounded),
// spent() always 0, remaining() effectively infinite. No enforcement
// happens anywhere in the engine. A future unit that wires real per-model
// pricing (spec §2a: "spent() counts real per-model cost ... once bridle
// exposes pricing") replaces this type's internals without changing
// ctxObj's shape.
type budgetObj struct{}

var (
	_ starlark.Value    = (*budgetObj)(nil)
	_ starlark.HasAttrs = (*budgetObj)(nil)
)

func (b *budgetObj) String() string        { return "<budget>" }
func (b *budgetObj) Type() string          { return "budget" }
func (b *budgetObj) Freeze()               {}
func (b *budgetObj) Truth() starlark.Bool  { return starlark.True }
func (b *budgetObj) Hash() (uint32, error) { return 0, fmt.Errorf("unhashable type: budget") }

var budgetAttrNames = []string{"total", "spent", "remaining"}

func (b *budgetObj) AttrNames() []string { return budgetAttrNames }

func (b *budgetObj) Attr(name string) (starlark.Value, error) {
	switch name {
	case "total":
		return starlark.MakeInt(0), nil
	case "spent":
		return starlark.NewBuiltin("budget.spent", func(*starlark.Thread, *starlark.Builtin, starlark.Tuple, []starlark.Tuple) (starlark.Value, error) {
			return starlark.MakeInt(0), nil
		}), nil
	case "remaining":
		return starlark.NewBuiltin("budget.remaining", func(*starlark.Thread, *starlark.Builtin, starlark.Tuple, []starlark.Tuple) (starlark.Value, error) {
			return starlark.MakeInt64(math.MaxInt64), nil
		}), nil
	}
	return nil, nil
}

# Terminal-Bench 2.0 cells (Harbor)

Puts agora's context-curation ContextManager (internal/ctxmgr — the
bridle/wset v2 algorithm) on Terminal-Bench 2.0 with a same-model ablation.
Campaign design + operator decisions: memory `project_tbench_wset_campaign`.

## Build the agent binary

```sh
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o bench/harbor/tbagent ./cmd/tbagent
```

## Run a cell (from the repo root, so `bench.harbor...` imports)

```sh
export TBAGENT_BINARY=$PWD/bench/harbor/tbagent
export TB_BASE_URL=http://<litellm-as-seen-from-containers>/v1
export TB_CURATION=true          # true = wset cell, false = bare cell

harbor run -d terminal-bench@2.0 \
  -a bench.harbor.tbagent_agent:TBAgent \
  -m openai/qwen3.6 \
  --n-attempts 5 -n 4 --jobs-dir jobs/wset-qwen36
```

The third cell (community baseline) needs no wrapper:

```sh
harbor run -d terminal-bench@2.0 -a terminus-2 -m openai/qwen3.6 ...
```

Submission hygiene (even though nothing is published without an operator
decision): 5 trials minimum, never override timeouts or task resources, keep
the full jobs dir including trajectories.

## Plumbing notes

- The wrapper uploads `TBAGENT_BINARY` into the container via
  `environment.upload_file` — no registry, no network fetch.
- `TB_BASE_URL` must be reachable **from inside task containers** (Docker
  NAT egress via the host; verify during smoke, fall back to a host
  port-forward on the docker bridge IP if tailnet routing doesn't carry).
- tbagent writes `trajectory.jsonl` + `metrics.json` to `/logs/agent`;
  Harbor syncs that to the host logs dir where the wrapper backfills
  AgentContext (input includes cache reads, pi.py convention).

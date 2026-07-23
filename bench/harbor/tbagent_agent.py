"""Harbor (Terminal-Bench 2.0) wrapper for agora's headless tbagent.

Installed-agent pattern: the statically linked linux-amd64 ``tbagent`` binary
(built from cmd/tbagent, CGO_ENABLED=0) is uploaded into the task container
and launched headlessly with the task instruction; it drives one
turnengine.Manager turn against an OpenAI-compatible backend and writes
trajectory.jsonl + metrics.json under /logs/agent, which Harbor syncs back to
the host where ``populate_context_post_run`` parses them.

Host-side configuration (environment variables, read at run time):
  TBAGENT_BINARY  path to the tbagent binary on the HOST (required for setup)
  TB_BASE_URL     OpenAI-compatible endpoint as reachable FROM THE CONTAINER
                  (required; e.g. a LiteLLM gateway)
  TB_API_KEY      bearer key for that endpoint (optional; "dummy" default)
  TB_CURATION     "true"/"false" — the ctxmgr context-curation ablation
                  switch (default true). This is the ONLY difference between
                  the bridle-wset and bridle-bare benchmark cells.

Model selection comes from Harbor's ``-m`` flag as ``openai/<model>``; the
part after the slash is passed to tbagent as TB_MODEL.

Invocation:
  harbor run -d terminal-bench@2.0 \
    -a bench.harbor.tbagent_agent:TBAgent -m openai/qwen3.6 ...
"""

import base64
import json
import os
import shlex
from pathlib import Path
from typing import override

from harbor.agents.installed.base import BaseInstalledAgent
from harbor.environments.base import BaseEnvironment
from harbor.models.agent.context import AgentContext

_CONTAINER_BINARY = "/usr/local/bin/tbagent"
_CONTAINER_LOGS = "/logs/agent"
_OUTPUT_FILENAME = "tbagent.txt"


class TBAgent(BaseInstalledAgent):
    @staticmethod
    @override
    def name() -> str:
        curation = os.environ.get("TB_CURATION", "true").lower()
        return "agora-bridle-bare" if curation in ("0", "false", "off", "no") else "agora-bridle-wset"

    @override
    def get_version_command(self) -> str | None:
        return None

    @override
    def version(self) -> str | None:
        return self._version or "dev"

    @override
    async def install(self, environment: BaseEnvironment) -> None:
        binary = os.environ.get("TBAGENT_BINARY", "")
        if not binary or not Path(binary).is_file():
            raise RuntimeError(
                "TBAGENT_BINARY must point to the built linux-amd64 tbagent "
                f"binary on the host (got {binary!r})"
            )
        await environment.upload_file(binary, _CONTAINER_BINARY)
        await self.exec_as_root(
            environment,
            command=f"chmod 0755 {_CONTAINER_BINARY} && {_CONTAINER_BINARY} -h 2>&1 | head -1",
        )

    @override
    async def run(
        self,
        instruction: str,
        environment: BaseEnvironment,
        context: AgentContext,
    ) -> None:
        base_url = os.environ.get("TB_BASE_URL", "")
        if not base_url:
            raise RuntimeError("TB_BASE_URL is required (endpoint reachable from inside the container)")
        if not self.model_name:
            raise RuntimeError("model name is required (harbor -m openai/<model>)")
        model = self._parsed_model_name or self.model_name
        curation = os.environ.get("TB_CURATION", "true")

        env = {
            "TB_MODEL": model,
            "TB_BASE_URL": base_url,
            "TB_API_KEY": os.environ.get("TB_API_KEY", "dummy"),
            "TB_CURATION": curation,
        }

        instr_b64 = base64.b64encode(
            self.render_instruction(instruction).encode()
        ).decode()

        await self.exec_as_agent(
            environment,
            command=(
                f"mkdir -p {_CONTAINER_LOGS} && "
                f"{_CONTAINER_BINARY} "
                f"-instruction-b64 {shlex.quote(instr_b64)} "
                f"-workdir / "
                f"-out {_CONTAINER_LOGS} "
                f"-curation={shlex.quote(curation)} "
                f"2>&1 </dev/null | tee {_CONTAINER_LOGS}/{_OUTPUT_FILENAME}"
            ),
            env=env,
        )

    @override
    def populate_context_post_run(self, context: AgentContext) -> None:
        metrics_file = self.logs_dir / "metrics.json"
        if not metrics_file.exists():
            return
        try:
            m = json.loads(metrics_file.read_text())
        except (json.JSONDecodeError, OSError):
            return
        # Same accounting convention as harbor's pi.py wrapper: input counts
        # include cache reads; cost only when the provider reported one.
        n_input = int(m.get("input_tokens", 0))
        n_cached = int(m.get("cached_tokens", 0))
        context.n_input_tokens = n_input + n_cached
        context.n_output_tokens = int(m.get("output_tokens", 0))
        context.n_cache_tokens = n_cached
        cost = float(m.get("cost_usd", 0.0))
        context.cost_usd = cost if cost > 0 else None

meta = workflow_meta(
    name = "two-agent-demo",
    description = "two chained ctx.agent calls, for the CLI's resume e2e test",
)

def main(ctx, args):
    a = ctx.agent("step1:" + args["seed"], label = "s1")
    b = ctx.agent("step2:" + a, label = "s2")
    return {"a": a, "b": b}

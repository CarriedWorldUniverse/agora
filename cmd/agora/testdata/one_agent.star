meta = workflow_meta(
    name = "one-agent-demo",
    description = "a single ctx.agent call, for the CLI's e2e test",
)

def main(ctx, args):
    result = ctx.agent("hello " + args["who"], label = "greet")
    return {"greeting": result}

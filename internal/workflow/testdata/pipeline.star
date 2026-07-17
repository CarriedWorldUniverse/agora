meta = workflow_meta(
    name = "pipeline-demo",
    description = "each item flows through two stages independently, no barrier between stages",
)

def main(ctx, args):
    def stage1(prev, original, index):
        return ctx.agent("s1:" + prev, label = "s1")

    def stage2(prev, original, index):
        return ctx.agent("s2:" + prev, label = "s2")

    items = args["items"]
    results = ctx.pipeline(items, stage1, stage2)
    return {"results": results}

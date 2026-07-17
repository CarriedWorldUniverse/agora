meta = workflow_meta(
    name = "parallel-demo",
    description = "fans out N agent calls concurrently and collects the results",
)

def main(ctx, args):
    labels = args["labels"]
    results = ctx.parallel([
        lambda l = l: ctx.agent("fan:" + l, label = l)
        for l in labels
    ])
    return {"results": results}

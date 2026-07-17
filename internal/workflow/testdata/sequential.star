meta = workflow_meta(
    name = "sequential-demo",
    description = "three chained agent calls -- each stage's prompt includes the prior stage's result, so an edit to one stage naturally invalidates every stage after it",
)

def main(ctx, args):
    a = ctx.agent("step1:" + args["seed"], label = "s1")
    b = ctx.agent("step2:" + a, label = "s2")
    c = ctx.agent("step3:" + b, label = "s3")
    return {"a": a, "b": b, "c": c}

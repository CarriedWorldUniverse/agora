meta = workflow_meta(
    name = "sequential-demo",
    description = "sequential.star with stage 2's prompt edited -- exercises the journal resume property: stage 1 (unchanged) must replay from cache, stage 2 (edited) and stage 3 (downstream of stage 2's now-different result) must both run live",
)

def main(ctx, args):
    a = ctx.agent("step1:" + args["seed"], label = "s1")
    b = ctx.agent("step2v2:" + a, label = "s2")
    c = ctx.agent("step3:" + b, label = "s3")
    return {"a": a, "b": b, "c": c}

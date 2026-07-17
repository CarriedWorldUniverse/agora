meta = workflow_meta(
    name = "now-demo",
    description = "returns ctx.now verbatim -- proves the frozen clock is the only time source the script can observe",
)

def main(ctx, args):
    return {"now": ctx.now}

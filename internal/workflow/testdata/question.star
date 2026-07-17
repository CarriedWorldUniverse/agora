meta = workflow_meta(
    name = "question-demo",
    description = "asks one blocking question, then reports the answer",
)

def main(ctx, args):
    ans = ctx.question({"text": "which color?", "options": [{"label": "red"}, {"label": "blue"}]})
    return {"text": ans["text"], "choice": ans["choice"]}

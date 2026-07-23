#!/usr/bin/env python3
# Minimal MCP stdio server for tests: newline-delimited JSON-RPC.
# Answers initialize, tools/list (one 'echo' tool), tools/call (echoes args).
import sys, json

def send(obj):
    sys.stdout.write(json.dumps(obj) + "\n")
    sys.stdout.flush()

for line in sys.stdin:
    line = line.strip()
    if not line:
        continue
    try:
        msg = json.loads(line)
    except Exception:
        continue
    mid = msg.get("id")
    method = msg.get("method")
    if method == "initialize":
        send({"jsonrpc":"2.0","id":mid,"result":{"protocolVersion":"2024-11-05","serverInfo":{"name":"fake","version":"1"},"capabilities":{}}})
    elif method == "notifications/initialized":
        pass  # notification, no reply
    elif method == "tools/list":
        send({"jsonrpc":"2.0","id":mid,"result":{"tools":[
            {"name":"echo","description":"echo back the text arg","inputSchema":{"type":"object","properties":{"text":{"type":"string"}}}}
        ]}})
    elif method == "tools/call":
        params = msg.get("params",{})
        name = params.get("name")
        args = params.get("arguments",{})
        if name == "echo":
            send({"jsonrpc":"2.0","id":mid,"result":{"content":[{"type":"text","text":"echo:"+str(args.get("text",""))}],"isError":False}})
        else:
            send({"jsonrpc":"2.0","id":mid,"result":{"content":[{"type":"text","text":"unknown tool"}],"isError":True}})
    elif mid is not None:
        send({"jsonrpc":"2.0","id":mid,"error":{"code":-32601,"message":"method not found: "+str(method)}})

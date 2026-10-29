# Reads `go test -json` from stdin; echoes plain output and emits one
# GitHub error annotation per failed test with the tail of its output.
import json, sys
out, failed = {}, []
for line in sys.stdin:
    try:
        e = json.loads(line)
    except Exception:
        print(line, end=""); continue
    t = e.get("Test")
    if e.get("Action") == "output":
        print(e.get("Output", ""), end="")
        if t: out.setdefault(t, []).append(e["Output"])
    if e.get("Action") == "fail" and t:
        failed.append(t)
leaves = [t for t in failed if not any(o != t and o.startswith(t + "/") for o in failed)]
for t in leaves:
    body = "".join(out.get(t, []))[-6000:]
    body = body.replace("%", "%25").replace("\r", "%0D").replace("\n", "%0A")
    print(f"::error title=FAIL {t}::{body}")
sys.exit(1 if failed else 0)

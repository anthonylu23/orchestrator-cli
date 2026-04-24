import json
import sys


print("starting failing training")
print(json.dumps({"type": "metric", "step": 1, "metrics": {"loss": 1.0}}))
print("runtime failure", file=sys.stderr)
sys.exit(7)

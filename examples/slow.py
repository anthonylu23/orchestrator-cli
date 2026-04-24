import json
import time


print("slow start", flush=True)
for step in range(30):
    print(json.dumps({"type": "metric", "step": step, "metrics": {"heartbeat": step}}), flush=True)
    time.sleep(0.2)
print("slow done", flush=True)

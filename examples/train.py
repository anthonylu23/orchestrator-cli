import json
import os
import pathlib
import time


checkpoint_dir = pathlib.Path(os.environ["ORCHESTRATOR_CHECKPOINT_DIR"])
checkpoint_dir.mkdir(parents=True, exist_ok=True)

print("starting local training")
for step in range(1, 4):
    print(json.dumps({
        "type": "metric",
        "step": step,
        "metrics": {"loss": round(1.0 / step, 4), "accuracy": round(step / 3, 4)},
        "split": "train",
    }))
    time.sleep(0.01)

checkpoint = checkpoint_dir / "ckpt-3"
checkpoint.write_text("checkpoint", encoding="utf-8")
print(json.dumps({
    "type": "checkpoint",
    "step": 3,
    "checkpoint_uri": checkpoint.as_uri(),
}))
print(json.dumps({"type": "status", "state": "completed"}))
print("finished local training")

import json
import os
import time
from pathlib import Path


def main() -> None:
    output_dir = Path(os.environ["CLOUDTUNE_OUTPUT_DIR"])
    output_dir.mkdir(parents=True, exist_ok=True)
    (output_dir / "started.json").write_text(json.dumps({"status": "started"}) + "\n", encoding="utf-8")
    print("modal slow start", flush=True)
    for step in range(120):
        print(json.dumps({"type": "metric", "step": step, "metrics": {"heartbeat": step}}), flush=True)
        time.sleep(1)
    (output_dir / "completed.json").write_text(json.dumps({"status": "completed"}) + "\n", encoding="utf-8")
    print("modal slow done", flush=True)


if __name__ == "__main__":
    main()

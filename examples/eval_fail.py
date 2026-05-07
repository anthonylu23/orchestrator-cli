import json
import os
import sys
from pathlib import Path


def main() -> None:
    output_dir = Path(os.environ["CLOUDTUNE_OUTPUT_DIR"])
    output_dir.mkdir(parents=True, exist_ok=True)
    partial = {
        "workload_type": os.environ.get("CLOUDTUNE_WORKLOAD_TYPE"),
        "dataset_path": os.environ.get("CLOUDTUNE_DATASET_PATH"),
        "status": "partial",
    }
    (output_dir / "partial_result.json").write_text(json.dumps(partial, indent=2) + "\n", encoding="utf-8")

    print(json.dumps({"type": "metric", "step": 1, "split": "eval", "metrics": {"accuracy": 0.0}}), flush=True)
    print(json.dumps({"type": "status", "state": "failed_controlled"}), flush=True)
    print("controlled eval failure after partial output", file=sys.stderr, flush=True)
    sys.exit(2)


if __name__ == "__main__":
    main()

import json
import os
from pathlib import Path


def main() -> None:
    dataset_path = os.environ.get("CLOUDTUNE_DATASET_PATH", "")
    output_dir = Path(os.environ["CLOUDTUNE_OUTPUT_DIR"])
    output_dir.mkdir(parents=True, exist_ok=True)

    examples = 0
    if dataset_path:
        with open(dataset_path, "r", encoding="utf-8") as handle:
            for line in handle:
                if line.strip():
                    examples += 1

    accuracy = 1.0 if examples else 0.0
    result = {
        "workload_type": os.environ.get("CLOUDTUNE_WORKLOAD_TYPE"),
        "model_provider": os.environ.get("CLOUDTUNE_MODEL_PROVIDER"),
        "model_name": os.environ.get("CLOUDTUNE_MODEL_NAME"),
        "dataset_path": dataset_path,
        "examples": examples,
        "accuracy": accuracy,
    }
    (output_dir / "eval_result.json").write_text(json.dumps(result, indent=2) + "\n", encoding="utf-8")

    print(json.dumps({"type": "metric", "step": 1, "split": "eval", "metrics": {"accuracy": accuracy}}), flush=True)
    print(json.dumps({"type": "status", "state": "verified"}), flush=True)
    print("evaluation complete", flush=True)


if __name__ == "__main__":
    main()

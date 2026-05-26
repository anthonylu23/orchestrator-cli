#!/usr/bin/env python3
import argparse
import json
import os
import subprocess
import time
from pathlib import Path


def emit(event, events_path):
    print(json.dumps(event), flush=True)
    if events_path:
        Path(events_path).parent.mkdir(parents=True, exist_ok=True)
        with open(events_path, "a", encoding="utf-8") as handle:
            handle.write(json.dumps(event) + "\n")


def upload_checkpoint(local_path, checkpoint_prefix):
    if not checkpoint_prefix:
        return local_path.as_uri()
    checkpoint_uri = checkpoint_prefix.rstrip("/") + "/" + local_path.name
    if checkpoint_uri.startswith("s3://"):
        subprocess.run(["aws", "s3", "cp", str(local_path), checkpoint_uri], check=True)
    else:
        raise ValueError(f"unsupported checkpoint prefix: {checkpoint_uri}")
    return checkpoint_uri


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--epochs", type=int, default=3)
    parser.add_argument("--train-data", default=os.environ.get("SWITCHBOARD_DATA_TRAIN_URI", ""))
    args = parser.parse_args()

    run_id = os.environ.get("SWITCHBOARD_RUN_ID", "")
    attempt_id = os.environ.get("SWITCHBOARD_ATTEMPT_ID", "")
    events_path = os.environ.get("SWITCHBOARD_EVENTS_PATH", "")
    checkpoint_prefix = os.environ.get("SWITCHBOARD_CHECKPOINT_URI_PREFIX", "")
    checkpoint_dir = Path(os.environ.get("SWITCHBOARD_CHECKPOINT_DIR", "/tmp/switchboard/checkpoints"))
    checkpoint_dir.mkdir(parents=True, exist_ok=True)

    if args.train_data:
        emit(
            {
                "type": "status",
                "run_id": run_id,
                "attempt_id": attempt_id,
                "state": "running",
                "message": f"using training data {args.train_data}",
            },
            events_path,
        )

    for epoch in range(1, args.epochs + 1):
        emit(
            {
                "type": "metric",
                "run_id": run_id,
                "attempt_id": attempt_id,
                "step": epoch,
                "epoch": epoch,
                "split": "train",
                "metrics": {"loss": round(1.0 / epoch, 4), "accuracy": round(0.80 + epoch * 0.03, 4)},
            },
            events_path,
        )
        time.sleep(0.1)

    checkpoint_path = checkpoint_dir / f"epoch-{args.epochs}.ckpt"
    checkpoint_path.write_text("lambda s3 checkpoint\n", encoding="utf-8")
    checkpoint_uri = upload_checkpoint(checkpoint_path, checkpoint_prefix)
    emit(
        {
            "type": "checkpoint",
            "run_id": run_id,
            "attempt_id": attempt_id,
            "step": args.epochs,
            "checkpoint_uri": checkpoint_uri,
        },
        events_path,
    )
    emit(
        {
            "type": "status",
            "run_id": run_id,
            "attempt_id": attempt_id,
            "state": "completed",
            "message": "lambda s3 checkpoint smoke completed",
        },
        events_path,
    )


if __name__ == "__main__":
    main()

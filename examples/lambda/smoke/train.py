#!/usr/bin/env python3
import argparse
import json
import os
import time
from pathlib import Path


def emit(event, events_path):
    print(json.dumps(event), flush=True)
    if events_path:
        Path(events_path).parent.mkdir(parents=True, exist_ok=True)
        with open(events_path, "a", encoding="utf-8") as handle:
            handle.write(json.dumps(event) + "\n")


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--epochs", type=int, default=3)
    args = parser.parse_args()

    run_id = os.environ.get("SWITCHBOARD_RUN_ID", "")
    attempt_id = os.environ.get("SWITCHBOARD_ATTEMPT_ID", "")
    events_path = os.environ.get("SWITCHBOARD_EVENTS_PATH", "")
    checkpoint_dir = Path(os.environ.get("SWITCHBOARD_CHECKPOINT_DIR", "/tmp/switchboard/checkpoints"))
    checkpoint_dir.mkdir(parents=True, exist_ok=True)

    for epoch in range(1, args.epochs + 1):
        loss = round(1.0 / epoch, 4)
        accuracy = round(0.80 + epoch * 0.03, 4)
        emit(
            {
                "type": "metric",
                "run_id": run_id,
                "attempt_id": attempt_id,
                "step": epoch,
                "epoch": epoch,
                "split": "train",
                "metrics": {"loss": loss, "accuracy": accuracy},
            },
            events_path,
        )
        time.sleep(0.1)

    checkpoint_path = checkpoint_dir / f"epoch-{args.epochs}.ckpt"
    checkpoint_path.write_text("lambda smoke checkpoint\n", encoding="utf-8")
    emit(
        {
            "type": "checkpoint",
            "run_id": run_id,
            "attempt_id": attempt_id,
            "step": args.epochs,
            "checkpoint_uri": checkpoint_path.as_uri(),
        },
        events_path,
    )
    emit(
        {
            "type": "status",
            "run_id": run_id,
            "attempt_id": attempt_id,
            "state": "completed",
            "message": "lambda smoke completed",
        },
        events_path,
    )


if __name__ == "__main__":
    main()

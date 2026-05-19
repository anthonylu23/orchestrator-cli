import argparse
import csv
import json
import os
import pathlib
import random
import sys
import tempfile
from urllib.parse import unquote, urlparse

import torch


FEATURE_COLUMNS = ["SepalLengthCm", "SepalWidthCm", "PetalLengthCm", "PetalWidthCm"]


class IrisMLP(torch.nn.Module):
    def __init__(self):
        super().__init__()
        self.layers = torch.nn.Sequential(
            torch.nn.Linear(4, 16),
            torch.nn.ReLU(),
            torch.nn.Linear(16, 3),
        )

    def forward(self, x):
        return self.layers(x)


def emit(event):
    print(json.dumps(event, sort_keys=True), flush=True)


def load_iris(path):
    rows = []
    with open(path, newline="", encoding="utf-8") as handle:
        for row in csv.DictReader(handle):
            rows.append(row)
    if not rows:
        raise ValueError(f"no rows found in {path}")

    labels = sorted({row["Species"] for row in rows})
    label_to_index = {label: index for index, label in enumerate(labels)}
    features = [[float(row[column]) for column in FEATURE_COLUMNS] for row in rows]
    targets = [label_to_index[row["Species"]] for row in rows]
    return torch.tensor(features, dtype=torch.float32), torch.tensor(targets, dtype=torch.long), label_to_index


def split_dataset(features, targets):
    generator = torch.Generator().manual_seed(23)
    indices = torch.randperm(len(features), generator=generator)
    validation_count = max(1, len(features) // 5)
    validation_indices = indices[:validation_count]
    train_indices = indices[validation_count:]

    x_train = features[train_indices]
    x_validation = features[validation_indices]
    mean = x_train.mean(dim=0, keepdim=True)
    std = x_train.std(dim=0, keepdim=True).clamp_min(1e-6)

    return (
        (x_train - mean) / std,
        targets[train_indices],
        (x_validation - mean) / std,
        targets[validation_indices],
    )


def accuracy(logits, targets):
    return (logits.argmax(dim=1) == targets).float().mean().item()


def checkpoint_path(value):
    if not value:
        return None
    parsed = urlparse(value)
    if parsed.scheme == "file":
        return pathlib.Path(unquote(parsed.path))
    if parsed.scheme == "gs":
        return download_gcs_checkpoint(value)
    if parsed.scheme:
        return None
    return pathlib.Path(value)


def download_gcs_checkpoint(uri):
    from google.cloud import storage

    parsed = urlparse(uri)
    target = pathlib.Path(tempfile.gettempdir()) / pathlib.Path(unquote(parsed.path)).name
    client = storage.Client()
    client.bucket(parsed.netloc).blob(unquote(parsed.path.lstrip("/"))).download_to_filename(target)
    return target


def upload_checkpoint_if_needed(path):
    prefix = os.environ.get("SWITCHBOARD_CHECKPOINT_URI_PREFIX") or os.environ.get("ORCHESTRATOR_CHECKPOINT_URI_PREFIX", "")
    parsed = urlparse(prefix)
    if parsed.scheme != "gs":
        return path.as_uri()

    from google.cloud import storage

    object_prefix = unquote(parsed.path.strip("/"))
    object_name = "/".join(part for part in [object_prefix, path.name] if part)
    client = storage.Client()
    client.bucket(parsed.netloc).blob(object_name).upload_from_filename(path)
    return f"gs://{parsed.netloc}/{object_name}"


def train(args):
    random.seed(23)
    torch.manual_seed(23)
    torch.set_num_threads(1)

    features, targets, label_to_index = load_iris(args.data)
    x_train, y_train, x_validation, y_validation = split_dataset(features, targets)

    model = IrisMLP()
    optimizer = torch.optim.Adam(model.parameters(), lr=args.learning_rate)
    criterion = torch.nn.CrossEntropyLoss()
    checkpoint_dir = pathlib.Path(os.environ.get("SWITCHBOARD_CHECKPOINT_DIR") or os.environ["ORCHESTRATOR_CHECKPOINT_DIR"])
    checkpoint_dir.mkdir(parents=True, exist_ok=True)

    start_epoch = 1
    best_validation_accuracy = -1.0
    resume_from = checkpoint_path(os.environ.get("SWITCHBOARD_RESUME_FROM", os.environ.get("ORCHESTRATOR_RESUME_FROM", "")))
    if resume_from and resume_from.exists():
        saved = torch.load(resume_from, map_location="cpu")
        model.load_state_dict(saved["model_state"])
        optimizer.load_state_dict(saved["optimizer_state"])
        start_epoch = int(saved["epoch"]) + 1
        best_validation_accuracy = float(saved["best_validation_accuracy"])
        emit({"type": "status", "state": "resumed", "checkpoint": str(resume_from), "epoch": start_epoch})

    for epoch in range(start_epoch, args.epochs + 1):
        model.train()
        optimizer.zero_grad()
        train_logits = model(x_train)
        train_loss = criterion(train_logits, y_train)
        train_loss.backward()
        optimizer.step()

        model.eval()
        with torch.no_grad():
            validation_logits = model(x_validation)
            validation_loss = criterion(validation_logits, y_validation)
            train_accuracy = accuracy(train_logits, y_train)
            validation_accuracy = accuracy(validation_logits, y_validation)

        emit({
            "type": "metric",
            "step": epoch,
            "epoch": epoch,
            "split": "validation",
            "metrics": {
                "train_accuracy": round(train_accuracy, 6),
                "train_loss": round(train_loss.item(), 6),
                "val_accuracy": round(validation_accuracy, 6),
                "val_loss": round(validation_loss.item(), 6),
            },
        })

        if validation_accuracy >= best_validation_accuracy:
            best_validation_accuracy = validation_accuracy
            path = checkpoint_dir / f"iris-epoch-{epoch}.pt"
            torch.save({
                "epoch": epoch,
                "best_validation_accuracy": best_validation_accuracy,
                "label_to_index": label_to_index,
                "model_state": model.state_dict(),
                "optimizer_state": optimizer.state_dict(),
            }, path)
            emit({"type": "checkpoint", "step": epoch, "checkpoint_uri": upload_checkpoint_if_needed(path)})

    emit({"type": "status", "state": "completed"})


def main():
    parser = argparse.ArgumentParser(description="Train a tiny PyTorch MLP on the Iris dataset.")
    parser.add_argument("--data", required=True, help="Path to Kaggle Iris CSV data.")
    parser.add_argument("--epochs", type=int, default=40)
    parser.add_argument("--learning-rate", type=float, default=0.03)
    args = parser.parse_args()
    if args.epochs < 1:
        print("--epochs must be at least 1", file=sys.stderr)
        return 2
    train(args)
    return 0


if __name__ == "__main__":
    raise SystemExit(main())

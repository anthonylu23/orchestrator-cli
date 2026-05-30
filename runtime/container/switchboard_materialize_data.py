#!/usr/bin/env python3
"""Materialize Switchboard URI data inputs inside image-based jobs."""

import argparse
import json
import os
import pathlib
import re
import shutil
import subprocess
import sys
import urllib.request
from dataclasses import dataclass
from urllib.parse import unquote, urlparse


DATA_URI_RE = re.compile(r"^SWITCHBOARD_DATA_(?P<key>[A-Z0-9_]+)_URI$")


@dataclass(frozen=True)
class DataInput:
    key: str
    source: str
    mount: str

    @property
    def destination_is_file(self) -> bool:
        if self.mount.endswith("/"):
            return False
        parsed = urlparse(self.source)
        path = unquote(parsed.path)
        if path.endswith("/"):
            return False
        if pathlib.PurePosixPath(path).suffix:
            return True
        return pathlib.PurePosixPath(self.mount).suffix != ""


def discover_inputs(env):
    inputs = []
    for name, source in env.items():
        match = DATA_URI_RE.match(name)
        if not match or not source:
            continue
        key = match.group("key")
        mount = env.get(f"SWITCHBOARD_DATA_{key}_MOUNT")
        if not mount:
            continue
        inputs.append(DataInput(key=key, source=source, mount=mount))
    return sorted(inputs, key=lambda item: item.key)


def materialize(input_item):
    parsed = urlparse(input_item.source)
    scheme = parsed.scheme.lower()
    if scheme in ("", "file"):
        materialize_local(input_item, parsed)
    elif scheme in ("http", "https"):
        materialize_http(input_item)
    elif scheme == "gs":
        materialize_gcs(input_item)
    elif scheme == "s3":
        materialize_s3(input_item)
    else:
        raise ValueError(f"unsupported data URI scheme {scheme!r} for {input_item.key}")


def materialize_local(input_item, parsed):
    if parsed.scheme == "file":
        source = pathlib.Path(unquote(parsed.path))
    else:
        source = pathlib.Path(input_item.source)
    destination = pathlib.Path(input_item.mount)
    if source.is_dir():
        destination.mkdir(parents=True, exist_ok=True)
        shutil.copytree(source, destination, dirs_exist_ok=True)
        return
    if input_item.destination_is_file:
        destination.parent.mkdir(parents=True, exist_ok=True)
        shutil.copy2(source, destination)
        return
    destination.mkdir(parents=True, exist_ok=True)
    shutil.copy2(source, destination / source.name)


def materialize_http(input_item):
    destination = pathlib.Path(input_item.mount)
    if not input_item.destination_is_file:
        filename = pathlib.PurePosixPath(unquote(urlparse(input_item.source).path)).name
        if not filename:
            raise ValueError(f"HTTP data input {input_item.key} must resolve to a file name")
        destination = destination / filename
    destination.parent.mkdir(parents=True, exist_ok=True)
    with urllib.request.urlopen(input_item.source) as response:
        with open(destination, "wb") as handle:
            shutil.copyfileobj(response, handle)


def materialize_gcs(input_item):
    try:
        from google.cloud import storage
    except ImportError:
        copy_with_gcs_cli(input_item)
        return

    bucket_name, object_name = parse_object_uri(input_item.source, "gs")
    client = storage.Client()
    bucket = client.bucket(bucket_name)
    if input_item.destination_is_file:
        destination = pathlib.Path(input_item.mount)
        destination.parent.mkdir(parents=True, exist_ok=True)
        bucket.blob(object_name).download_to_filename(destination)
        return

    destination = pathlib.Path(input_item.mount)
    destination.mkdir(parents=True, exist_ok=True)
    prefix = object_name.rstrip("/") + "/"
    copied = 0
    for blob in client.list_blobs(bucket_name, prefix=prefix):
        rel = blob.name[len(prefix):]
        if not rel:
            continue
        target = destination / rel
        target.parent.mkdir(parents=True, exist_ok=True)
        blob.download_to_filename(target)
        copied += 1
    if copied == 0:
        raise FileNotFoundError(f"no GCS objects found under gs://{bucket_name}/{prefix}")


def materialize_s3(input_item):
    try:
        import boto3
    except ImportError:
        copy_with_aws_cli(input_item)
        return

    bucket_name, object_name = parse_object_uri(input_item.source, "s3")
    client = boto3.client("s3")
    if input_item.destination_is_file:
        destination = pathlib.Path(input_item.mount)
        destination.parent.mkdir(parents=True, exist_ok=True)
        client.download_file(bucket_name, object_name, str(destination))
        return

    destination = pathlib.Path(input_item.mount)
    destination.mkdir(parents=True, exist_ok=True)
    prefix = object_name.rstrip("/") + "/"
    paginator = client.get_paginator("list_objects_v2")
    copied = 0
    for page in paginator.paginate(Bucket=bucket_name, Prefix=prefix):
        for item in page.get("Contents", []):
            key = item["Key"]
            rel = key[len(prefix):]
            if not rel:
                continue
            target = destination / rel
            target.parent.mkdir(parents=True, exist_ok=True)
            client.download_file(bucket_name, key, str(target))
            copied += 1
    if copied == 0:
        raise FileNotFoundError(f"no S3 objects found under s3://{bucket_name}/{prefix}")


def copy_with_gcs_cli(input_item):
    if shutil.which("gcloud"):
        args = ["gcloud", "storage", "cp"]
        if not input_item.destination_is_file:
            args.append("--recursive")
        run(args + [input_item.source, input_item.mount])
        return
    if shutil.which("gsutil"):
        args = ["gsutil", "-m", "cp"]
        if not input_item.destination_is_file:
            args.append("-r")
        run(args + [input_item.source, input_item.mount])
        return
    raise RuntimeError("gs:// inputs require google-cloud-storage, gcloud, or gsutil in the container")


def copy_with_aws_cli(input_item):
    if not shutil.which("aws"):
        raise RuntimeError("s3:// inputs require boto3 or the AWS CLI in the container")
    args = ["aws", "s3", "cp"]
    if not input_item.destination_is_file:
        args.append("--recursive")
    run(args + [input_item.source, input_item.mount])


def parse_object_uri(value, expected_scheme):
    parsed = urlparse(value)
    if parsed.scheme.lower() != expected_scheme or not parsed.netloc or not parsed.path.strip("/"):
        raise ValueError(f"expected {expected_scheme}://bucket/object URI, got {value!r}")
    return parsed.netloc, unquote(parsed.path.lstrip("/"))


def run(args):
    subprocess.run(args, check=True)


def input_summary(inputs):
    return [
        {
            "key": item.key,
            "source": item.source,
            "mount": item.mount,
            "destination_kind": "file" if item.destination_is_file else "directory",
        }
        for item in inputs
    ]


def main():
    parser = argparse.ArgumentParser(description="Materialize SWITCHBOARD_DATA_* URI inputs.")
    parser.add_argument("--dry-run", action="store_true", help="Print planned materialization without downloading.")
    parser.add_argument("command", nargs=argparse.REMAINDER, help="Optional command to exec after materialization; prefix with --.")
    args = parser.parse_args()
    command = args.command
    if command and command[0] == "--":
        command = command[1:]

    inputs = discover_inputs(os.environ)
    if args.dry_run:
        print(json.dumps({"inputs": input_summary(inputs)}, sort_keys=True))
        return 0

    for item in inputs:
        print(f"materializing {item.key}: {item.source} -> {item.mount}", file=sys.stderr, flush=True)
        materialize(item)

    if command:
        os.execvp(command[0], command)
    return 0


if __name__ == "__main__":
    raise SystemExit(main())

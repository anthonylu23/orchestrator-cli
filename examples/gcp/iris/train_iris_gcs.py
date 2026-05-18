import argparse
import pathlib
import sys
import tempfile
from urllib.parse import unquote, urlparse

import iris_pytorch


def download_gcs(uri, destination):
    from google.cloud import storage

    parsed = urlparse(uri)
    if parsed.scheme != "gs" or not parsed.netloc or not parsed.path.strip("/"):
        raise ValueError(f"expected a gs://bucket/object URI, got {uri!r}")

    client = storage.Client()
    bucket = client.bucket(parsed.netloc)
    blob = bucket.blob(unquote(parsed.path.lstrip("/")))
    blob.download_to_filename(destination)
    return destination


def local_data_path(value):
    parsed = urlparse(value)
    if parsed.scheme == "gs":
        target = pathlib.Path(tempfile.gettempdir()) / "switchboard-iris.csv"
        return download_gcs(value, target)
    if parsed.scheme == "file":
        return pathlib.Path(unquote(parsed.path))
    if parsed.scheme:
        raise ValueError(f"unsupported data URI scheme {parsed.scheme!r}; use gs:// or a local path")
    return pathlib.Path(value)


def main():
    parser = argparse.ArgumentParser(description="Run the Switchboard Iris PyTorch demo from a GCS CSV.")
    parser.add_argument("--data-uri", required=True, help="GCS URI or local path for Iris.csv.")
    parser.add_argument("--epochs", type=int, default=40)
    parser.add_argument("--learning-rate", type=float, default=0.03)
    args = parser.parse_args()

    data_path = local_data_path(args.data_uri)
    sys.argv = [
        "iris_pytorch.py",
        "--data",
        str(data_path),
        "--epochs",
        str(args.epochs),
        "--learning-rate",
        str(args.learning_rate),
    ]
    return iris_pytorch.main()


if __name__ == "__main__":
    raise SystemExit(main())

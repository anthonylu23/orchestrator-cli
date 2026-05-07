#!/usr/bin/env python3
import json
import os
import shlex
import sys
import traceback
from pathlib import Path


CONTROL_PREFIX = "__CLOUDTUNE_MODAL__ "
REMOTE_ROOT = "/workspace/cloudtune"


def control(event):
    print(CONTROL_PREFIX + json.dumps(event, separators=(",", ":")), flush=True)


def fail(kind, message, exit_code=1):
    control({"event": "error", "kind": kind, "message": message, "exit_code": exit_code})
    return exit_code


def main() -> int:
    if len(sys.argv) < 2:
        return fail("invalid_spec_error", "modal runner command is required")
    command = sys.argv[1]
    if command == "doctor":
        try:
            import modal  # noqa: F401
        except Exception as exc:
            return fail("auth_error", f"modal Python SDK is not importable: {exc}")
        control({"event": "doctor", "status": "ok"})
        return 0
    if command == "cancel":
        if len(sys.argv) != 3:
            return fail("invalid_spec_error", "cancel requires a sandbox id")
        return cancel(sys.argv[2])
    if command == "status":
        if len(sys.argv) != 3:
            return fail("invalid_spec_error", "status requires a sandbox id")
        return status(sys.argv[2])
    if command == "run":
        if len(sys.argv) != 3:
            return fail("invalid_spec_error", "run requires a request JSON path")
        return run(sys.argv[2])
    return fail("invalid_spec_error", f"unknown modal runner command {command!r}")


def cancel(sandbox_id: str) -> int:
    try:
        import modal

        sandbox = modal.Sandbox.from_id(sandbox_id)
        sandbox.terminate()
        try:
            sandbox.detach()
        except Exception:
            pass
        control({"event": "cancelled", "provider_ref": f"modal-sandbox:{sandbox_id}"})
        return 0
    except Exception as exc:
        return fail("provider_internal_error", f"cancel sandbox {sandbox_id}: {exc}")


def status(sandbox_id: str) -> int:
    try:
        import modal

        sandbox = modal.Sandbox.from_id(sandbox_id)
        code = sandbox.poll()
        if code is None:
            state = "running"
        elif code == 0:
            state = "succeeded"
        else:
            state = "failed"
        control({"event": "status", "provider_ref": f"modal-sandbox:{sandbox_id}", "state": state, "exit_code": code})
        try:
            sandbox.detach()
        except Exception:
            pass
        return 0
    except Exception as exc:
        return fail("provider_internal_error", f"status sandbox {sandbox_id}: {exc}")


def run(request_path: str) -> int:
    try:
        import modal
    except Exception as exc:
        return fail("auth_error", f"modal Python SDK is not importable: {exc}")

    request = json.loads(Path(request_path).read_text(encoding="utf-8"))
    app_name = request.get("app_name") or "cloudtune-orchestrator"
    timeout = int(request.get("timeout_seconds") or 300)
    sandbox = None
    try:
        app = modal.App.lookup(app_name, create_if_missing=True)
        image = modal.Image.debian_slim()
        sandbox = modal.Sandbox.create("sleep", str(timeout), app=app, image=image, timeout=timeout)
        sandbox_id = sandbox.object_id
        control({"event": "provider_ref", "provider_ref": f"modal-sandbox:{sandbox_id}", "sandbox_id": sandbox_id})

        remote_script = f"{REMOTE_ROOT}/job/{Path(request['script']).name}"
        sandbox.filesystem.copy_from_local(request["script"], remote_script)

        runtime_env = dict(request.get("runtime_env") or {})
        job_env = dict(request.get("env") or {})
        runtime_env.update(job_env)
        runtime_env["CLOUDTUNE_OUTPUT_DIR"] = f"{REMOTE_ROOT}/outputs"
        runtime_env["CLOUDTUNE_CHECKPOINT_DIR"] = f"{REMOTE_ROOT}/checkpoints"
        runtime_env["CLOUDTUNE_EVENTS_PATH"] = f"{REMOTE_ROOT}/events.jsonl"
        runtime_env["CLOUDTUNE_ARTIFACTS_MANIFEST"] = f"{REMOTE_ROOT}/artifacts.json"
        runtime_env["SWITCHBOARD_CHECKPOINT_DIR"] = runtime_env["CLOUDTUNE_CHECKPOINT_DIR"]
        runtime_env["SWITCHBOARD_EVENTS_PATH"] = runtime_env["CLOUDTUNE_EVENTS_PATH"]
        runtime_env["ORCHESTRATOR_CHECKPOINT_DIR"] = runtime_env["CLOUDTUNE_CHECKPOINT_DIR"]
        runtime_env["ORCHESTRATOR_EVENTS_PATH"] = runtime_env["CLOUDTUNE_EVENTS_PATH"]

        dataset_path = request.get("dataset_path") or ""
        if dataset_path:
            remote_dataset = f"{REMOTE_ROOT}/data/{Path(dataset_path).name}"
            sandbox.filesystem.copy_from_local(dataset_path, remote_dataset)
            runtime_env["CLOUDTUNE_DATASET_PATH"] = remote_dataset

        env_file = f"{REMOTE_ROOT}/env.sh"
        sandbox.filesystem.write_text(env_file, render_env(runtime_env))
        args = " ".join(shlex.quote(value) for value in request.get("args") or [])
        command = (
            f"mkdir -p {REMOTE_ROOT}/outputs {REMOTE_ROOT}/checkpoints && "
            f"set -a && . {shlex.quote(env_file)} && set +a && "
            f"cd {REMOTE_ROOT} && python3 {shlex.quote(remote_script)} {args}"
        )
        process = sandbox.exec("bash", "-lc", command)
        stdout = process.stdout.read() if process.stdout else ""
        stderr = process.stderr.read() if process.stderr else ""
        exit_code = process.wait()

        archive_remote = f"{REMOTE_ROOT}/artifacts.tar.gz"
        archive_cmd = f"cd {REMOTE_ROOT} && tar -czf artifacts.tar.gz outputs checkpoints 2>/tmp/cloudtune-tar.err || true"
        archive_process = sandbox.exec("bash", "-lc", archive_cmd)
        archive_stderr = archive_process.stderr.read() if archive_process.stderr else ""
        archive_process.wait()
        local_archive = request["local_archive"]
        archive_fetched = False
        try:
            sandbox.filesystem.copy_to_local(archive_remote, local_archive)
            archive_fetched = True
        except Exception as exc:
            archive_stderr = f"{archive_stderr}\nartifact fetch failed: {exc}".strip()

        control(
            {
                "event": "result",
                "provider_ref": f"modal-sandbox:{sandbox_id}",
                "sandbox_id": sandbox_id,
                "exit_code": exit_code,
                "stdout": stdout,
                "stderr": stderr,
                "archive_fetched": archive_fetched,
                "archive_stderr": archive_stderr,
            }
        )
        return 0
    except Exception as exc:
        control(
            {
                "event": "error",
                "kind": classify_error(exc),
                "message": str(exc),
                "traceback": traceback.format_exc(),
                "exit_code": 1,
            }
        )
        return 1
    finally:
        if sandbox is not None:
            try:
                sandbox.terminate()
            except Exception:
                pass
            try:
                sandbox.detach()
            except Exception:
                pass


def render_env(values) -> str:
    lines = []
    for key in sorted(values):
        if not key:
            continue
        value = "" if values[key] is None else str(values[key])
        lines.append(f"export {key}={shlex.quote(value)}")
    return "\n".join(lines) + "\n"


def classify_error(exc) -> str:
    name = exc.__class__.__name__.lower()
    message = str(exc).lower()
    if "auth" in name or "token" in message or "credential" in message:
        return "auth_error"
    if "quota" in message or "rate" in message:
        return "quota_error"
    if "timeout" in name or "timeout" in message:
        return "provider_internal_error"
    return "provider_internal_error"


if __name__ == "__main__":
    raise SystemExit(main())

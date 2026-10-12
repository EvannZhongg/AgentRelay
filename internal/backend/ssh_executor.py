"""Ephemeral capability backend; standard library only, no agent runtime."""
import base64
import json
import os
import platform
import signal
import subprocess
import sys
import threading
import time
import urllib.parse

LIMIT = 1024 * 1024


def reply(status, value):
    body = json.dumps(value, ensure_ascii=True).encode()
    print(json.dumps({"status": status, "body": base64.b64encode(body).decode()}), flush=True)


def resolve(root, value):
    if os.path.isabs(value) or "\\" in value or "\x00" in value:
        raise ValueError("path must be workspace-relative")
    target = os.path.realpath(os.path.join(root, value))
    if os.path.commonpath([root, target]) != root:
        raise ValueError("path escapes the authorized workspace")
    return target


def execute(root, request):
    command = request.get("command", "")
    if not isinstance(command, str) or not command.strip():
        raise ValueError("command is required")
    timeout = request.get("timeout_ms", 0)
    if not isinstance(timeout, int) or timeout < 0 or timeout > 1800000:
        raise ValueError("timeout_ms must be between 0 and 1800000")
    env = os.environ.copy()
    additions = request.get("env", {})
    if not isinstance(additions, dict) or any(not isinstance(k, str) or not isinstance(v, str) for k, v in additions.items()):
        raise ValueError("env must contain string values")
    env.update(additions)
    cwd = resolve(root, request.get("working_dir", ""))
    started = time.monotonic()
    proc = subprocess.Popen(["/bin/sh", "-c", command], cwd=cwd, env=env, stdin=subprocess.DEVNULL,
                            stdout=subprocess.PIPE, stderr=subprocess.PIPE, start_new_session=True)
    outputs = [bytearray(), bytearray()]
    truncated = [False, False]

    def collect(stream, index):
        while True:
            chunk = stream.read(65536)
            if not chunk:
                break
            remaining = LIMIT - len(outputs[index])
            outputs[index].extend(chunk[:remaining])
            truncated[index] |= len(chunk) > remaining

    def kill():
        try:
            os.killpg(proc.pid, signal.SIGKILL)
        except ProcessLookupError:
            pass

    # The controller leaves stdin open. SSH channel loss/cancellation closes it.
    def disconnected():
        while os.read(0, 65536):
            pass
        kill()

    threading.Thread(target=disconnected, daemon=True).start()
    readers = [threading.Thread(target=collect, args=(proc.stdout, 0)),
               threading.Thread(target=collect, args=(proc.stderr, 1))]
    for reader in readers:
        reader.start()
    timed_out = False
    try:
        proc.wait(timeout=(timeout or 1800000) / 1000)
    except subprocess.TimeoutExpired:
        timed_out = True
        kill()
        proc.wait()
    # Descendants may retain output pipes after the shell exits.
    for reader in readers:
        reader.join(timeout=1)
    if any(reader.is_alive() for reader in readers):
        kill()
    for reader in readers:
        reader.join()
    return {"stdout": outputs[0].decode("utf-8", "replace"), "stderr": outputs[1].decode("utf-8", "replace"),
            "exit_code": proc.returncode, "duration_ms": int((time.monotonic() - started) * 1000),
            "timed_out": timed_out, "output_truncated": any(truncated)}


try:
    envelope = json.loads(sys.stdin.buffer.readline(48 * 1024 * 1024))
    root = os.path.realpath(envelope["workspace"])
    if not os.path.isdir(root):
        raise ValueError("authorized workspace must already exist")
    request = envelope["request"]
    method = request["method"]
    url = urllib.parse.urlsplit(request["path"])
    if method == "GET" and url.path == "/probe":
        reply(200, {"os": platform.system().lower(), "architecture": platform.machine(),
                    "workspace": root, "capabilities": {"exec": True, "files": True, "sync": False,
                    "tasks": False, "sessions": False, "shells": ["sh"], "max_payload_bytes": 32 * 1024 * 1024}})
    elif method == "POST" and url.path == "/v1/exec":
        body = json.loads(base64.b64decode(request.get("body", "")))
        reply(200, execute(root, body))
    elif method == "GET" and url.path == "/v1/files/read":
        path = urllib.parse.parse_qs(url.query).get("path", [""])[0]
        with open(resolve(root, path), "rb") as stream:
            data = stream.read(16 * 1024 * 1024 + 1)
        if len(data) > 16 * 1024 * 1024:
            raise ValueError("file read exceeds 16 MiB")
        reply(200, {"path": path, "content_base64": base64.b64encode(data).decode(), "bytes": len(data)})
    else:
        reply(501, {"error": "capability is unavailable on this connection"})
except FileNotFoundError as error:
    reply(404, {"error": str(error)})
except (ValueError, KeyError, TypeError, OSError) as error:
    reply(400, {"error": str(error)})

#!/usr/bin/env python3
# tool_gateway/start-gateway.py - launch the Python gateway in background and wait for /health.
import argparse
import http.client
import os
import subprocess
import sys
import time
from pathlib import Path

REPO_ROOT = Path(__file__).resolve().parent.parent
DEFAULT_PORT = 8080
PYTHON_EXE = sys.executable


def wait_health(host: str, port: int, timeout_sec: int) -> bool:
    deadline = time.monotonic() + timeout_sec
    while time.monotonic() < deadline:
        try:
            conn = http.client.HTTPConnection(host, port, timeout=2)
            conn.request("GET", "/health")
            r = conn.getresponse()
            if r.status == 200:
                return True
            conn.close()
        except Exception:
            pass
        time.sleep(0.3)
    return False


def main():
    p = argparse.ArgumentParser()
    p.add_argument("--port", type=int, default=DEFAULT_PORT)
    p.add_argument("--wait-timeout-sec", type=int, default=15)
    p.add_argument("--host", default="127.0.0.1")
    p.add_argument("--mock", action="store_true",
                   help="start deterministic service-tool responses without PowerShell or external services")
    p.add_argument("--mock-tools", default="",
                   help="comma-separated tool names exposed by --mock")
    args = p.parse_args()

    logs_dir = REPO_ROOT / "logs"
    logs_dir.mkdir(parents=True, exist_ok=True)
    log_file = logs_dir / f"gateway_{args.port}.log"
    pid_file = logs_dir / f"gateway_{args.port}.pid"

    # already running?
    if pid_file.is_file():
        old = pid_file.read_text(encoding="utf-8").strip()
        if old.isdigit():
            try:
                os.kill(int(old), 0)
                if wait_health(args.host, args.port, 3):
                    print(f"[gateway] already running on port {args.port} (pid={old})")
                    return 0
            except (OSError, ProcessLookupError):
                pass
            print(f"[gateway] stale pid file (pid={old}), removing")
            pid_file.unlink(missing_ok=True)

    # truncate log
    log_file.write_text("", encoding="utf-8")

    server_py = REPO_ROOT / "tool_gateway" / "server.py"
    cmd = [
        PYTHON_EXE, "-u", str(server_py),
        "--port", str(args.port),
        "--host", args.host,
        "--log-file", str(log_file),
    ]
    if args.mock:
        cmd.append("--mock")
        if args.mock_tools:
            cmd.extend(["--mock-tools", args.mock_tools])

    # Detach the gateway without using a Windows-only Popen argument. On Unix,
    # start_new_session is the equivalent of a new process group; Windows needs
    # the documented creation flags instead.
    popen_options = {
        "stdin": subprocess.DEVNULL,
        "stdout": subprocess.DEVNULL,
        "stderr": subprocess.DEVNULL,
        "close_fds": True,
        "env": {**os.environ, "PYTHONUNBUFFERED": "1"},
    }
    if os.name == "nt":
        popen_options["creationflags"] = 0x00000008 | 0x00000200  # DETACHED_PROCESS | CREATE_NEW_PROCESS_GROUP
    else:
        popen_options["start_new_session"] = True
    proc = subprocess.Popen(cmd, **popen_options)
    pid_file.write_text(str(proc.pid), encoding="utf-8")
    print(f"[gateway] launching pid={proc.pid}, log={log_file}")

    # Poll for readiness with longer timeout; allow the child a moment to bind the socket.
    time.sleep(0.5)
    if wait_health(args.host, args.port, args.wait_timeout_sec):
        print(f"[gateway] READY on http://{args.host}:{args.port}/")
        print(f"[gateway] tools endpoint: http://{args.host}:{args.port}/api/agent/tools")
        print(f"[gateway] tool_call   endpoint: http://{args.host}:{args.port}/api/agent/tool_call")
        return 0
    else:
        print(f"[gateway] did not become healthy in {args.wait_timeout_sec}s. Tail of log:")
        try:
            tail = log_file.read_text(encoding="utf-8")
            print(tail)
        except Exception as e:
            print(f"[gateway] could not read log: {e}")
        try:
            proc.kill()
        except Exception:
            pass
        pid_file.unlink(missing_ok=True)
        return 1


if __name__ == "__main__":
    sys.exit(main())

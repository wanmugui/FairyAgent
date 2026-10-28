#!/usr/bin/env python3
# tool_gateway/stop-gateway.py - stop a launched gateway.
import argparse
import os
from pathlib import Path

REPO_ROOT = Path(__file__).resolve().parent.parent


def main():
    p = argparse.ArgumentParser()
    p.add_argument("--port", type=int, default=8080)
    args = p.parse_args()

    pid_file = REPO_ROOT / "logs" / f"gateway_{args.port}.pid"
    if not pid_file.is_file():
        print(f"[gateway] no pid file for port {args.port}; not running here")
        return 0

    pid_str = pid_file.read_text(encoding="utf-8").strip()
    if pid_str.isdigit():
        pid = int(pid_str)
        try:
            os.kill(pid, 9)
            print(f"[gateway] stopped pid={pid}")
        except ProcessLookupError:
            print(f"[gateway] pid={pid} already gone")
    pid_file.unlink(missing_ok=True)
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
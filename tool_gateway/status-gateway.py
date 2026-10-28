#!/usr/bin/env python3
# tool_gateway/status-gateway.py - probe a running gateway.
import argparse
import http.client
import json
import os
from pathlib import Path

REPO_ROOT = Path(__file__).resolve().parent.parent


def main():
    p = argparse.ArgumentParser()
    p.add_argument("--port", type=int, default=8080)
    p.add_argument("--host", default="127.0.0.1")
    args = p.parse_args()

    pid_file = REPO_ROOT / "logs" / f"gateway_{args.port}.pid"
    pid = ""
    if pid_file.is_file():
        pid = pid_file.read_text(encoding="utf-8").strip()

    print(f"[gateway] port      : {args.port}")
    print(f"[gateway] pid_file  : {pid_file} ({pid})")

    alive = False
    if pid.isdigit():
        try:
            os.kill(int(pid), 0)
            alive = True
        except OSError:
            pass
    print(f"[gateway] proc alive: {alive}")

    def probe(path):
        try:
            conn = http.client.HTTPConnection(args.host, args.port, timeout=3)
            conn.request("GET", path)
            r = conn.getresponse()
            body = r.read().decode("utf-8", errors="replace")
            print(f"  status={r.status} body={body}")
        except Exception as e:
            print(f"  unreachable: {e}")

    print("[gateway] /health   :")
    probe("/health")
    print("[gateway] /api/agent/tools :")
    probe("/api/agent/tools")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
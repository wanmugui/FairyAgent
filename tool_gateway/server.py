#!/usr/bin/env python3
# tool_gateway/server.py - deterministic local mock for the unified tool gateway.
#
# Protocol (matches D:\agent-service-main\configs\config.yml
# agentV3.tools.unifiedToolService contract):
#
#   POST /api/agent/tool_call
#   Content-Type: application/json
#   Request body:
#     { "tool_call_id": "...", "tool_name": "read_file", "arguments": "<json str>" }
#   Response body (200):
#     { "tool_call_id": "...", "result": "<str>", "error": "" }
#   Response body (404):
#     { "tool_call_id": "...", "result": "", "error": "no handler registered ..." }
#
# This process deliberately does not proxy repository-local executables. The
# Agent calls a configured real HTTP service directly; this server only makes
# protocol and frontend regressions deterministic.

import argparse
import json
import os
import threading
import time
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from typing import Optional

# Per-tool enable/disable. Empty default = all registered tools enabled.
HTTP_TOOLS_ENABLED: dict = {}
DEFAULT_MOCK_TOOLS = (
    "web_search",
    "web_fetch",
    "image_search",
    "image_generate",
    "image_vqa",
    "document_parser",
    "knowledge_download",
    "knowledge_retrieve",
    "memory_search",
    "reflection",
)


def is_tool_enabled(name: str) -> bool:
    if not HTTP_TOOLS_ENABLED:
        return True
    if name not in HTTP_TOOLS_ENABLED:
        return False
    entry = HTTP_TOOLS_ENABLED[name]
    if isinstance(entry, dict):
        return bool(entry.get("enabled", True))
    return bool(entry)


def mock_tool_result(tool_name: str, arguments) -> dict:
    """Return a deterministic result for protocol and UI testing only."""
    if isinstance(arguments, str):
        try:
            arguments = json.loads(arguments)
        except json.JSONDecodeError:
            arguments = {"raw_arguments": arguments}
    return {"ok": True, "tool": tool_name, "mock": True, "arguments": arguments}


def make_handler(handlers: dict, bearer_token: str, log_file_path: Optional[str]):
    log_lock = threading.Lock()

    def log(line: str):
        msg = f"[{time.strftime('%Y-%m-%d %H:%M:%S')}] {line}"
        print(msg, flush=True)
        if log_file_path:
            try:
                with log_lock:
                    with open(log_file_path, "a", encoding="utf-8") as f:
                        f.write(msg + "\n")
            except Exception:
                pass

    handlers_snapshot = dict(handlers)

    class GatewayHandler(BaseHTTPRequestHandler):
        def log_message(self, format, *args):
            pass

        def _send_json(self, body: dict, status: int = 200):
            data = json.dumps(body, ensure_ascii=False).encode("utf-8")
            self.send_response(status)
            self.send_header("Content-Type", "application/json; charset=utf-8")
            self.send_header("Content-Length", str(len(data)))
            self.end_headers()
            self.wfile.write(data)

        def _check_auth(self) -> bool:
            if not bearer_token:
                return True
            hdr = self.headers.get("Authorization", "")
            return hdr == "Bearer " + bearer_token

        def do_GET(self):
            if not self._check_auth():
                self._send_json({"error": "unauthorized"}, status=401)
                return
            if self.path == "/health":
                tools = sorted(t for t in handlers_snapshot if is_tool_enabled(t))
                self._send_json({"ok": True, "tools": tools})
            elif self.path == "/api/agent/tools":
                tools = sorted(t for t in handlers_snapshot if is_tool_enabled(t))
                self._send_json({"tools": tools})
            else:
                self._send_json({"error": "not found", "path": self.path}, status=404)

        def do_POST(self):
            if not self._check_auth():
                self._send_json({"error": "unauthorized"}, status=401)
                return
            if self.path != "/api/agent/tool_call":
                self._send_json({"error": "not found", "path": self.path}, status=404)
                return

            length_hdr = self.headers.get("Content-Length", "0")
            try:
                length = int(length_hdr)
            except ValueError:
                length = 0
            raw = self.rfile.read(length) if length > 0 else b""
            try:
                req = json.loads(raw.decode("utf-8"))
            except (UnicodeDecodeError, json.JSONDecodeError) as e:
                self._send_json({"tool_call_id": "", "result": "", "error": f"bad json: {e}"}, status=400)
                return

            call_id = str(req.get("tool_call_id") or req.get("id") or "")
            tool_name = str(req.get("tool_name") or req.get("name") or "")
            args_str = req.get("arguments")
            if args_str is None or args_str == "":
                args_str = "{}"

            if not tool_name:
                self._send_json({"tool_call_id": call_id, "result": "", "error": "missing tool_name"}, status=400)
                return
            if tool_name not in handlers_snapshot:
                self._send_json({"tool_call_id": call_id, "result": "",
                                 "error": f"no handler registered for tool '{tool_name}'"}, status=404)
                return
            if not is_tool_enabled(tool_name):
                self._send_json({"tool_call_id": call_id, "result": "",
                                 "error": f"tool '{tool_name}' is disabled in httpTools config"}, status=403)
                return

            result = mock_tool_result(tool_name, args_str)
            result_str = json.dumps(result, ensure_ascii=False, separators=(",", ":"))
            log(f"[gateway] MOCK {tool_name:<20} call_id={call_id:<20}")
            self._send_json({"tool_call_id": call_id, "result": result_str, "error": ""})

    return GatewayHandler


def main():
    parser = argparse.ArgumentParser(description="Deterministic unified-tool mock")
    parser.add_argument("--port", type=int, default=8080)
    parser.add_argument("--bearer-token", default="")
    parser.add_argument("--log-file", default="")
    parser.add_argument("--host", default="127.0.0.1")
    parser.add_argument("--mock", action="store_true", help=argparse.SUPPRESS)
    parser.add_argument("--mock-tools", default=",".join(DEFAULT_MOCK_TOOLS),
                        help="Comma-separated tool names exposed by this mock")
    args = parser.parse_args()

    handlers = {name.strip(): "mock" for name in args.mock_tools.split(",") if name.strip()}
    print(f"[gateway] registered tools: {', '.join(sorted(handlers))}")
    print(f"[gateway] bearer_token={'<set, len=' + str(len(args.bearer_token)) + '>' if args.bearer_token else '<empty>'}")

    log_path = args.log_file or None
    if log_path:
        log_dir = os.path.dirname(log_path)
        if log_dir:
            os.makedirs(log_dir, exist_ok=True)
        open(log_path, "w", encoding="utf-8").close()

    handler_cls = make_handler(handlers, args.bearer_token, log_path)
    server = ThreadingHTTPServer((args.host, args.port), handler_cls)
    print()
    print(f"[gateway] listening on http://{args.host}:{args.port}/")
    print(f"[gateway] Ctrl+C to stop.")
    print()
    try:
        server.serve_forever()
    except KeyboardInterrupt:
        print("[gateway] stopping")
    finally:
        server.server_close()


if __name__ == "__main__":
    main()

#!/usr/bin/env python3
"""超窗工具结果的语义裁判服务。

定位：**只做裁判，不做判官。**
agent 侧的结构规则（R1/R2/R3/R5a/C3）全部照旧，只把「C1 已超窗」的那一组送来这里
问一句「这份结果跟当前正在做的事还相关吗」。任何异常都返回空分数，agent 侧按
「不可清」处理——退回改造前的行为，绝不因为模型出错而误删。

只用标准库 + transformers/torch，不引额外框架。

接口：
  GET  /health -> {"ok":true,"model":"..."}
  POST /score  {"items":[{"call_id":"...","head":"..."}],"now_context":"..."}
              -> {"scores":[{"call_id":"...","similarity":0.52}]}

为什么值得上（实测，2026-10-16 会话内 163 条超窗候选）：
  结构判据按「后续是否出现同标识」筛选，对这 163 条一条都筛不出来——163 条全部
  被判成仍在使用。原因是这种信号太松，弱巧合也算引用。判断「几十步前的结果跟我
  现在在做的事还有没有关系」靠的是语义关联，不是字符串重合。
  25 条人工标注上，sim<0.6 清 15 条、危险误删 0。
  样本量很小（正样本仅 4 条）且标注与判据同源，所以这里刻意做成「只裁决超窗子集、
  阈值保守、失败即保守」的形式，而不是让它独当一面。
"""
from __future__ import annotations

import json
import os
import sys
import threading
import time
from http.server import BaseHTTPRequestHandler, HTTPServer

MODEL_ID = os.environ.get("FAIRY_EMBED_MODEL", "BAAI/bge-small-zh-v1.5")
PORT = int(os.environ.get("FAIRY_EMBED_PORT", "8137"))
MAX_HEAD_CHARS = int(os.environ.get("FAIRY_EMBED_MAX_HEAD", "300"))
NOW_CHARS = int(os.environ.get("FAIRY_EMBED_NOW_CHARS", "800"))

_lock = threading.Lock()
_state = {"model": None, "tok": None, "torch": None, "err": None, "loaded_at": 0.0}


def load_model():
    """预热模型。失败不抛：健康检查会如实报告 unavailable，agent 侧据此退回保守行为。"""
    if _state["model"] is not None:
        return True
    try:
        import torch
        from transformers import AutoModel, AutoTokenizer

        t0 = time.time()
        tok = AutoTokenizer.from_pretrained(MODEL_ID)
        model = AutoModel.from_pretrained(MODEL_ID)
        model.eval()
        torch.set_num_threads(max(1, (os.cpu_count() or 4)))
        with _lock:
            _state.update(torch=torch, tok=tok, model=model, err=None,
                          loaded_at=time.time(), load_seconds=time.time() - t0)
        print(f"[stale-tiebreak] 模型就绪 {MODEL_ID} {time.time()-t0:.1f}s", file=sys.stderr, flush=True)
        return True
    except Exception as exc:  # noqa: BLE001 — 这里必须吞掉：服务不可用时 agent 侧走保守路径
        with _lock:
            _state["err"] = f"{type(exc).__name__}: {exc}"
        print(f"[stale-tiebreak] 模型加载失败: {_state['err']}", file=sys.stderr, flush=True)
        return False


def _embed(texts, batch=16):
    torch = _state["torch"]
    tokz = _state["tok"]
    model = _state["model"]
    outs = []
    with torch.no_grad():
        for i in range(0, len(texts), batch):
            b = tokz(texts[i:i + batch], padding=True, truncation=True,
                     max_length=256, return_tensors="pt")
            h = model(**b).last_hidden_state[:, 0]      # CLS pooling（bge 官方用法）
            h = torch.nn.functional.normalize(h, dim=-1)
            outs.append(h)
    return torch.cat(outs)


def score_items(items, now_context):
    if not items:
        return []
    if _state["model"] is None and not load_model():
        return []
    texts = [it["head"][:MAX_HEAD_CHARS] for it in items]
    anchor = " ".join((now_context or "").split())[:NOW_CHARS]
    if not anchor:
        return []
    vecs = _embed(texts)
    ref = _embed([anchor])[0]
    sims = (vecs @ ref).tolist()
    return [{"call_id": it.get("call_id", ""), "similarity": round(float(s), 4)}
            for it, s in zip(items, sims)]


class Handler(BaseHTTPRequestHandler):
    protocol_version = "HTTP/1.1"

    def _send(self, code, payload):
        body = json.dumps(payload, ensure_ascii=False).encode("utf-8")
        self.send_response(code)
        self.send_header("Content-Type", "application/json; charset=utf-8")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def log_message(self, *_args):
        pass  # 别把访问日志灌满 stderr：它会被落盘到 agent-stderr.log

    def do_GET(self):
        if self.path == "/health":
            ok = _state["model"] is not None
            self._send(200, {"ok": ok, "model": MODEL_ID, "loaded": ok,
                             "error": _state["err"], "load_seconds": round(_state["load_seconds"], 1) if ok else None})
            return
        self._send(404, {"error": "not found"})

    def do_POST(self):
        if self.path != "/score":
            self._send(404, {"error": "not found"})
            return
        try:
            n = int(self.headers.get("Content-Length") or 0)
            payload = json.loads(self.rfile.read(n).decode("utf-8"))
        except Exception as exc:  # noqa: BLE001
            self._send(400, {"error": f"bad request: {exc}"})
            return
        try:
            t0 = time.time()
            scores = score_items(payload.get("items") or [], payload.get("now_context") or "")
            print(f"[stale-tiebreak] scored {len(scores)} items in {(time.time()-t0)*1000:.0f}ms",
                  file=sys.stderr, flush=True)
            self._send(200, {"scores": scores, "elapsed_ms": int((time.time() - t0) * 1000)})
        except Exception as exc:  # noqa: BLE001 — 任何异常都退回空分数，agent 侧按不可清处理
            print(f"[stale-tiebreak] score failed: {type(exc).__name__}: {exc}", file=sys.stderr, flush=True)
            self._send(200, {"scores": [], "error": f"{type(exc).__name__}: {exc}"})


if __name__ == "__main__":
    threading.Thread(target=load_model, daemon=True).start()
    srv = HTTPServer(("127.0.0.1", PORT), Handler)
    print(f"[stale-tiebreak] listening on 127.0.0.1:{PORT} model={MODEL_ID}", file=sys.stderr, flush=True)
    srv.serve_forever()

"""判定 first_frame_image 的 1026 到底是**内容审核**还是 **data URL 通道**坏了。

关键技巧：内容审核的判决很早出。
  轮询到 Processing = 已过审（后面只是渲染，可以不等）
  轮询到 Fail/1026  = 被拦
所以默认**过审即停**，不用干等渲染完。要等完整结果加 --wait-complete。
"""
from __future__ import annotations

import argparse
import sys
import time
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent))
import mmclient as mm

DEV = Path("/home/user/Fairy/workspace/3d-pipeline-dev")

SAMPLES = {
    # 明确无害的对照组（已验证过审）
    "apple": DEV / "out/smoke_0.png",
    # 非 IP 的原创角色：判断是"特定 IP/这张图"被拦，还是"动漫女性角色图"普遍被拦
    "generic_anime": DEV / "e2e/generic_anime.png",
    # 同一张星见雅图的换裁法：半身、侧面
    "miyabi_bust": DEV / "e2e/miyabi_bust.png",
    "miyabi_side": DEV / "e2e/miyabi_v2_side.png",
    # 已知被拦，留作同条件复核
    "miyabi_front": DEV / "e2e/miyabi_v2_front.png",
}

PROMPT = "the subject moves slightly, subtle motion, static camera"


def poll_until_verdict(task_id: str, key: str, wait_complete: bool, max_wait: int = 420) -> tuple[str, str]:
    deadline = time.time() + max_wait
    while time.time() < deadline:
        data = mm.query_video(task_id, key=key)
        status = data.get("status", "?")
        if status == "Success":
            return "PASS", f"Success file_id={data.get('file_id')}"
        if status in ("Fail", "Failed"):
            base = data.get("base_resp", {})
            return "BLOCKED", f"{status} code={base.get('status_code')} msg={base.get('status_msg')}"
        if status == "Processing" and not wait_complete:
            return "PASS", "Processing（已过审，未等渲染完成）"
        print(f"    poll status={status}", flush=True)
        time.sleep(12)
    return "TIMEOUT", f"{max_wait}s 内无终态"


def main() -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument("--only", default=None, help="只跑某个样本名")
    ap.add_argument("--wait-complete", action="store_true", help="等到渲染完成（慢）")
    ap.add_argument("--duration", type=int, default=6)
    ap.add_argument("--resolution", default="768P")
    args = ap.parse_args()

    key = mm.load_key()
    results = {}

    for name, path in SAMPLES.items():
        if args.only and name != args.only:
            continue
        if not path.is_file():
            print(f"[{name}] 跳过，文件不存在: {path}", flush=True)
            results[name] = ("SKIP", "file missing")
            continue
        try:
            task_id = mm.submit_video(
                PROMPT, key=key, first_frame_image=mm.to_data_url(path),
                duration=args.duration, resolution=args.resolution,
            )
        except Exception as exc:
            print(f"[{name}] 提交失败 {exc}", flush=True)
            results[name] = ("SUBMIT_FAIL", str(exc)[:160])
            continue
        print(f"[{name}] submitted task_id={task_id} ({path.name}, {path.stat().st_size}B)", flush=True)
        try:
            verdict, detail = poll_until_verdict(task_id, key, args.wait_complete)
        except Exception as exc:
            verdict, detail = "POLL_ERROR", f"{type(exc).__name__}: {exc}"[:200]
        results[name] = (verdict, detail)
        print(f"[{name}] => {verdict}: {detail}", flush=True)
        time.sleep(3)

    print("\n===== 汇总 =====")
    for name, (verdict, detail) in results.items():
        print(f"{name:14s} {verdict:11s} {detail}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())

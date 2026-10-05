"""MiniMax 图像/视频 API 最小客户端（图像 + 图生视频）。

实测要点（2026-10-02，踩过的坑都在这）：
- 主机是 ``api.minimaxi.com``，不是 ``api.minimax.io``。
- 鉴权用 ``agent`` 自己的密钥（``config/local_secrets.json``）。仓库根目录那个
  ``MINIMAX_KEY_TXT.txt`` 是两行格式且**已失效**（status_code=2049 invalid api key）。
- 出图的 ``response_format`` 收 ``"base64"``（不是 ``"b64_json"``），
  图片在 ``data.image_base64``；业务错误看 ``base_resp.status_code``，**0 才是成功**。
  注意 HTTP 状态码永远是 200，鉴权失败也是 200，别只看 HTTP 码。
- ``subject_reference`` 现在只收**公网 URL**。裸 base64 和 ``data:`` 前缀都会被
  SSRF 校验拒掉（status_code=1000 disallowed image url）。
- 视频 ``resolution`` 只收 512P / 768P / 1080P。
"""

from __future__ import annotations

import base64
import json
import os
import time
import urllib.error
import urllib.request
from pathlib import Path

API_BASE = "https://api.minimaxi.com/v1"
DEFAULT_SECRETS = "/home/user/Fairy/config/local_secrets.json"
DEFAULT_SECRET_FIELD = "user_minimax-m3-1-flash"
LEGACY_KEY_FILE = "/home/user/Fairy/MINIMAX_KEY_TXT.txt"

RESOLUTIONS = ("512P", "768P", "1080P")
ASPECT_RATIOS = ("1:1", "16:9", "4:3", "2:3", "3:2", "9:16", "21:9")

OK = 0
RATE_LIMIT = 1002


class MiniMaxError(RuntimeError):
    pass


def load_key(secrets_file: str | None = None, field: str | None = None) -> str:
    """取 API key。只在内存里用，禁止打印 / 写日志 / 上报。"""
    sp = Path(secrets_file or os.environ.get("MINIMAX_SECRETS_FILE") or DEFAULT_SECRETS)
    if sp.is_file():
        data = json.loads(sp.read_text(encoding="utf-8"))
        name = field or os.environ.get("MINIMAX_SECRET_FIELD") or DEFAULT_SECRET_FIELD
        value = data.get(name)
        if value:
            return str(value).strip()
    kp = Path(LEGACY_KEY_FILE)
    if kp.is_file():
        lines = [ln.strip() for ln in kp.read_text(encoding="utf-8").splitlines()]
        if len(lines) >= 2 and lines[1]:
            return lines[1]
    raise MiniMaxError(f"no usable api key: check {sp} (field={field})")


def _request(url: str, key: str, payload: dict | None = None, timeout: int = 180) -> dict:
    data = json.dumps(payload).encode("utf-8") if payload is not None else None
    req = urllib.request.Request(
        url,
        data=data,
        headers={"Authorization": f"Bearer {key}", "Content-Type": "application/json"},
        method="POST" if payload is not None else "GET",
    )
    try:
        with urllib.request.urlopen(req, timeout=timeout) as resp:
            return json.loads(resp.read().decode("utf-8"))
    except urllib.error.HTTPError as exc:
        body = exc.read().decode("utf-8", "replace")[:800]
        raise MiniMaxError(f"HTTP {exc.code} on {url.split('?')[0]}: {body}") from None
    except urllib.error.URLError as exc:
        raise MiniMaxError(f"network error on {url.split('?')[0]}: {exc.reason}") from None


def _post(path: str, payload: dict, key: str, timeout: int = 180) -> dict:
    return _request(f"{API_BASE}{path}", key, payload, timeout)


def _get(path: str, params: str, key: str, timeout: int = 60) -> dict:
    return _request(f"{API_BASE}{path}?{params}", key, None, timeout)


def _check(base_resp: dict, where: str) -> None:
    code = base_resp.get("status_code", OK)
    if code != OK:
        raise MiniMaxError(f"{where}: status_code={code} {base_resp.get('status_msg')}")


# ---------------------------------------------------------------- 图像

def generate_image(
    prompt: str,
    *,
    key: str,
    model: str = "image-01",
    aspect_ratio: str = "1:1",
    n: int = 1,
    subject_reference: list[dict] | None = None,
    prompt_optimizer: bool = False,
    retries: int = 3,
    backoff: int = 5,
) -> list[str]:
    """出图，返回 base64 PNG 列表。"""
    if aspect_ratio not in ASPECT_RATIOS:
        raise MiniMaxError(f"aspect_ratio must be one of {ASPECT_RATIOS}, got {aspect_ratio}")
    payload: dict = {
        "model": model,
        "prompt": prompt,
        "aspect_ratio": aspect_ratio,
        "n": n,
        "response_format": "base64",
        "prompt_optimizer": prompt_optimizer,
    }
    if subject_reference:
        payload["subject_reference"] = subject_reference

    last: Exception | None = None
    for attempt in range(1, retries + 1):
        try:
            data = _post("/image_generation", payload, key)
            _check(data.get("base_resp", {}), "image_generation")
            imgs = [_strip_data_url(i) for i in (data.get("data") or {}).get("image_base64", []) if i]
            if not imgs:
                raise MiniMaxError("no image_base64 in response")
            return imgs
        except MiniMaxError as exc:
            last = exc
            if attempt < retries:
                wait = backoff * attempt  # 退避，别猛打接口
                print(f"  [retry {attempt}/{retries}] {exc} -> sleep {wait}s", flush=True)
                time.sleep(wait)
    raise last  # type: ignore[misc]


def _strip_data_url(b64: str) -> str:
    b64 = b64.strip()
    if b64.startswith("data:"):
        comma = b64.find(",")
        if comma > 0:
            return b64[comma + 1:]
    return b64


# ---------------------------------------------------------------- 本地 IO

def save_b64(b64: str, out_path: str | Path) -> Path:
    p = Path(out_path)
    p.parent.mkdir(parents=True, exist_ok=True)
    p.write_bytes(base64.b64decode(b64))
    return p


def load_b64(path: str | Path) -> str:
    return base64.b64encode(Path(path).read_bytes()).decode("ascii")


def to_data_url(path: str | Path, min_short_side: int = 300) -> str:
    """把本地图片包成 data URL。

    实测（2026-10-02）：
    - 视频首帧 ``first_frame_image`` **必须**带 ``data:image/...;base64,`` 前缀才收；
      裸 base64 会被拒（``status_code=2013 invalid params, first_frame_image: invalid image url``）。
    - 接口还要求**短边不小于 300px**（``2013 ... The minimum pixel on the short side of
      the image: 300px``）。三视图裁出来的侧面常常只有 130px 宽，直接提交必失败，
      所以这里自动放大到达标。
    - 但 ``subject_reference`` 连 data URL 都不收，只收公网 URL —— 两者规则不一致，别搞混。
    """
    p = Path(path)
    mime = {
        "png": "image/png", "jpg": "image/jpeg", "jpeg": "image/jpeg", "webp": "image/webp"
    }.get(p.suffix.lower().lstrip("."), "image/png")
    data = p.read_bytes()
    try:
        import io
        import sys as _sys

        import numpy as np
        from PIL import Image

        im = Image.open(io.BytesIO(data))
        if min(im.size) < min_short_side:
            scale = min_short_side / min(im.size)
            im = im.convert("RGB").resize((
                max(int(im.width * scale), min_short_side),
                max(int(im.height * scale), min_short_side),
            ))
        # 长宽比必须落在 0.5~2（实测 300x1636 -> 2013 aspect ratio not between 0.5 and 2）。
        # 超了只能给**短边补边**（复制边缘像素），保住完整主体，又不拉伸变形。
        w, h = im.size
        if max(w, h) / max(min(w, h), 1) > 2:
            im = im.convert("RGB")
            arr = np.array(im)
            if h >= w:  # 太高 -> 左右补边
                need = int(round(h / 2)) - w
                left = need // 2
                arr = np.pad(arr, ((0, 0), (left, need - left), (0, 0)), mode="edge")
            else:       # 太宽 -> 上下补边
                need = int(round(w / 2)) - h
                top = need // 2
                arr = np.pad(arr, ((top, need - top), (0, 0), (0, 0)), mode="edge")
            im = Image.fromarray(arr)
        buf = io.BytesIO()
        im.save(buf, format="PNG")
        data = buf.getvalue()
        mime = "image/png"
    except Exception as exc:  # 别静默吞掉：静默失败会让不合规的原图直接发出去
        print(f"[mmclient] 首帧预处理失败，将发原图：{type(exc).__name__}: {exc}",
              file=_sys.stderr, flush=True)
    return f"data:{mime};base64," + base64.b64encode(data).decode("ascii")


# ---------------------------------------------------------------- 视频

def submit_video(
    prompt: str,
    *,
    key: str,
    first_frame_image: str | None = None,
    model: str = "MiniMax-Hailuo-02",
    duration: int = 6,
    resolution: str = "768P",
) -> str:
    """提交图生视频，返回 task_id。

    ``first_frame_image`` 传 data URL（``data:image/png;base64,...``）。
    裸 base64 不收，会被拒。
    """
    if resolution not in RESOLUTIONS:
        raise MiniMaxError(f"resolution must be one of {RESOLUTIONS}, got {resolution}")
    payload: dict = {
        "model": model,
        "prompt": prompt,
        "duration": duration,
        "resolution": resolution,
    }
    if first_frame_image:
        payload["first_frame_image"] = first_frame_image
    data = _post("/video_generation", payload, key)
    _check(data.get("base_resp", {}), "video_generation")
    task_id = data.get("task_id")
    if not task_id:
        raise MiniMaxError(f"no task_id in response: {str(data)[:300]}")
    return task_id


def query_video(task_id: str, *, key: str) -> dict:
    data = _get("/query/video_generation", f"task_id={task_id}", key)
    _check(data.get("base_resp", {}), "query_video_generation")
    return data


def wait_video(
    task_id: str,
    *,
    key: str,
    interval: int = 15,
    max_wait: int = 900,
) -> str:
    """轮询到 Success，返回 file_id。"""
    deadline = time.time() + max_wait
    last_status = "?"
    while time.time() < deadline:
        data = query_video(task_id, key=key)
        last_status = data.get("status", "?")
        if last_status == "Success":
            file_id = data.get("file_id")
            if not file_id:
                raise MiniMaxError("Success but no file_id")
            return file_id
        if last_status in ("Fail", "Failed"):
            raise MiniMaxError(f"video task failed: {data.get('error_msg', 'no reason')}")
        print(f"  [poll] status={last_status}", flush=True)
        time.sleep(interval)
    raise MiniMaxError(f"timeout after {max_wait}s, last status={last_status}")


def file_download_url(file_id: str, *, key: str) -> str:
    data = _get("/files/retrieve", f"file_id={file_id}", key)
    url = (data.get("file") or {}).get("download_url")
    if not url:
        raise MiniMaxError(f"no download_url for file_id={file_id}")
    return url


def download(url: str, out_path: str | Path, timeout: int = 300) -> Path:
    p = Path(out_path)
    p.parent.mkdir(parents=True, exist_ok=True)
    with urllib.request.urlopen(url, timeout=timeout) as resp, p.open("wb") as fh:
        fh.write(resp.read())
    return p


def generate_video(
    prompt: str,
    *,
    key: str,
    first_frame: str | Path | None = None,
    out_path: str | Path,
    model: str = "MiniMax-Hailuo-02",
    duration: int = 6,
    resolution: str = "768P",
    interval: int = 15,
    max_wait: int = 900,
) -> Path:
    """完整流程：提交 → 轮询 → 下载落盘。

    ``first_frame`` 传**本地路径**，内部自动包成 data URL。
    """
    first = to_data_url(first_frame) if first_frame else None
    task_id = submit_video(
        prompt,
        key=key,
        first_frame_image=first,
        model=model,
        duration=duration,
        resolution=resolution,
    )
    print(f"[video] task_id={task_id} waiting...", flush=True)
    file_id = wait_video(task_id, key=key, interval=interval, max_wait=max_wait)
    url = file_download_url(file_id, key=key)
    path = download(url, out_path)
    print(f"[video] saved {path} bytes={path.stat().st_size}", flush=True)
    return path

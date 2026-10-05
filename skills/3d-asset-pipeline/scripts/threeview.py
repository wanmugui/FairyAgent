#!/usr/bin/env python3
"""出三视图：一次 turnaround sheet → 裁成 front / side / back。

用法:
    python3 threeview.py --desc "Hoshimi Miyabi, blonde, white+crimson jacket" -o out/miyabi
    python3 threeview.py --desc "..." -o out/x --ref-url https://.../ref.png

产物:
    <out>_sheet.png   三人并排原图
    <out>_front.png   左块（正面）
    <out>_side.png    中块（侧面）
    <out>_back.png    右块（背面）
    <out>_manifest.json

为什么是「一次出图再裁」而不是「三次分别出图」：实测见 references/threeview-decision.md。
简单说：三次独立采样角色会漂，一次出图更省 2/3 次数。
"""

from __future__ import annotations

import argparse
import json
import sys
import time
from pathlib import Path

sys.path.insert(0, str(Path(__file__).parent))
import mmclient as mm
from PIL import Image

# 显式逐个位置指定朝向 + 显式压制文字，这两个都是实测踩出来的，删了就会出 A-v1 那种
# 「正面+两个侧面、没有背面」或者「图上带小字标注」的废图。
SHEET_SUFFIX = (
    "A three-view character model sheet on a pure white background. "
    "Exactly three separate full-body figures, evenly spaced in one row, all the same height, "
    "standing on the same ground line, all wearing the identical costume with identical hair and colors. "
    "Left figure shows the FRONT of the character facing the viewer. "
    "Middle figure shows the character in SIDE PROFILE facing left. "
    "Right figure shows the BACK of the character, we see the back of her head and her back, not her face. "
    "Plain orthographic views, flat even studio lighting, no shadows, "
    "absolutely no text, no letters, no numbers, no labels, no watermark, no signature, no border."
)

VIEWS = ("front", "side", "back")


def build_prompt(desc: str, style: str) -> str:
    return f"{desc}. {style}. {SHEET_SUFFIX}"


def detect_bands(img, ink_threshold: int = 245, min_width: float = 0.02, min_gap: float = 0.03) -> list[tuple[int, int]]:
    """从 sheet 里切出**实际**的人形列区间，不要无脑三等分。

    实测踩坑：模型有时画 3 个、有时 6 个、有时把中间那个朝向画错。
    无脑三等分碰上 6 个就直接切错人。
    做法：按列算"有没有墨"，再把连续墨段并成 band，段间小缝隙合并。
    返回 [(left, right), ...]，left/right 是像素列号。
    """
    import numpy as np

    g = np.array(img.convert("L"))
    ink = (g < ink_threshold)
    col_has_ink = ink.sum(axis=0) >= 2  # 容忍零星噪点
    W = img.size[0]
    min_w = max(int(W * min_width), 4)
    min_gap_px = max(int(W * min_gap), 4)

    bands: list[tuple[int, int]] = []
    start = None
    for x, has in enumerate(col_has_ink):
        if has and start is None:
            start = x
        elif not has and start is not None:
            bands.append((start, x))
            start = None
    if start is not None:
        bands.append((start, W))

    # 合并靠得太近的段（可能是同一人物的剑/飘带被切开）
    merged: list[tuple[int, int]] = []
    for b in bands:
        if merged and b[0] - merged[-1][1] < min_gap_px:
            merged[-1] = (merged[-1][0], b[1])
        else:
            merged.append(b)
    return [b for b in merged if b[1] - b[0] >= min_w]


def cut_sheet(sheet_path: Path, out_stem: Path, out_dir: Path | None = None) -> dict:
    """按实际人形区间裁切。band 数不等于 3 时直接报错，绝不硬切。"""
    im = Image.open(sheet_path).convert("RGB")
    W, H = im.size
    bands = detect_bands(im)
    if len(bands) != len(VIEWS):
        raise ValueError(f"期望 {len(VIEWS)} 个人形，实测 {len(bands)} 个: {bands}（模型没照排，重试）")

    out = {"sheet_size": [W, H], "bands": bands, "panels": {}}
    for (left, right), name in zip(bands, VIEWS):
        p = (out_dir or out_stem.parent) / f"{out_stem.name}_{name}.png"
        im.crop((left, 0, right, H)).save(p)
        out["panels"][name] = {"path": str(p), "size": [right - left, H], "band": [left, right], "bytes": p.stat().st_size}

    # 侧面视图的人形投影宽度通常明显窄于正面；若中间那块并不窄，多半朝向没照排
    widths = {n: v["size"][0] for n, v in out["panels"].items()}
    out["panel_widths"] = widths
    out["side_narrower_than_front"] = widths["side"] < widths["front"]
    if not out["side_narrower_than_front"]:
        out["warning"] = "中间块不比正面窄，很可能不是侧面；请人眼/VLM 复核"
    return out


def main() -> int:
    ap = argparse.ArgumentParser(description="生成角色三视图（单图裁切）")
    ap.add_argument("--desc", required=True, help="角色外观描述，越具体越稳")
    ap.add_argument("-o", "--out", required=True, help="输出文件前缀，如 out/miyabi")
    ap.add_argument("--ref-url", default=None,
                    help="参考图的**公网 URL**。只有公网地址能用；base64/data URL 会被接口拒")
    ap.add_argument("--style", default="anime game character, cel shaded, full body, no background props")
    ap.add_argument("--ratio", default="16:9", choices=mm.ASPECT_RATIOS,
                    help="16:9 实测面板最高最完整；21:9 面板只有 416px 高，别用")
    ap.add_argument("--model", default="image-01")
    ap.add_argument("--attempts", type=int, default=3,
                    help="人形数不对就重试的次数上限（实测模型常画成 6 个或排错朝向）")
    args = ap.parse_args()

    stem = Path(args.out)
    stem.parent.mkdir(parents=True, exist_ok=True)
    key = mm.load_key()

    subject_reference = None
    if args.ref_url:
        subject_reference = [{"type": "character", "image": [args.ref_url], "fidelity": 0.8}]

    prompt = build_prompt(args.desc, args.style)

    last_err = ""
    for attempt in range(1, args.attempts + 1):
        tag = "" if attempt == 1 else f"_try{attempt}"
        sheet_path = stem.parent / f"{stem.name}{tag}_sheet.png"
        print(f"[threeview] 第 {attempt}/{args.attempts} 次出图 ratio={args.ratio} "
              f"ref_url={'yes' if args.ref_url else 'no'}", flush=True)
        images = mm.generate_image(
            prompt,
            key=key,
            model=args.model,
            aspect_ratio=args.ratio,
            n=1,
            subject_reference=subject_reference,
            retries=1,
        )
        mm.save_b64(images[0], sheet_path)
        print(f"[threeview] sheet {sheet_path} bytes={sheet_path.stat().st_size}", flush=True)

        try:
            manifest = cut_sheet(sheet_path, stem, out_dir=stem.parent)
        except ValueError as exc:
            last_err = str(exc)
            print(f"  [reject] {exc}", flush=True)
            time.sleep(8)
            continue

        manifest["prompt"] = prompt
        manifest["aspect_ratio"] = args.ratio
        manifest["used_subject_reference"] = bool(args.ref_url)
        manifest["attempts_used"] = attempt
        if manifest.get("warning"):
            print(f"  [warn] {manifest['warning']}", flush=True)
        mpath = stem.parent / f"{stem.name}_manifest.json"
        mpath.write_text(json.dumps(manifest, ensure_ascii=False, indent=2), encoding="utf-8")
        for name, info in manifest["panels"].items():
            print(f"  {name}: {info['size'][0]}x{info['size'][1]} band={info['band']} -> {info['path']}", flush=True)
        print(f"[threeview] manifest {mpath}")
        print("提醒：朝向是否照排仍需人眼/VLM 复核，脚本只能保证切对人形区间。")
        return 0

    print(f"❌ {args.attempts} 次都没出到 3 个人形：{last_err}", flush=True)
    return 1


if __name__ == "__main__":
    raise SystemExit(main())

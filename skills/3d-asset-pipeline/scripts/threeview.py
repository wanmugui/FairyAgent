#!/usr/bin/env python3
"""出三视图：一次 turnaround sheet → 裁成 front / side / back。

用法:
    python3 threeview.py --desc "Hoshimi Miyabi, blonde, white+crimson jacket" -o out/miyabi
    python3 threeview.py --desc "..." -o out/x --ref-url https://.../ref.png

    # 批量：一份元素清单（树/岩/屋/草…）一次跑完，-o 当输出目录用
    python3 threeview.py --list elements.json -o out/scene --sleep 6
    python3 threeview.py --list elements.json -o out/scene --dry-run   # 只打印计划，不调接口

产物:
    <out>_sheet.png   三人并排原图
    <out>_front.png   左块（正面）
    <out>_side.png    中块（侧面）
    <out>_back.png    右块（背面）
    <out>_manifest.json
    批量模式额外产出 <outdir>/_batch_manifest.json

为什么是「一次出图再裁」而不是「三次分别出图」：实测见 references/threeview-decision.md。
简单说：三次独立采样角色会漂，一次出图更省 2/3 次数。
"""

from __future__ import annotations

import argparse
import json
import shutil
import sys
import time
from pathlib import Path

import numpy as np

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

# 场景元素（树/岩/屋/草）不能套上面那段：那里写的是「full-body figures」「the BACK of her head」，
# 拿去出树会稳定跑偏。教程《HD-3D场景生成/模型生成提示词.txt》对这类元素要求的是
# **正交正视 / 正交右侧 / 正交顶**，所以这里单独一套措辞，视图也从 back 换成 top。
PROP_SHEET_SUFFIX = (
    "Three-view orthographic model sheet of ONE object on a pure white background. "
    "Exactly three renderings of the SAME object, evenly spaced in one row, identical in size and color. "
    "Left: orthographic FRONT view, camera perpendicular to the front face. "
    "Middle: orthographic RIGHT SIDE view. Right: orthographic TOP view, camera straight down. "
    "No perspective, no tilt, no rotation. Each rendering stays inside its own third of the image "
    "and does not touch or overlap its neighbours; wide empty white gaps between them. "
    "Nothing cut off, nothing floating, no ground line, no baseline, no horizon, "
    "no shadow joining them, no frame, no grid, no divider, no people. "
    "Flat even studio lighting. "
    "No text, no letters, no numbers, no labels, no watermark, no signature, no border."
)

# 第 2 次起追加这段。实测：薄物体（路灯杆）第一次常被画成「只画一个视图」，
# 或在画面左右边缘留两小块杂物，导致 band 数变成 1 或 4。重试时把数量和留白再钉一遍，
# 比原样重试有效得多。**不用「凑够 3 段就硬切」那套**——切错比报错更贵。
RETRY_SUFFIX = (
    "IMPORTANT: exactly THREE copies, no more, no fewer, wide empty gaps between them. "
    "Keep the left and right edges of the image completely empty."
)

VIEWS = ("front", "side", "back")
PROP_VIEWS = ("front", "side", "top")

CHARACTER_KINDS = ("character", "character_sheet", "角色")


# 默认 kind 显式定成 character：单元素模式（只有 --desc）一直是角色三视图，
# 保持默认即保持旧行为不变。**批量清单里不给 kind 会落到 "prop"**（见 resolve_plan），
# 因为场景元素才是批量这条路的实际用途。两套默认值曾经不一致，已统一成显式默认值。
DEFAULT_KIND = "character"


def views_for_kind(kind: str = DEFAULT_KIND) -> tuple:
    return VIEWS if kind in CHARACTER_KINDS else PROP_VIEWS


def suffix_for_kind(kind: str = DEFAULT_KIND) -> str:
    return SHEET_SUFFIX if kind in CHARACTER_KINDS else PROP_SHEET_SUFFIX


def build_prompt(desc: str, style: str, kind: str = DEFAULT_KIND) -> str:
    return f"{desc}. {style}. {suffix_for_kind(kind)}"


# 「墨」的灰度阈值。**245 是错的**，本轮拿 23 张实测 sheet 扫出来的：
# 245 会把物体下方大片柔和投影也算成墨，于是三个物体之间的白缝被阴影桥接成一片，
# 整张图只剩 1 条带（实测岩石那张 VLM 确认是 3 块石头，检测器只切出 2 段）。
# 200 在全语料上是严格改进：4 张原本误判的变 3，所有原本正确的仍是 3。
# **不能再往深调**：180 会把浅色元素（草地）切碎，160/140 直接切出 0 段。
INK_THRESHOLD_DEFAULT = 200


def detect_bands(img, ink_threshold: int = INK_THRESHOLD_DEFAULT,
                 min_width: float = 0.02, min_gap: float = 0.03) -> list:
    """从 sheet 里切出**实际**的主体列区间，不要无脑三等分。

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

    bands = []
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
    merged = []
    for b in bands:
        if merged and b[0] - merged[-1][1] < min_gap_px:
            merged[-1] = (merged[-1][0], b[1])
        else:
            merged.append(b)
    return [b for b in merged if b[1] - b[0] >= min_w]


def cut_sheet(sheet_path: Path, out_stem: Path, out_dir: Path = None, views=VIEWS,
              ink_threshold: int = INK_THRESHOLD_DEFAULT) -> dict:
    """按实际主体区间裁切。band 数不等于视图数时直接报错，绝不硬切。"""
    im = Image.open(sheet_path).convert("RGB")
    W, H = im.size
    bands = detect_bands(im, ink_threshold=ink_threshold)
    if len(bands) != len(views):
        raise ValueError(f"期望 {len(views)} 个主体，实测 {len(bands)} 个: {bands}（模型没照排，重试）")

    out = {"sheet_size": [W, H], "bands": bands, "views": list(views),
           "ink_threshold": ink_threshold, "panels": {}}
    for (left, right), name in zip(bands, views):
        p = (out_dir or out_stem.parent) / f"{out_stem.name}_{name}.png"
        im.crop((left, 0, right, H)).save(p)
        out["panels"][name] = {"path": str(p), "size": [right - left, H],
                               "band": [left, right], "bytes": p.stat().st_size}

    # 每格的实际墨迹高度。用途见下面的 top_view_height_ratio 告警：
    # 真顶视图是「从上往下看」，投影高度必然明显小于正/侧面；
    # 若第三格和第一格一样高，它就不是顶视图，多半是模型又画了一个立面。
    gray = np.array(im.convert("L"))
    heights = {}
    for (left, right), name in zip(bands, views):
        seg = gray[:, left:right]
        rows = np.where((seg < ink_threshold).sum(axis=1) >= 2)[0]
        heights[name] = int(rows.max() - rows.min()) if len(rows) else 0
    out["panel_ink_heights"] = heights

    widths = {n: v["size"][0] for n, v in out["panels"].items()}
    out["panel_widths"] = widths
    warnings = []
    if "side" in widths and "front" in widths:
        out["side_narrower_than_front"] = widths["side"] < widths["front"]
        if not out["side_narrower_than_front"]:
            warnings.append("中间块不比正面窄，很可能不是侧面；请人眼/VLM 复核")
    if "top" in heights and "front" in heights and heights["front"]:
        ratio = heights["top"] / heights["front"]
        out["top_view_height_ratio"] = round(ratio, 3)
        if ratio >= 0.7:
            warnings.append(
                f"第三格墨迹高度是第一格的 {ratio:.2f} 倍（>=0.7），它大概率不是顶视图，"
                f"而是模型又画了一个立面；顶视投影高度本应明显更矮。已照排但语义可能错，必须人眼/VLM 复核")
    if warnings:
        out["warning"] = "；".join(warnings)
    return out


# ------------------------------------------------------------------ 批量

DEFAULT_STYLE = "anime game character, cel shaded, full body, no background props"

# 实测硬约束：接口拒绝 length >= 1500 的 prompt，报
# `image_generation: status_code=2013 invalid params, prompt length must be less than 1500`。
# 这条不是文档里写的，是本轮跑批量时被拒才知道的。留 80 字余量（不同模型限制可能不同）。
MAX_PROMPT_CHARS = 1420


class PromptTooLong(ValueError):
    """提示词超长。**别重试**——重试用的是同一段超长文本，再试一次只是再拒一次。"""


def load_list(path) -> dict:
    with open(path, "r", encoding="utf-8") as fh:
        data = json.load(fh)
    if not isinstance(data, dict) or "elements" not in data:
        raise ValueError("元素清单必须是 JSON 对象且含 elements 数组")
    if not isinstance(data["elements"], list) or not data["elements"]:
        raise ValueError("elements 必须是非空数组")
    return data


def resolve_plan(data: dict, cli_style: str) -> list:
    """把清单 + CLI 默认值解析成逐元素执行计划。

    style 优先级：元素自带 style > 清单里按 kind 的 styles > CLI --style。
    **每个元素实际用的是哪一条必须打印出来**——默认 style 写死
    「anime game character…full body」，拿去出树/岩石/建筑会稳定出成人形，
    这是场景路线最常见的一类废图。
    """
    defaults = data.get("defaults") or {}
    styles = data.get("styles") or {}
    plan = []
    for i, el in enumerate(data["elements"]):
        if not isinstance(el, dict) or not (el.get("desc") or "").strip():
            # 空白 desc 也要拒：它不报错，只会拼出一条「只有 style + 后缀」的
            # 无主体提示词，出一张空图再靠 band 检测去失败，浪费一次出图。
            raise ValueError(f"elements[{i}] 必须含非空 desc")
        name = el.get("name") or f"item{i:02d}"
        if "/" in name or "\\" in name or name.startswith("."):
            raise ValueError(f"元素名 {name!r} 含路径分隔符，会写到输出目录外")
        kind = el.get("kind", "prop")
        if el.get("style"):
            style, style_src = el["style"], "element"
        elif kind in styles:
            style, style_src = styles[kind], f"styles[{kind}]"
        else:
            style, style_src = cli_style, "--style(未按 kind 覆盖!)"
        base = build_prompt(el["desc"], style, kind)
        try:
            retry_chars = len(prompt_for_attempt(base, 2))
        except PromptTooLong:
            retry_chars = len(base) + len(RETRY_SUFFIX) + 1
        record = {
            "name": name,
            "kind": kind,
            "desc": el["desc"],
            "style": style,
            "style_source": style_src,
            "views": list(views_for_kind(kind)),
            "ratio": el.get("ratio", defaults.get("ratio", "16:9")),
            "model": el.get("model", defaults.get("model", "image-01")),
            "attempts": int(el.get("attempts", defaults.get("attempts", 3))),
            "ref_url": el.get("ref_url"),
            "ink_threshold": None,      # 由 CLI 决定，运行时回填
            "prompt_chars": len(base),
            "retry_prompt_chars": retry_chars,
        }
        if retry_chars >= 1500:
            print(f"  [warn] {name}: 首次 {len(base)} 字符、重试 {retry_chars} 字符，"
                  f"重试会被接口拒（上限 1500）")
        plan.append(record)
    return plan


def prompt_for_attempt(base_prompt: str, attempt: int) -> str:
    """第 1 次用原提示词；重试时追加强调数量与留白的段落。超长直接报错。"""
    prompt = base_prompt if attempt <= 1 else f"{base_prompt} {RETRY_SUFFIX}"
    if len(prompt) >= 1500:
        raise PromptTooLong(
            f"提示词 {len(prompt)} 字符，超过接口上限 1500（第 {attempt} 次尝试）。"
            f"缩短 desc 或 styles[kind] 里的描述；脚本已中止，不会拿超长文本反复撞接口。")
    return prompt


def run_one(item: dict, out_dir: Path, key: str,
            ink_threshold: int = INK_THRESHOLD_DEFAULT) -> dict:
    """跑单个元素。返回结果记录，不抛异常——批量里单个失败不该中断整批。"""
    stem = out_dir / item["name"]
    views = tuple(item["views"])
    base_prompt = build_prompt(item["desc"], item["style"], item["kind"])
    record = dict(item)
    record.update(prompt=base_prompt, status="fail", attempts_used=0)

    subject_reference = None
    if item.get("ref_url"):
        subject_reference = [{"type": "character", "image": [item["ref_url"]], "fidelity": 0.8}]

    last_err = ""
    for attempt in range(1, item["attempts"] + 1):
        tag = "" if attempt == 1 else f"_try{attempt}"
        sheet_path = out_dir / f"{stem.name}{tag}_sheet.png"
        prompt = prompt_for_attempt(base_prompt, attempt)
        print(f"[threeview] {item['name']} 第 {attempt}/{item['attempts']} 次出图 "
              f"kind={item['kind']} views={'/'.join(views)} ratio={item['ratio']} "
              f"style_src={item['style_source']} "
              f"{'retry+reminder' if attempt > 1 else 'first-try'}", flush=True)
        record["prompt_chars"] = len(prompt)
        try:
            images = mm.generate_image(
                prompt, key=key, model=item["model"], aspect_ratio=item["ratio"],
                n=1, subject_reference=subject_reference, retries=1,
            )
            mm.save_b64(images[0], sheet_path)
        except PromptTooLong as exc:
            last_err = f"prompt_too_long: {exc}"
            print(f"  [abort] {last_err}", flush=True)
            break
        except Exception as exc:                      # 接口/网络错误也只记不抛
            last_err = f"generate: {exc}"
            print(f"  [error] {last_err}", flush=True)
            break
        print(f"[threeview] sheet {sheet_path} bytes={sheet_path.stat().st_size}", flush=True)

        try:
            manifest = cut_sheet(sheet_path, stem, out_dir=out_dir, views=views,
                                 ink_threshold=ink_threshold)
        except ValueError as exc:
            last_err = str(exc)
            print(f"  [reject] {exc}", flush=True)
            time.sleep(8)
            continue

        manifest["prompt"] = prompt
        manifest["aspect_ratio"] = item["ratio"]
        manifest["kind"] = item["kind"]
        manifest["style_source"] = item["style_source"]
        manifest["used_subject_reference"] = bool(item.get("ref_url"))
        manifest["attempts_used"] = attempt
        if manifest.get("warning"):
            print(f"  [warn] {manifest['warning']}", flush=True)
        mpath = out_dir / f"{stem.name}_manifest.json"
        mpath.write_text(json.dumps(manifest, ensure_ascii=False, indent=2), encoding="utf-8")
        for name, info in manifest["panels"].items():
            print(f"  {name}: {info['size'][0]}x{info['size'][1]} band={info['band']} -> {info['path']}", flush=True)
        # 通过的那张 sheet 必须占住「规范名」。否则重试才成功时，`<name>_sheet.png`
        # 里留的还是第 1 次被拒的废图，而 panel 是从 _tryN_sheet 裁的——
        # 人对着 _sheet.png 复核会看错图（本轮就被这个坑误导过一次）。
        canonical = out_dir / f"{stem.name}_sheet.png"
        if sheet_path != canonical:
            shutil.copyfile(sheet_path, canonical)
            print(f"  [note] 第 {attempt} 次才通过，已把通过的那张同步为规范名 {canonical.name}", flush=True)

        record.update(status="ok", attempts_used=attempt, manifest=str(mpath),
                      sheets=[str(sheet_path)],
                      panels={k: v["path"] for k, v in manifest["panels"].items()},
                      warning=manifest.get("warning"))
        return record

    record["error"] = last_err
    record["error_kind"] = ("prompt_too_long" if last_err.startswith("prompt_too_long")
                            else "api_or_network" if last_err.startswith("generate")
                            else "layout_bands_mismatch" if last_err else "unknown")
    if last_err.startswith("期望"):
        record["attempts_used"] = item["attempts"]
    return record


def run_batch(args) -> int:
    data = load_list(args.list)
    plan = resolve_plan(data, args.style)
    if args.only:
        want = [n.strip() for n in args.only.split(",") if n.strip()]
        unknown = [n for n in want if n not in {p["name"] for p in plan}]
        if unknown:
            raise ValueError(f"--only 指定了清单里不存在的元素: {unknown}")
        plan = [p for p in plan if p["name"] in want]
        print(f"[threeview] --only 过滤后剩 {len(plan)} 个元素")
    out_dir = Path(args.out)
    out_dir.mkdir(parents=True, exist_ok=True)

    print(f"[threeview] 批量：{len(plan)} 个元素 -> {out_dir}")
    for item in plan:
        print(f"  - {item['name']:<16} kind={item['kind']:<10} views={'/'.join(item['views']):<16} "
              f"style_src={item['style_source']}")
    if args.dry_run:
        print("[threeview] --dry-run：只解析计划，未调用出图接口。")
        return 0

    key = mm.load_key()
    results = []
    for i, item in enumerate(plan):
        if i:
            time.sleep(args.sleep)
        results.append(run_one(item, out_dir, key, ink_threshold=args.ink_threshold))

    # --only 补跑**不能覆盖**整批汇总：踩过——只跑一个元素就把上一次 6 元素的
    # 汇总冲成 1 元素，证据就没了。按元素名合并，以本次结果覆盖同名旧记录。
    spath = out_dir / "_batch_manifest.json"
    merged = {r["name"]: r for r in results}
    if spath.exists():
        try:
            old = json.loads(spath.read_text(encoding="utf-8"))
            for r in old.get("results", []):
                merged.setdefault(r["name"], r)
        except (ValueError, OSError) as exc:
            print(f"  [warn] 旧汇总读不出来，按新结果重建: {exc}", flush=True)
    results = list(merged.values())
    ok = [r for r in results if r["status"] == "ok"]
    summary = {
        "list": str(Path(args.list).resolve()),
        "total": len(results), "ok": len(ok), "failed": len(results) - len(ok),
        "sleep_between": args.sleep,
        "results": results,
    }
    spath.write_text(json.dumps(summary, ensure_ascii=False, indent=2), encoding="utf-8")
    print(f"[threeview] 批量汇总 {len(ok)}/{len(results)} 成功 -> {spath}")
    for r in results:
        if r["status"] != "ok":
            print(f"  X {r['name']}: {r.get('error')}")
    print("提醒：每个元素的三视图是否照排仍要人眼/VLM 复核；脚本只保证切对主体区间。")
    return 0 if ok else 1


def main() -> int:
    ap = argparse.ArgumentParser(description="生成三视图（单图裁切；--list 走批量）")
    src = ap.add_mutually_exclusive_group(required=True)
    src.add_argument("--desc", help="单个主体的外观描述，越具体越稳")
    src.add_argument("--list", help="元素清单 JSON（批量场景元素用）")
    ap.add_argument("-o", "--out", required=True,
                    help="单元素：输出文件前缀 out/x；--list：输出目录 out/scene")
    ap.add_argument("--ref-url", default=None,
                    help="参考图的**公网 URL**。只有公网地址能用；base64/data URL 会被接口拒")
    ap.add_argument("--style", default=DEFAULT_STYLE)
    ap.add_argument("--sleep", type=float, default=5.0,
                    help="批量时元素之间的间隔秒数（速率控制，默认 5；批量调用别设 0）")
    ap.add_argument("--only", default=None,
                    help="--list 模式下只跑这些元素（逗号分隔的名字），用于批量补跑，省掉重出已成功的图")
    ap.add_argument("--dry-run", action="store_true",
                    help="只解析清单并打印每个元素实际用的 style 与视图，不调用出图接口")
    ap.add_argument("--ratio", default="16:9", choices=mm.ASPECT_RATIOS,
                    help="16:9 实测面板最高最完整；21:9 面板只有 416px 高，别用")
    ap.add_argument("--model", default="image-01")
    ap.add_argument("--ink-threshold", type=int, default=INK_THRESHOLD_DEFAULT,
                    help="列墨迹的灰度阈值（默认 200）。调更浅会把柔和阴影算成墨、"
                         "把三个物体桥接成一条带；调更深会切碎浅色元素。别乱动")
    ap.add_argument("--attempts", type=int, default=3,
                    help="主体数不对就重试的次数上限（实测模型常画成 6 个或排错朝向）")
    args = ap.parse_args()

    if args.list:
        return run_batch(args)

    stem = Path(args.out)
    stem.parent.mkdir(parents=True, exist_ok=True)
    key = mm.load_key()

    subject_reference = None
    if args.ref_url:
        subject_reference = [{"type": "character", "image": [args.ref_url], "fidelity": 0.8}]

    prompt = build_prompt(args.desc, args.style, "character")

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
            manifest = cut_sheet(sheet_path, stem, out_dir=stem.parent, views=VIEWS,
                                 ink_threshold=args.ink_threshold)
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

    print(f"X {args.attempts} 次都没出到 3 个人形：{last_err}", flush=True)
    return 1


if __name__ == "__main__":
    raise SystemExit(main())

#!/usr/bin/env python3
"""model.py —— OpenSCAD 建模闭环：多视角渲染预览 / 导出 / 参数体检。

本机固定是 OpenSCAD **2021.01**，有三个必须遵守的坑，写在这里省得每次重踩：

  1. ``--render`` **必须带值**（``--render=true``）。不给值它会去开 GUI 窗口，
     在无头机器上直接卡死。
  2. PNG 渲染要 OpenGL，必须走 ``xvfb-run -a``。纯导出（STL/3MF）不需要。
  3. ``--camera`` 只收 **6 个数**（矢量相机）或 **7 个数**（云台相机，末位是距离）。
     给别的个数会被静默忽略或直接报错。

子命令：
  render  多视角 PNG 预览（给人看、给模型自己看）
  export  导出 STL/3MF/OFF
  check   参数范围 + 体积/三角面数体检
"""

from __future__ import annotations

import argparse
import re
import struct
import subprocess
import sys
import tempfile
from pathlib import Path

OPENSCAD = "/usr/bin/openscad"

# 矢量相机（6 个数）：前 3 个是相机平移，后 3 个是模型旋转。
# 距离交给 --viewall / --autocenter 自动算，所以这里不写死。
VIEW_PRESETS: dict[str, list[float]] = {
    "iso": [1.3, -1.6, 1.2, 0, 0, 0],
    "front": [0, -2.0, 0, 0, 0, 0],
    "back": [0, 2.0, 0, 0, 0, 0],
    "left": [-2.0, 0, 0, 0, 0, 0],
    "right": [2.0, 0, 0, 0, 0, 0],
    "top": [0, 0, 2.0, 0, 0, 0],
    "bottom": [0, 0, -2.0, 0, 0, 0],
}

# 模型里用注释声明参数合法区间：// @param width 5 120
BOUNDS_RE = re.compile(
    r"//\s*@param\s+([A-Za-z_]\w*)\s+(-?\d+(?:\.\d+)?)\s+(-?\d+(?:\.\d+)?)"
)
ASSIGN_RE = re.compile(r"^\s*([A-Za-z_]\w*)\s*=\s*([^;]+);", re.M)

EXIT_USAGE = 2
EXIT_RANGE = 3
EXIT_LIMIT = 4
EXIT_OPENSCAD = 5


class ModelError(RuntimeError):
    def __init__(self, msg: str, code: int = 1) -> None:
        super().__init__(msg)
        self.code = code


# --------------------------------------------------------------------------- #
# 调用 OpenSCAD
# --------------------------------------------------------------------------- #


def run_openscad(args: list[str], *, xvfb: bool, timeout: int = 300) -> subprocess.CompletedProcess:
    """跑一次 openscad。xvfb=True 用于 PNG 渲染。"""
    cmd: list[str] = []
    if xvfb:
        cmd += ["xvfb-run", "-a"]
    cmd += [OPENSCAD] + args

    try:
        proc = subprocess.run(
            cmd, capture_output=True, text=True, timeout=timeout, check=False
        )
    except FileNotFoundError as exc:  # pragma: no cover - 环境缺失
        raise ModelError(f"找不到可执行文件：{cmd[0] if not xvfb else 'xvfb-run'}", EXIT_OPENSCAD) from exc
    except subprocess.TimeoutExpired as exc:
        raise ModelError(f"openscad 超时（{timeout}s）", EXIT_OPENSCAD) from exc
    return proc


def define_args(overrides: list[str]) -> list[str]:
    """把 k=v 转成 -D k=v。"""
    out: list[str] = []
    for item in overrides:
        if "=" not in item:
            raise ModelError(f"--set 需要 k=v 形式，收到：{item}", EXIT_USAGE)
        k, v = item.split("=", 1)
        if not re.match(r"^[A-Za-z_]\w*$", k):
            raise ModelError(f"参数名不合法：{k}", EXIT_USAGE)
        out += ["-D", f"{k}={v}"]
    return out


# --------------------------------------------------------------------------- #
# STL 解析 + 网格统计
# --------------------------------------------------------------------------- #


def parse_stl(path: Path) -> list[tuple[tuple[float, float, float], ...]]:
    """读 STL，返回三角面列表。二进制优先（按长度判定，避免 solid 开头的误判）。"""
    data = path.read_bytes()
    if len(data) < 84:
        raise ModelError(f"STL 太小，不像有效文件：{path}（{len(data)} 字节）")

    count = struct.unpack("<I", data[80:84])[0]
    if len(data) == 84 + 50 * count and count > 0:
        tris = []
        for i in range(count):
            off = 84 + i * 50
            v = struct.unpack("<12f", data[off : off + 48])
            tris.append((v[3:6], v[6:9], v[9:12]))
        return tris

    # ASCII 回退
    tris = []
    verts = re.findall(
        r"vertex\s+(\S+)\s+(\S+)\s+(\S+)", data.decode("utf-8", "replace")
    )
    for i in range(0, len(verts) - 2, 3):
        tris.append(tuple(tuple(float(c) for c in verts[i + k]) for k in range(3)))
    if not tris:
        raise ModelError(f"解析不出三角面：{path}")
    return tris


def mesh_stats(tris: list[tuple[tuple[float, float, float], ...]]) -> dict:
    """用散度定理算有向体积（闭合实体时为正），并统计包围盒。"""
    vol = 0.0
    lo = [float("inf")] * 3
    hi = [float("-inf")] * 3
    for a, b, c in tris:
        vol += (
            a[0] * (b[1] * c[2] - b[2] * c[1])
            - a[1] * (b[0] * c[2] - b[2] * c[0])
            + a[2] * (b[0] * c[1] - b[1] * c[0])
        ) / 6.0
        for v in (a, b, c):
            for k in range(3):
                lo[k] = min(lo[k], v[k])
                hi[k] = max(hi[k], v[k])
    return {
        "volume": abs(vol),
        "facets": len(tris),
        "bbox": [hi[k] - lo[k] for k in range(3)],
        "size": [hi[k] for k in range(3)],
    }


# --------------------------------------------------------------------------- #
# 参数解析
# --------------------------------------------------------------------------- #


def read_bounds(model: Path) -> dict[str, tuple[float, float]]:
    """从模型源码里读 // @param name min max 声明。"""
    text = model.read_text(encoding="utf-8", errors="replace")
    bounds: dict[str, tuple[float, float]] = {}
    for name, lo, hi in BOUNDS_RE.findall(text):
        lo_f, hi_f = float(lo), float(hi)
        if lo_f >= hi_f:
            raise ModelError(
                f"@{name} 的区间写反了：{lo_f} >= {hi_f}", EXIT_RANGE
            )
        bounds[name] = (lo_f, hi_f)
    return bounds


def read_defaults(model: Path) -> dict[str, str]:
    text = model.read_text(encoding="utf-8", errors="replace")
    return {m.group(1): m.group(2).strip() for m in ASSIGN_RE.finditer(text)}


# --------------------------------------------------------------------------- #
# 子命令：render
# --------------------------------------------------------------------------- #


def cmd_render(args: argparse.Namespace) -> int:
    model = Path(args.model)
    if not model.is_file():
        raise ModelError(f"模型文件不存在：{model}", EXIT_USAGE)

    views = (
        list(VIEW_PRESETS) if args.views == "all" else
        [v.strip() for v in args.views.split(",") if v.strip()]
    )
    unknown = [v for v in views if v not in VIEW_PRESETS]
    if unknown:
        raise ModelError(
            f"未知视角 {unknown}，可选：{', '.join(VIEW_PRESETS)}", EXIT_USAGE
        )

    outdir = Path(args.outdir) if args.outdir else model.parent / "preview"
    outdir.mkdir(parents=True, exist_ok=True)
    dargs = define_args(args.set)

    made: list[str] = []
    for view in views:
        cam = ",".join(f"{v:g}" for v in VIEW_PRESETS[view])
        png = outdir / f"{model.stem}-{view}.png"
        # 注意 2021.01：--render 必须带值，PNG 必须走 xvfb。
        cmd_args = [
            "-o", str(png),
            f"--imgsize={args.size},{args.size}",
            "--render=true",
            "--viewall",
            "--autocenter",
            "--projection=perspective",
            f"--camera={cam}",
            *dargs,
            str(model),
        ]
        proc = run_openscad(cmd_args, xvfb=True)
        if proc.returncode != 0 or not png.is_file() or png.stat().st_size == 0:
            raise ModelError(
                f"渲染 {view} 失败（退出码 {proc.returncode}）：\n{proc.stderr.strip()[-1500:]}",
                EXIT_OPENSCAD,
            )
        made.append(str(png))
        print(f"[render] {view:>6} -> {png}  ({png.stat().st_size} 字节)")

    print("\n预览图路径（结论里要贴出来）：")
    for p in made:
        print(f"  {p}")
    return 0


# --------------------------------------------------------------------------- #
# 子命令：export
# --------------------------------------------------------------------------- #


def cmd_export(args: argparse.Namespace) -> int:
    model = Path(args.model)
    if not model.is_file():
        raise ModelError(f"模型文件不存在：{model}", EXIT_USAGE)

    fmt = args.format
    out = Path(args.out) if args.out else model.with_suffix(f".{fmt}")
    out.parent.mkdir(parents=True, exist_ok=True)
    dargs = define_args(args.set)

    cmd_args = ["-o", str(out), *dargs, str(model)]
    proc = run_openscad(cmd_args, xvfb=False)
    if proc.returncode != 0 or not out.is_file() or out.stat().st_size == 0:
        # 再带 xvfb 试一次（少数环境导出也会碰 OpenGL）
        proc = run_openscad(cmd_args, xvfb=True)
    if proc.returncode != 0 or not out.is_file() or out.stat().st_size == 0:
        raise ModelError(
            f"导出 {fmt} 失败（退出码 {proc.returncode}）：\n{proc.stderr.strip()[-1500:]}",
            EXIT_OPENSCAD,
        )
    print(f"[export] {fmt} -> {out}  ({out.stat().st_size} 字节)")
    return 0


# --------------------------------------------------------------------------- #
# 子命令：check
# --------------------------------------------------------------------------- #


def cmd_check(args: argparse.Namespace) -> int:
    model = Path(args.model)
    if not model.is_file():
        raise ModelError(f"模型文件不存在：{model}", EXIT_USAGE)

    problems: list[str] = []

    # 1) 参数范围体检
    bounds = read_bounds(model)
    defaults = read_defaults(model)
    overrides: dict[str, str] = {}
    for item in args.set:
        k, v = item.split("=", 1) if "=" in item else (item, "")
        overrides[k] = v

    print("[check] 参数范围")
    if not bounds:
        print("  模型里没有 // @param 声明，跳过范围校验")
    for name, (lo, hi) in sorted(bounds.items()):
        shown = overrides.get(name, defaults.get(name, "?"))
        line = f"  {name:<12} 区间 [{lo:g}, {hi:g}]  当前 {shown}"
        print(line)
        if name in overrides:
            try:
                val = float(overrides[name])
            except ValueError:
                problems.append(f"{name} 的覆盖值不是数字：{overrides[name]}")
                continue
            if not (lo <= val <= hi):
                problems.append(f"{name}={val:g} 越界，合法区间 [{lo:g}, {hi:g}]")
                print("      ✗ 越界")
                continue
        print("      ✓")

    # 声明了区间却没被赋值/没默认值，提醒但不拦
    for name in sorted(set(overrides) - set(bounds)):
        print(f"  {name:<12} 无区间声明，按 {overrides[name]} 使用（无法校验）")

    # 2) 几何体检：导出一份 STL 回来量
    with tempfile.TemporaryDirectory() as td:
        stl = Path(td) / "probe.stl"
        proc = run_openscad(
            ["-o", str(stl), *define_args(args.set), str(model)], xvfb=False
        )
        if proc.returncode != 0 or not stl.is_file():
            proc = run_openscad(
                ["-o", str(stl), *define_args(args.set), str(model)], xvfb=True
            )
        if proc.returncode != 0 or not stl.is_file():
            raise ModelError(
                f"为体检导出 STL 失败（退出码 {proc.returncode}）：\n"
                f"{proc.stderr.strip()[-1500:]}",
                EXIT_OPENSCAD,
            )
        stats = mesh_stats(parse_stl(stl))

    vol = stats["volume"]
    facets = stats["facets"]
    bb = stats["bbox"]
    print("\n[check] 几何")
    print(f"  体积      {vol:10.1f} mm^3")
    print(f"  三角面数  {facets:10d}")
    print(f"  包围盒    {bb[0]:.2f} x {bb[1]:.2f} x {bb[2]:.2f} mm")

    if facets == 0:
        problems.append("模型是空的（0 个三角面）")
    if vol <= 0:
        problems.append("体积为 0，模型可能没有实体（是不是全是壳？）")

    # 3) 硬上限
    print("\n[check] 上限")
    if args.max_volume is not None:
        ok = vol <= args.max_volume
        print(f"  体积 <= {args.max_volume:g} mm^3 : {'✓' if ok else '✗ 超了'}")
        if not ok:
            problems.append(f"体积 {vol:.1f} 超过上限 {args.max_volume:g} mm^3")
    else:
        print("  体积上限：未设置")
    if args.max_facets is not None:
        ok = facets <= args.max_facets
        print(f"  面数 <= {args.max_facets:d}       : {'✓' if ok else '✗ 超了'}")
        if not ok:
            problems.append(f"面数 {facets} 超过上限 {args.max_facets}")
    else:
        print("  面数上限：未设置")

    if problems:
        print("\n[check] 不通过：")
        for p in problems:
            print(f"  - {p}")
        code = EXIT_RANGE if any("越界" in p for p in problems) else EXIT_LIMIT
        return code

    print("\n[check] 通过")
    return 0


# --------------------------------------------------------------------------- #


def main(argv: list[str] | None = None) -> int:
    ap = argparse.ArgumentParser(
        prog="model.py",
        description="OpenSCAD 建模闭环：render 多视角预览 / export 导出 / check 体检",
    )
    sub = ap.add_subparsers(dest="cmd", required=True)

    # render
    p = sub.add_parser("render", help="渲染多视角 PNG 预览")
    p.add_argument("model")
    p.add_argument("--views", default="iso,front,top",
                   help="逗号分隔的视角，或 all。可选：iso/front/back/left/right/top/bottom")
    p.add_argument("--outdir", help="输出目录，默认 <模型目录>/preview")
    p.add_argument("--size", type=int, default=600, help="边长像素，默认 600")
    p.add_argument("--set", action="append", default=[], help="参数覆盖 k=v，可重复")
    p.set_defaults(func=cmd_render)

    # export
    p = sub.add_parser("export", help="导出 STL/3MF/OFF")
    p.add_argument("model")
    p.add_argument("--format", default="stl", choices=["stl", "3mf", "off", "csg", "amf"])
    p.add_argument("--out", help="输出文件，默认与模型同名")
    p.add_argument("--set", action="append", default=[], help="参数覆盖 k=v，可重复")
    p.set_defaults(func=cmd_export)

    # check
    p = sub.add_parser("check", help="参数范围 + 体积/面数体检")
    p.add_argument("model")
    p.add_argument("--set", action="append", default=[], help="参数覆盖 k=v，可重复")
    p.add_argument("--max-volume", type=float, help="体积上限 mm^3")
    p.add_argument("--max-facets", type=int, help="三角面数上限")
    p.set_defaults(func=cmd_check)

    args = ap.parse_args(argv)
    try:
        return args.func(args)
    except ModelError as exc:
        print(f"错误：{exc}", file=sys.stderr)
        return exc.code


if __name__ == "__main__":
    raise SystemExit(main())

#!/usr/bin/env python3
"""bpy 建模通道的 MCP server（stdio 传输）。

为什么是这个形态
----------------
1. bpy 只发 cp313 wheel，bpy_model.py 必须在 bpyenv 的 python3.13 里跑。
   mcp 2.3.0 已一并装进同一个 bpyenv（`python3.13 -m pip install mcp`），
   于是 server 与建模逻辑**同解释器、同进程**，不需要跨解释器桥接。
2. 建模逻辑直接复用 bpy_model.py 的函数而不是复制一份，避免两套代码跑偏。
   bpy_model.py 有 `if __name__ == "__main__"` 卫哨，import 是安全的；
   每次建模前 build_scene() 会 reset_scene()，天然提供调用间的状态隔离。

⚠️ 最重要的一条：stdio 传输下 **stdout 就是协议通道**。bpy_model.py 里到处是
   print()，bpy 自己也会往 stdout 写。任何一个字节漏出去都会污染 JSON-RPC 帧、
   把连接搞崩。所以所有对 bpy_model 的调用都在 redirect_stdout 里跑，并把捕获
   到的文本作为工具结果的一部分回给调用方——既不污染协议，又把日志还给人看。

启动：
    /home/user/miniforge3/envs/bpyenv/bin/python3.13 bpy_mcp_server.py
"""

from __future__ import annotations

import contextlib
import io
import os
import platform
import sys
from pathlib import Path
from typing import Any

# ⚠️ 为什么要在 import bpy 之前动文件描述符。
#
# stdio 传输下 **fd 1 就是协议通道**，bpy 往 stdout 写的任何字节都会把 JSON-RPC 帧
# 打成非法 JSON。实测 `import bpy` 会写两行（且是**延迟**触发的——addon 惰性注册，
# 可能在 import 返回之后才打出来，所以只能在 import 之前就把 fd 1 挪走）：
#     Exception in module register(): .../io_scene_gltf2/__init__.py
#     Unable to initialize audio
#
# 做法：把 fd 1 永久指向 stderr，**不还原**；协议通道另开一个 dup 出来的 fd，
# 挂到 sys.stdout 上。mcp 的 stdio_server() 会检测 sys.stdout 是否还基于 fd 1
# ——不是的话它就走 stream.buffer 分支，正好用上我们给它这个干净的通道。
#
# 别再对 fd 1 做「用完还原」那套：mcp 自己在 run 时会把 fd 1 改道成私有 fd，
# 两边互相踩 fd 1 会直接把 server 进程搞死（踩过一次，别再试）。
_PROTO_FD = os.dup(1)
os.dup2(2, 1)

# bpy 必须在 bmesh 之前 import，否则 ImportError（见 3d-model 技能里的已知坑）
import bpy  # noqa: E402

sys.path.insert(0, str(Path(__file__).resolve().parent))

import bpy_model as M  # noqa: E402
import mcp  # noqa: E402
from mcp.server.mcpserver import MCPServer  # noqa: E402

mcp_ver = getattr(mcp, "__version__", "2.x")

# 建模是个几分钟起步的重活，超时给宽松一点
DEFAULT_TIMEOUT_MS = 600_000

# ⚠️ 四个工具**必须**写成 async def，不能写普通 def。
# mcp SDK 会把同步函数丢到 worker 线程里执行，而 bpy 不可重入：
# 在非主线程调 M.reset_scene() 会直接 SIGSEGV（实测 rc=139，core dumped），
# 表现为 MCP 连接毫无征兆地断开、server 进程静默退出。
# 写成 async def 后，handler 在事件循环（主线程）上被 await，bpy 才跑在它该跑的线程上。
# 这不是风格问题，是踩过一次段错误换来的，别改回去。

server = MCPServer(
    name="bpy-model",
    title="bpy 建模通道",
    description=(
        "用 bpy 纯数据 API 做 3D 建模：建场景、造单体块、存 .blend、渲图/渲序列。"
        "无 GUI，库模式运行——注意这里不能用 bpy.ops 做建模，只能走 bmesh 数据 API。"
    ),
    instructions=(
        "先调 bpy_doctor 确认环境。建模用 bpy_scene（给完整场景 JSON）或 "
        "bpy_asset（要一个单体块就用它）。bpy_render 打开已有 .blend 渲图。"
        "已知坑：引擎只有 BLENDER_EEVEE；bmesh.ops 没有 create_torus，圆环齿轮是手写网格。"
    ),
)


@contextlib.contextmanager
def _quiet():
    """把 bpy_model.py 的 print() 收进缓冲区。

    这里**只**用 redirect_stdout 就够了，分工是：
      - bpy 的 C 层噪音 → fd 1 → 已在 import 前永久指向 stderr，不归这里管
      - bpy_model 的 print() → sys.stdout → 也就是协议通道，必须在这里拦
    收上来的文本原样回给调用方，既不污染协议，又把日志还给人看。
    """
    buf = io.StringIO()
    with contextlib.redirect_stdout(buf):
        yield buf


def _stat_line(p: Path) -> str:
    if not p.exists():
        return f"（未生成）{p}"
    kb = p.stat().st_size / 1024
    return f"{p}  ({kb:.1f} KB)"


def _require_write(path: str) -> Path:
    p = Path(path).expanduser().resolve()
    if not str(p).startswith("/"):
        raise ValueError(f"必须给绝对路径，收到: {path}")
    p.parent.mkdir(parents=True, exist_ok=True)
    return p


@server.tool(
    name="bpy_doctor",
    title="检查 bpy 环境",
    description="确认 bpy 可用、版本、可用渲染引擎与解释器路径。不产生任何文件。",
)
async def bpy_doctor() -> str:
    valid = sorted(
        i.identifier
        for i in bpy.types.RenderSettings.bl_rna.properties["engine"].enum_items
    )
    log = io.StringIO()
    with _quiet() as buf:
        M.reset_scene()
    log.write(buf.getvalue())
    return "\n".join(
        [
            f"bpy        : {bpy.app.version_string}",
            f"python     : {platform.python_version()}  ({sys.executable})",
            f"渲染引擎   : {', '.join(valid)}",
            f"默认引擎   : {M.ENGINE}",
            f"可建模类型 : {', '.join(sorted(M.PRIMS))}",
            "reset_scene: 正常（上面若无报错即通过）",
            f"日志       : {log.getvalue().strip() or '（空）'}",
        ]
    )


@server.tool(
    name="bpy_scene",
    title="按场景 JSON 建模并存 .blend",
    description=(
        "按一份场景 JSON 建模并存成 .blend，可选再渲一张 PNG。\n"
        "JSON 结构：{\"objects\":[{\"type\":\"cube|plane|sphere|cylinder|cone|torus|gear|ico\","
        "\"name\":..,\"scale\":[..],\"location\":[..],\"rotation\":[..],\"material\":..,"
        "\"color\":[r,g,b,a],\"metallic\":..,\"roughness\":..,\"outline\":..,\"depth\":..}],"
        "\"studio\":{\"world\":..,\"lights\":[{\"name\":..,\"loc\":[..],\"energy\":..,\"type\":..,\"size\":..}],"
        "\"camera\":{\"name\":..,\"loc\":[..],\"look_at\":[..]}},"
        "\"render\":{\"engine\":..,\"width\":..,\"height\":..,\"transparent\":..,\"fps\":..,"
        "\"frame_start\":..,\"frame_end\":..},"
        "\"spin\":{\"axis\":\"X|Y|Z\",\"degrees\":360}}"
    ),
)
async def bpy_scene(
    scene: dict[str, Any],
    blend: str,
    render: str | None = None,
    timeout_ms: int = DEFAULT_TIMEOUT_MS,
) -> str:
    """scene: 场景 JSON（见描述）。blend: 输出 .blend 绝对路径。render: 可选输出 PNG 绝对路径。"""
    blend_p = _require_write(blend)
    out = []
    with _quiet() as buf:
        M.build_scene(scene)
        M.save_blend(str(blend_p))
        if render:
            M.render_still(_require_write(render))
    out.append(buf.getvalue().strip())
    out.append(f"场景已存: {_stat_line(blend_p)}")
    if render:
        out.append(f"渲染已存: {_stat_line(Path(render).expanduser().resolve())}")
    return "\n".join(x for x in out if x)


@server.tool(
    name="bpy_asset",
    title="造一个单体块并存 .blend / 渲图",
    description="快速造一个基本体（立方体/球/圆柱/圆环/齿轮等），可存 .blend、可渲 PNG。适合只要一个零件时用。",
)
async def bpy_asset(
    shape: str = "cube",
    name: str = "Primitive",
    blend: str | None = None,
    render: str | None = None,
    outline: list[list[float]] | None = None,
    depth: float = 1.0,
    count: int = 4,
    step: list[float] | None = None,
    base_type: str = "cube",
) -> str:
    """造一个物体。shape 见 bpy_doctor 的"可建模类型"，另支持两种复合体：

    - extrusion（2D 轮廓挤出，如墙体/型材）：必须给 outline = 至少 3 个 [x,y] 点，
      depth 为拉伸厚度，默认 1.0
    - array（阵列，如栏杆/栅格）：count 个数（默认 4）、step 为间距（默认 [1,0,0]），
      base_type 是被复制的单体块类型
    blend / render 为可选输出绝对路径。
    """
    allowed = set(M.PRIMS) | {"extrusion", "array"}
    if shape not in allowed:
        raise ValueError(f"不认识的形状 {shape!r}，可用: {', '.join(sorted(allowed))}")
    spec: dict[str, Any] = {
        "type": shape, "name": name, "material": "m", "color": [0.9, 0.5, 0.2, 1.0],
    }
    if shape == "extrusion":
        if not outline or len(outline) < 3:
            raise ValueError("extrusion 需要 outline：至少 3 个 [x,y] 点")
        spec["outline"] = outline
        spec["depth"] = depth
    elif shape == "array":
        if base_type not in M.PRIMS:
            raise ValueError(f"array 的 base_type {base_type!r} 不认识，"
                             f"可用: {', '.join(sorted(M.PRIMS))}")
        spec["base"] = {"type": base_type, "name": f"{name}-base"}
        spec["count"] = int(count)
        spec["step"] = step or [1, 0, 0]
    with _quiet() as buf:
        M.reset_scene()
        M.add_object(spec)          # 完整分发器：primitive / extrusion / array
        M.default_studio({})
        sc = bpy.context.scene
        sc.render.engine = M.ENGINE
        sc.render.resolution_x, sc.render.resolution_y = 800, 600
        if blend:
            M.save_blend(str(_require_write(blend)))
        if render:
            M.render_still(_require_write(render))
    out = [buf.getvalue().strip(), f"{shape} 创建成功"]
    if blend:
        out.append(f"已存: {_stat_line(Path(blend).expanduser().resolve())}")
    if render:
        out.append(f"已渲: {_stat_line(Path(render).expanduser().resolve())}")
    return "\n".join(x for x in out if x)


@server.tool(
    name="bpy_render",
    title="打开 .blend 渲图或渲帧序列",
    description="打开一个已有 .blend 并渲染。animation=true 渲整个帧序列（输出前缀），否则渲单张。",
)
async def bpy_render(
    blend: str,
    out: str,
    animation: bool = False,
    frame: int | None = None,
) -> str:
    """blend: 输入 .blend 绝对路径。out: 输出 PNG 绝对路径（序列时为前缀）。frame: 单张时的帧号。"""
    blend_p = Path(blend).expanduser().resolve()
    if not blend_p.exists():
        raise ValueError(f"blend 不存在: {blend_p}")
    with _quiet() as buf:
        bpy.ops.wm.open_mainfile(filepath=str(blend_p))
        if animation:
            M.render_range(_require_write(out))
        else:
            M.render_still(_require_write(out), frame)
    out_p = Path(out).expanduser().resolve()
    got = _stat_line(out_p)
    if not out_p.exists() and animation:
        # 序列渲染时 bpy 会往文件名里插帧号，原前缀本身不存在是正常的
        sibs = sorted(out_p.parent.glob(out_p.stem + "*.png"))
        got = (f"序列 {len(sibs)} 帧，前 3 个: "
               + ", ".join(f"{s.name}({s.stat().st_size/1024:.0f}KB)" for s in sibs[:3]))
    return "\n".join(x for x in [buf.getvalue().strip(), f"输入: {blend_p}", f"渲染: {got}"] if x)


if __name__ == "__main__":
    # 协议通道挂到 sys.stdout 上（fd 1 保持在 stderr 那边，永远不还原）。
    # mcp 的 stdio_server() 发现 sys.stdout 已不基于 fd 1，就改从 .buffer 供线，
    # 于是 bpy 怎么往 fd 1 喷都碰不到协议。
    sys.stdout = os.fdopen(_PROTO_FD, "w", buffering=1, encoding="utf-8", closefd=True)
    print("bpy-mcp-server 就绪：bpy %s / mcp %s / python %s"
          % (bpy.app.version_string, mcp_ver, platform.python_version()),
          file=sys.stderr)
    sys.stderr.flush()
    server.run(transport="stdio")

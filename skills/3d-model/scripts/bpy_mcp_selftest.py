#!/usr/bin/env python3
"""bpy MCP server 的真实验收：起真进程、走 stdio、真调用建模工具。

不是 mock：这里 spawn 的是 bpy_mcp_server.py 本体，用官方 MCP 客户端
ClientSession 连上去，所以能同时验到「协议通不通」「工具注册对不对」
「bpy_model 的建模逻辑有没有被我的封装写坏」。

跑法（必须用 bpyenv 的 python3.13，mcp 装在那边）：
    /home/user/miniforge3/envs/bpyenv/bin/python3.13 bpy_mcp_selftest.py
"""
from __future__ import annotations

import asyncio
import sys
import tempfile
from pathlib import Path

from mcp import ClientSession
from mcp.client.stdio import StdioServerParameters, stdio_client

HERE = Path(__file__).resolve().parent
SERVER = HERE / "bpy_mcp_server.py"
PY313 = "/home/user/miniforge3/envs/bpyenv/bin/python3.13"

passed, failed = 0, 0


def check(name: str, cond: bool, detail: str = "") -> None:
    global passed, failed
    if cond:
        passed += 1
        print(f"  ok   {name}" + (f" — {detail}" if detail else ""))
    else:
        failed += 1
        print(f"  FAIL {name}" + (f" — {detail}" if detail else ""))


async def main() -> int:
    outdir = Path(tempfile.mkdtemp(prefix="bpy-mcp-"))
    blend = outdir / "selftest.blend"
    png = outdir / "selftest.png"

    params = StdioServerParameters(command=PY313, args=[str(SERVER)], env=None)
    print(f"工作目录: {outdir}\n")

    async with stdio_client(params) as (read, write):
        async with ClientSession(read, write) as sess:
            init = await sess.initialize()

            # ---- 验收 1：客户端能列出工具清单 ----
            tools = (await sess.list_tools()).tools
            names = sorted(t.name for t in tools)
            print("== 验收 1：工具清单 ==")
            check("列出工具", len(tools) > 0, f"{len(tools)} 个")
            for want in ("bpy_doctor", "bpy_scene", "bpy_asset", "bpy_render"):
                check(f"含 {want}", want in names)
            check("每个工具都有描述", all(t.description for t in tools))
            # v2 的字段是 input_schema（snake_case），不是 v1 的 inputSchema
            check("bpy_scene 的入参 schema 非空",
                  bool(getattr(tools[names.index("bpy_scene")],
                               "input_schema", None)))

            # ---- doctor ----
            r = await sess.call_tool("bpy_doctor", {})
            txt = r.content[0].text
            print("\n== bpy_doctor ==")
            for line in txt.splitlines():
                print("   " + line)
            check("doctor 返回了 bpy 版本", "bpy" in txt and any(
                c.isdigit() for c in txt))
            check("doctor 列出渲染引擎", "BLENDER_EEVEE" in txt)
            check("doctor 未产生 .blend", not list(outdir.glob("*.blend")))

            # ---- 验收 2：真调一次建模，能出 .blend 与 .png ----
            r = await sess.call_tool("bpy_asset", {
                "shape": "cube", "name": "McpCube",
                "blend": str(blend), "render": str(png),
            })
            atxt = r.content[0].text
            print("\n== bpy_asset ==")
            for line in atxt.splitlines():
                print("   " + line)
            check("bpy_asset 未报 isError", not getattr(r, "isError", False))
            check("生成了 .blend", blend.exists(),
                  f"{blend.stat().st_size/1024:.1f} KB" if blend.exists() else "没生成")
            check("生成了 .png", png.exists(),
                  f"{png.stat().st_size/1024:.1f} KB" if png.exists() else "没生成")
            check("png 不是空壳", png.exists() and png.stat().st_size > 2000)

            # ---- 再验一次 bpy_scene（比 asset 复杂：多物体 + studio）----
            r2 = await sess.call_tool("bpy_scene", {
                "scene": {
                    "objects": [
                        {"type": "cube", "name": "A", "scale": [1, 1, 1],
                         "location": [-1, 0, 0], "material": "m",
                         "color": [0.8, 0.2, 0.2, 1.0]},
                        {"type": "sphere", "name": "B", "scale": [1, 1, 1],
                         "location": [1, 0, 0], "material": "m",
                         "color": [0.2, 0.4, 0.9, 1.0]},
                    ],
                },
                "blend": str(outdir / "scene.blend"),
                "render": str(outdir / "scene.png"),
            })
            stxt = r2.content[0].text
            print("\n== bpy_scene ==")
            for line in stxt.splitlines():
                print("   " + line)
            check("bpy_scene 未报 isError", not getattr(r2, "isError", False))
            check("scene 生成了 .blend", (outdir / "scene.blend").exists())

            # ---- bpy_render：打开已有 .blend 再渲（走 bpy.ops.open_mainfile，库模式高风险）----
            r4 = await sess.call_tool("bpy_render", {
                "blend": str(blend), "out": str(outdir / "rerendered.png"),
            })
            gtxt = r4.content[0].text
            print("\n== bpy_render ==")
            for line in gtxt.splitlines():
                print("   " + line)
            check("bpy_render 未报 isError", not getattr(r4, "isError", False))
            check("bpy_render 生成了 PNG", (outdir / "rerendered.png").exists(),
                  f"{(outdir / 'rerendered.png').stat().st_size/1024:.1f} KB"
                  if (outdir / "rerendered.png").exists() else "没生成")
            bad = await sess.call_tool("bpy_render", {
                "blend": str(outdir / "nope.blend"), "out": str(outdir / "x.png"),
            })
            check("bpy_render 对不存在的 blend 报错而非崩",
                  getattr(bad, "isError", True) or "不存在" in bad.content[0].text)

            # ---- 复合体：extrusion 与 array（CLI 支持、MCP 工具也必须等价支持）----
            ex = await sess.call_tool("bpy_asset", {
                "shape": "extrusion", "name": "Wall",
                "outline": [[0, 0], [2, 0], [2, 1], [0, 1]], "depth": 0.5,
                "render": str(outdir / "wall.png"),
            })
            check("extrusion 可用", not getattr(ex, "isError", False)
                  and (outdir / "wall.png").exists())
            ar = await sess.call_tool("bpy_asset", {
                "shape": "array", "name": "Rail", "base_type": "cube",
                "count": 5, "step": [1, 0, 0], "render": str(outdir / "array.png"),
            })
            check("array 可用", not getattr(ar, "isError", False)
                  and (outdir / "array.png").exists())
            bad_ex = await sess.call_tool("bpy_asset", {"shape": "extrusion", "name": "X"})
            check("extrusion 缺 outline 会明确报错",
                  getattr(bad_ex, "isError", True)
                  or "outline" in bad_ex.content[0].text)
            bad_sh = await sess.call_tool("bpy_asset", {"shape": "banana"})
            check("不认识的形状会明确报错", getattr(bad_sh, "isError", True)
                  or "banana" in bad_sh.content[0].text)

            # ---- 连续两次调用之间不能串味（reset_scene 的隔离）----
            r3 = await sess.call_tool("bpy_asset", {"shape": "cube", "name": "Second"})
            check("第二次调用仍正常", not getattr(r3, "isError", False),
                  r3.content[0].text.splitlines()[-1][:60] if r3.content else "")

    print(f"\n{'='*46}\n结果: 通过 {passed} / 失败 {failed}")
    return 1 if failed else 0


if __name__ == "__main__":
    sys.exit(asyncio.run(main()))

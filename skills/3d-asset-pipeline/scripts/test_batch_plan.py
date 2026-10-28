#!/usr/bin/env python3
"""threeview --list 批量模式的**离线**自测（不调用出图接口）。

覆盖 resolve_plan / views_for_kind / 名字护栏这几处纯逻辑。
为什么单独写：批量的真实代价在出图接口上，而这里要测的全是
「清单解析对不对」——跑一次真实批量要几分钟，这个测试是秒级的。

用法:
    python3 test_batch_plan.py
"""

import json
import sys
import tempfile
from pathlib import Path

sys.path.insert(0, str(Path(__file__).parent))
import threeview as tv

FAILS = []
PASSES = []


def check(name, cond, detail=""):
    (PASSES if cond else FAILS).append(name)
    print(("  OK  " if cond else "  FAIL") + f"  {name}" + (f"  -> {detail}" if detail else ""))


def main() -> int:
    print("== threeview batch planner ==")

    # 1. 按 kind 切换视图：角色 front/side/back，物件 front/side/top
    check("character -> front/side/back",
          tv.views_for_kind("character") == ("front", "side", "back"),
          str(tv.views_for_kind("character")))
    check("tree -> front/side/top",
          tv.views_for_kind("tree") == ("front", "side", "top"),
          str(tv.views_for_kind("tree")))
    # 默认 kind 显式是 character（单元素模式的旧行为）；批量清单不给 kind 时是 prop。
    check("不传 kind 时默认走角色视图（保持旧的 --desc 行为）",
          tv.views_for_kind() == ("front", "side", "back"), str(tv.views_for_kind()))
    check("显式 prop 走物件视图", tv.views_for_kind("prop") == tv.PROP_VIEWS)

    # 2. 措辞分流：物件那段必须真的被换掉，且不含人形词
    p_char = tv.build_prompt("a knight", "cel shaded", "character")
    p_tree = tv.build_prompt("a pine tree", "stylized", "tree")
    check("角色用 three-view character sheet 措辞", "three-view character model sheet" in p_char)
    check("物件用 orthographic 措辞", "orthographic" in p_tree)
    check("物件措辞不含 full-body figures（否则出树会跑偏出人形）",
          "full-body figures" not in p_tree)
    check("物件措辞要求顶视图", "TOP view" in p_tree)
    # 负面对照：这两句会把三个渲染用一条线连起来，导致整张图只剩 1 条墨带
    # （实测 rock/lamp 就是这么失败的：实测 1 个主体 band=(17,1208)）
    # 注意是「肯定式」的 ground line 不能出现；"no ground line" 这种否定式是要保留的。
    check("物件措辞没有肯定式 ground line（会把三视图连成一条带）",
          "sits on the ground line" not in p_tree and "the same ground line" not in p_tree,
          p_tree[p_tree.find("ground line") - 30: p_tree.find("ground line") + 20]
          if "ground line" in p_tree else "")
    check("物件措辞限制每个渲染只占自己那一三分之一（实测粘连成一整条带）",
          "own third of the image" in p_tree and "does not touch or overlap" in p_tree)
    check("物件措辞显式要求三者之间留白不相连",
          "wide empty white gaps" in p_tree and "no ground line" in p_tree
          and "no shadow joining them" in p_tree)

    # 2a. 提示词长度预算：接口硬拒 >=1500 字符（实测 status_code=2013）。
    # 措辞改长很容易悄悄顶爆上限，而顶爆后重试会被整段拒掉，等于重试失效。
    check("有 1500 上限的常量和专用异常类型",
          tv.MAX_PROMPT_CHARS < 1500 and issubclass(tv.PromptTooLong, ValueError))
    try:
        tv.prompt_for_attempt("x" * 1490, 2)
        check("超长提示词被拦（重试段顶爆）", False, "本该抛错")
    except tv.PromptTooLong:
        check("超长提示词被拦（重试段顶爆）", True)
    check("未超长时正常返回且含重试段",
          "RETRY" not in tv.prompt_for_attempt("P", 1) and tv.RETRY_SUFFIX in tv.prompt_for_attempt("P", 2))

    # 2b. 重试时提示词必须升级，而不是原样重试
    check("第 1 次不加强调段", tv.prompt_for_attempt("P", 1) == "P")
    check("第 2 次起追加强调段",
          tv.RETRY_SUFFIX in tv.prompt_for_attempt("P", 2) and "P" in tv.prompt_for_attempt("P", 2))
    check("强调段确实钉住数量与留白",
          "exactly THREE" in tv.RETRY_SUFFIX and "wide empty gaps" in tv.RETRY_SUFFIX
          and "edges of the image completely empty" in tv.RETRY_SUFFIX)

    # 2c. 自带示例清单必须全部留在预算内（否则示例本身就是坏的用法示范）
    example = Path(__file__).parent / "elements-example.json"
    if example.is_file():
        plan = tv.resolve_plan(tv.load_list(example), tv.DEFAULT_STYLE)
        over = [p["name"] for p in plan if p["retry_prompt_chars"] >= 1500]
        check("示例清单所有元素的重试提示词都在 1500 以内", not over, str(over))
        check("示例清单里每个 kind 都覆盖了 style（无 --style 回退）",
              all(p["style_source"].startswith("styles[") for p in plan),
              str([p["style_source"] for p in plan]))

    # 3. style 按 kind 覆盖，且能看出用的是哪一条
    data = {
        "styles": {"tree": "stylized low poly tree", "rock": "stylized low poly rock"},
        "defaults": {"attempts": 2},
        "elements": [
            {"name": "pine", "kind": "tree", "desc": "a pine"},
            {"name": "boulder", "kind": "rock", "desc": "a boulder"},
            {"name": "inline", "kind": "rock", "desc": "a thing", "style": "flat vector"},
            {"desc": "mystery"},
        ],
    }
    plan = tv.resolve_plan(data, tv.DEFAULT_STYLE)
    check("清单元素数一致", len(plan) == 4, str(len(plan)))
    check("按 kind 取 style（tree）",
          plan[0]["style"] == "stylized low poly tree" and plan[0]["style_source"] == "styles[tree]")
    check("按 kind 取 style（rock）",
          plan[1]["style_source"] == "styles[rock]")
    check("元素自带 style 优先级最高",
          plan[2]["style"] == "flat vector" and plan[2]["style_source"] == "element")
    check("无 kind 且清单无对应 style -> 回退 CLI 并显式告警来源",
          plan[3]["style"] == tv.DEFAULT_STYLE
          and "未按 kind 覆盖" in plan[3]["style_source"], plan[3]["style_source"])
    check("defaults 里的 attempts 被继承", plan[0]["attempts"] == 2, str(plan[0]["attempts"]))
    check("无 name 时给出可寻址的默认名 itemNN", plan[3]["name"] == "item03", plan[3]["name"])
    check("清单不给 kind 时按 prop 处理（不是角色）",
          plan[3]["kind"] == "prop" and plan[3]["views"] == ["front", "side", "top"],
          str(plan[3]["kind"]))

    # 4a. 逐元素护栏：路径穿越与缺 desc 必须报错，而不是静默写到输出目录外
    for bad, label in (
        ({"elements": [{"name": "../evil", "desc": "x"}]}, "元素名含 ../ 被拒"),
        ({"elements": [{"name": "a/b", "desc": "x"}]}, "元素名含 / 被拒"),
        ({"elements": [{"name": "a\\b", "desc": "x"}]}, "元素名含 \\ 被拒"),
        ({"elements": [{"name": ".hidden", "desc": "x"}]}, "元素名以点开头被拒"),
        ({"elements": [{"name": "ok"}]}, "缺 desc 被拒"),
        ({"elements": [{"name": "ok", "desc": "  "}]}, "desc 为空白被拒"),
    ):
        try:
            tv.resolve_plan(bad, tv.DEFAULT_STYLE)
            check(label, False, "本该抛错却通过了")
        except ValueError:
            check(label, True)

    # 4b. 文件级护栏：缺 elements / 空 elements 在读清单时就拒
    for bad, label in (({"foo": 1}, "缺 elements 被拒"), ({"elements": []}, "空 elements 被拒")):
        with tempfile.NamedTemporaryFile("w", suffix=".json", delete=False, encoding="utf-8") as fh:
            json.dump(bad, fh)
            tmp = fh.name
        try:
            tv.load_list(tmp)
            check(label, False, "本该抛错却通过了")
        except ValueError:
            check(label, True)
        finally:
            Path(tmp).unlink()

    # 5. 清单文件读得动
    with tempfile.NamedTemporaryFile("w", suffix=".json", delete=False, encoding="utf-8") as fh:
        json.dump(data, fh, ensure_ascii=False)
        tmp = fh.name
    loaded = tv.load_list(tmp)
    check("load_list 读回元素数一致", len(loaded["elements"]) == 4)
    Path(tmp).unlink()

    # 5b. 顶视图高度告警（第三格不像顶视图时要喊出来，不能静默出错图）
    import tempfile as _tf
    from PIL import Image as _Image
    import numpy as _np

    def _sheet(path, third_height):
        a = _np.full((720, 1280), 255, dtype=_np.uint8)
        a[100:600, 60:380] = 30          # front：又高又宽
        a[100:600, 500:800] = 30         # side
        top = 600 - third_height
        a[top:600, 920:1180] = 30        # top：高度可调
        _Image.fromarray(a).save(path)

    with _tf.TemporaryDirectory() as d:
        p_flat = Path(d) / "flat_sheet.png"
        p_true = Path(d) / "true_sheet.png"
        _sheet(p_flat, 500)              # 第三格和前两格一样高 -> 该告警
        _sheet(p_true, 90)               # 真顶视：明显更矮 -> 不该告警
        # 必须显式传 PROP_VIEWS：默认 VIEWS 是 front/side/back，根本没有 top 这一格
        m_flat = tv.cut_sheet(p_flat, Path(d) / "flat", out_dir=Path(d), views=tv.PROP_VIEWS)
        m_true = tv.cut_sheet(p_true, Path(d) / "true", out_dir=Path(d), views=tv.PROP_VIEWS)
        check("记录每格墨迹高度", "panel_ink_heights" in m_flat, str(m_flat.get("panel_ink_heights")))
        check("第三格等高时告警（它不是顶视图）",
              "top_view_height_ratio" in m_flat and m_flat.get("warning"),
              str(m_flat.get("warning")))
        check("真顶视（明显更矮）不告警",
              m_true.get("top_view_height_ratio", 0) < 0.7 and not m_true.get("warning"),
              str(m_true.get("top_view_height_ratio")))

    # 6. 签名自检：run_one 是模块函数，**拿不到 CLI 的 args**。
    # 本轮真出过这个错：把 ink_threshold=args.ink_threshold 写进 run_one，
    # 干跑（--dry-run 根本不走 run_one）全绿，真跑立刻 NameError。
    import inspect
    check("run_one 显式收 ink_threshold 参数（模块函数拿不到 args）",
          "ink_threshold" in inspect.signature(tv.run_one).parameters,
          str(list(inspect.signature(tv.run_one).parameters)))
    src = inspect.getsource(tv.run_one)
    check("成功与失败都记录 attempts_used（曾经成功时恒为 0）",
          "attempts_used=attempt" in src and 'attempts_used"] = ' in src, "")
    check("run_one 源码里不出现裸的 args.",
          "args." not in inspect.getsource(tv.run_one))

    print(f"\n{len(PASSES)} passed, {len(FAILS)} failed")
    if FAILS:
        for f in FAILS:
            print("  FAILED:", f)
        return 1
    print("ALL_PASS")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())

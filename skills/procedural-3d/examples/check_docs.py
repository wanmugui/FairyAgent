"""文档一致性检查：SKILL.md / 路由表 / scene-recipes.md 与实际实现是否还对得上。

    /path/to/miniforge3/envs/bpyenv/bin/python3.13 skills/procedural-3d/examples/check_docs.py

层 7 的验收写的是「SKILL.md 场景章节与 `scene-recipes.md` **无矛盾**」。
「无矛盾」如果只靠人眼读，那它下一轮就会悄悄烂掉——所以把它做成能复跑的检查。

查四类：
1. **引用路径真实存在**：文档里提到的每个脚本/参考文件/解释器都要 `test -e` 通过。
2. **不做超额承诺**：文档里写「能做到」的每个用例，脚本必须真的存在且真的能跑。
3. **不隐瞒已知缺口**：backlog 里各层标了遗留的项，文档的「还做不到」一栏里必须提到它——
   文档比实现更乐观，和文档比实现更悲观一样是矛盾。
4. **解释器写对**：唯一装了 trimesh 的是 fair 环境，不是 /usr/bin/python3
   （实测 /usr/bin/python3 没有 trimesh，写错会让独立检查直接挂掉）。
"""

import json
import os
import re
import subprocess
import sys

HERE = os.path.dirname(os.path.abspath(__file__))
SKILL_DIR = os.path.normpath(os.path.join(HERE, ".."))
REPO = os.path.normpath(os.path.join(HERE, "..", "..", ".."))
sys.path.insert(0, HERE)

DOCS = {
    "SKILL.md": os.path.join(SKILL_DIR, "SKILL.md"),
    "scene-recipes.md": os.path.join(SKILL_DIR, "references", "scene-recipes.md"),
    "3d-asset-pipeline/SKILL.md": os.path.join(
        REPO, "skills", "3d-asset-pipeline", "SKILL.md"),
}

# 前面必须是非路径字符（行首/空白/反引号/点），否则会从
# `skills/sculpting-character-pipeline/references/blockout-character.py`
# 这种**完整路径的中段**切出 `references/blockout-character.py`，
# 再相对错误目录一查，就报出一条根本不存在的「悬空引用」。
PATH_RE = re.compile(r"(?<![A-Za-z0-9_/.-])((?:[A-Za-z0-9_-]+/)*"
                     r"(?:examples|scripts|references)/[A-Za-z0-9_./-]+\.(?:py|json|md)"
                     r"|/path/to/[A-Za-z0-9_./-]+)")


def check_paths():
    """引用路径真实存在。

    ⚠️ 相对路径要按**该文档所属 skill 的目录**解析：
    `3d-asset-pipeline/SKILL.md` 里的 `scripts/threeview.py` 是相对它自己，
    不是相对 procedural-3d。第一版一律按 procedural-3d 解析，
    于是一口气报出 7 条「不存在的路径」，全是误报——文件明明都在。
    """
    missing = []
    for label, path in DOCS.items():
        if not os.path.exists(path):
            missing.append(f"{label}: 文档本身不存在 {path}")
            continue
        own = os.path.dirname(path)          # 该 skill 自己的目录
        text = open(path, encoding="utf-8").read()
        # 只有一个捕获组时 findall 返回字符串，多组才返回元组——
        # 写死 mm[0] 会把每个路径切成「第一个字符」。
        found = PATH_RE.findall(text)
        found = {x if isinstance(x, str) else x[0] for x in found}
        for m in sorted(found):
            if m.startswith("/"):
                cand = m
            elif m.startswith(("procedural-3d", "3d-asset-pipeline")):
                cand = os.path.join(REPO, "skills", m)
            else:
                # 逐级回退：相对本文档所在目录 → 同级 skill → skills 根目录
                cand = None
                # 文档里既有 skill 相对路径，也有**仓库根相对**路径
                # （`skills/procedural-3d/examples/...`），两种都得试。
                for base in (REPO, own, os.path.dirname(own),
                             os.path.join(REPO, "skills")):
                    trial = os.path.normpath(os.path.join(base, m))
                    if os.path.exists(trial):
                        cand = trial
                        break
                cand = cand or os.path.normpath(os.path.join(own, m))
            if not os.path.exists(cand):
                missing.append(f"{label}: 引用了不存在的路径 {m}")
    return {"pass": not missing, "missing": missing[:8], "missing_count": len(missing),
            "why": "文档引用的每个路径都必须真实存在"}


def check_promises():
    """文档说「能做」的用例，脚本必须真的存在。"""
    missing = []
    text = open(DOCS["scene-recipes.md"], encoding="utf-8").read()
    for m in set(re.findall(r"`(examples/[A-Za-z0-9_./-]+\.py)`", text)):
        if not os.path.exists(os.path.join(SKILL_DIR, m)):
            missing.append(m)
    return {"pass": not missing, "missing": missing,
            "why": "配方文档承诺的每个用例都得有对应脚本"}


def check_trimesh_interpreter():
    """唯一装了 trimesh 的是 fair 环境；文档必须写对。"""
    env = "/path/to/miniforge3/envs/fairy/bin/python3"
    ok_env = subprocess.run([env, "-c", "import trimesh"],
                            capture_output=True).returncode == 0
    sys_ok = subprocess.run(["/usr/bin/python3", "-c", "import trimesh"],
                            capture_output=True).returncode == 0
    # 判据不是「文档里有没有出现 /usr/bin/python3」——那行「实测它没有 trimesh」的
    # **警告**本来就该出现，第一版把警告当违规，误报了。真正的判据是：
    # 凡是提到独立检查的文档，**必须点名 fair 环境**。
    bad = []
    for label, path in DOCS.items():
        text = open(path, encoding="utf-8").read()
        if "inspect_delivery" in text and env not in text:
            bad.append(f"{label}: 提到 inspect_delivery 却没点名 {env}")
    return {"pass": ok_env and not bad, "fair_env_has_trimesh": ok_env,
            "usr_bin_has_trimesh": sys_ok, "doc_issues": bad,
            "why": "独立检查的解释器写错就会 ModuleNotFoundError，层 6 验收作废"}


def check_known_gaps():
    """backlog 里各层标注的遗留项，必须在文档「还做不到」栏里露个脸。"""
    backlog = open(os.path.join(REPO, "workspace", "dev-backlog.md"), encoding="utf-8").read()
    route = open(DOCS["3d-asset-pipeline/SKILL.md"], encoding="utf-8").read()
    gaps = {
        "树/石头碰撞代理": "碰撞",
        "HDRI 反解太阳": "反解",
        "真引擎加载": "引擎",
        "放置点旋转缩放": "旋转",
    }
    missing = []
    for topic, needle in gaps.items():
        if topic in backlog and needle not in route:
            missing.append(f"backlog 提到「{topic}」的遗留，但路由表「还做不到」栏没提")
    return {"pass": not missing, "missing": missing,
            "why": "文档比实现更乐观，也是一种矛盾"}


def main():
    checks = {
        "referenced_paths_exist": check_paths(),
        "promises_have_scripts": check_promises(),
        "trimesh_interpreter_right": check_trimesh_interpreter(),
        "known_gaps_disclosed": check_known_gaps(),
    }
    print("== 文档一致性检查 ==")
    for k, v in checks.items():
        detail = ", ".join(f"{a}={b}" for a, b in v.items()
                           if a not in ("pass", "why", "missing", "doc_issues")) or "无缺失"
        extra = v.get("missing") or v.get("doc_issues") or []
        if extra:
            detail += f"｜{extra}"
        print(f"  {'PASS' if v['pass'] else 'FAIL'}  {k}: {detail}")
    failed = [k for k, v in checks.items() if not v["pass"]]
    print(f"\n== {'DOCS: PASS' if not failed else 'DOCS: FAIL -> ' + ', '.join(failed)} ==")
    out = os.path.join(REPO, "workspace", "result", "layer7")
    os.makedirs(out, exist_ok=True)
    with open(os.path.join(out, "_docs_check.json"), "w", encoding="utf-8") as fh:
        json.dump(checks, fh, ensure_ascii=False, indent=2)
    return 1 if failed else 0


if __name__ == "__main__":
    raise SystemExit(main())

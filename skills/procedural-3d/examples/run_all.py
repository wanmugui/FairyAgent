"""一键跑完本 skill 的全部场景用例（3d-scene-quality 层 7）。

    /path/to/miniforge3/envs/bpyenv/bin/python3.13 skills/procedural-3d/examples/run_all.py

层 7 的验收是「`examples/` 里的场景用例**全部可一键跑通**」。
所以固化物不只是文档，还得有一条**真的能跑通**的命令——文档里写「可一键跑通」，
而跑不通，那句话就是假的。

⚠️ **两个解释器，这一步最容易踩**：
   - 大部分用例要 **bpy**（`/path/to/miniforge3/envs/bpyenv/bin/python3.13`）；
   - `inspect_delivery.py` **故意不用 bpy**，它是层 6 的**独立检查**，
     用系统 python 的 trimesh 读回 glb。导出方自检等于自己判自己卷面及格，
     所以这一条必须换解释器跑，否则层 6 的验收就作废了。
"""

import os
import subprocess
import sys
import time

HERE = os.path.dirname(os.path.abspath(__file__))
REPO = os.path.normpath(os.path.join(HERE, "..", "..", ".."))

BPY_PY = "/path/to/miniforge3/envs/bpyenv/bin/python3.13"
# 独立检查必须用**系统 python**（装了 trimesh 的那个）。
# 别用 sys.executable——这个脚本自己就跑在 bpy 环境里，
# 那样「独立检查」就成了同一个解释器自查，层 6 的验收直接作废。
# 注意：装了 trimesh 的**不是** /usr/bin/python3，而是 fairy 这个 conda 环境
# （实测 /usr/bin/python3 与 bpyenv 都没有 trimesh）。
SYS_PY = "/path/to/miniforge3/envs/fairy/bin/python3"

# Godot：层 6 交付物的**真实引擎加载验证**（2026-10-18 补）。
# 为什么必须单独一条、而不是并进 inspect_delivery.py：
# inspect_delivery 用 trimesh 读裸 glb，走的是「文件格式」这条路径；
# Godot 走的是**引擎自己的导入管线**（ResourceLoader → PackedScene → 实例化），
# 两者能抓到的错根本不重叠。最典型的一个：trimesh 全绿的 glb，
# 引擎里 Y-up 可能是错的；反过来引擎才会暴露轴向、原点、材质槽问题。
GODOT = os.path.expanduser("~/.local/bin/godot")
GODOT_PROJ = os.path.join(REPO, "workspace", "3d-pipeline-dev", "godot_verify")

# (脚本, 解释器, 说明)
CASES = [
    ("../scripts/gnkit_smoke.py", BPY_PY, "GN 工具库冒烟（SKILL.md 里写「预期 9 passed」）"),
    ("hd2d_diorama.py", BPY_PY, "层 -1/2：24 元素 HD-2D 场景拼装 + 贴地/碰撞自检"),
    ("build_props_batch.py", BPY_PY, "层 2：清单 → N 个 glb + 每件预览渲图"),
    ("scene_atmosphere.py", BPY_PY, "大气与光照专项：物理天空 + 体积雾对照"),
    ("three_structures.py", BPY_PY, "三个结构体演示"),
    ("layer3_light_rig.py", BPY_PY, "层 3：三灯打光台 + 体积雾 + AgX，五项自查"),
    ("layer3b_levers.py", BPY_PY, "层 3b：HDRI / Mist Pass / Glare / 移轴 四项对照"),
    ("layer4_terrain.py", BPY_PY, "层 4：脊状分形地形 + 侵蚀 + ColorRamp 分层材质"),
    ("layer5_scatter.py", BPY_PY, "层 5：GN 散布，万级实例 + 坡度/高度遮罩"),
    ("layer6_delivery.py", BPY_PY, "层 6：LOD 链 + 碰撞代理 + glTF 导出"),
    ("inspect_delivery.py", SYS_PY, "层 6 独立检查（fair 环境的 trimesh，不经 bpy）"),
    ("check_docs.py", BPY_PY, "层 7：文档一致性（引用路径 / 承诺 / 解释器 / 已知缺口）"),
]

# 独立于 CASES：它不是 python 脚本，是一次引擎命令行调用。
# 单独放是因为「可一键跑通」意味着有人在没装 Godot 的机器上也能跑完其余 12 条，
# 不能让这一条把整条链卡死。缺引擎时标记 SKIP 而不是 FAIL。
ENGINE_CASES = [
    ("godot_load_verify", "层 6 引擎验证：4 个 glb 过 Godot 导入管线 + 轴向/原点/LOD/碰撞/散点 11 项检查",
     "verify_load.gd"),
    ("godot_diorama_verify", "层 -1 引擎验证：箱庭整体 + 5 分类 glb 加载，Y-up 与景深跨度核对",
     "verify_diorama.gd"),
]


def run(script, interp, note):
    path = os.path.normpath(os.path.join(HERE, script))
    if not os.path.exists(path):
        return script, "MISSING", f"找不到 {script}", 0.0
    t0 = time.time()
    env = dict(os.environ)
    proc = subprocess.run([interp, path], cwd=REPO, env=env,
                          capture_output=True, text=True, timeout=1800)
    dt = time.time() - t0
    tail = [ln.strip() for ln in (proc.stdout or "").splitlines()
            if "ACCEPT" in ln or "INSPECT" in ln or "passed" in ln]
    return script, ("PASS" if proc.returncode == 0 else f"FAIL({proc.returncode})"), \
        (tail[-1] if tail else (note if proc.returncode == 0 else "见 stderr")), dt


def run_godot(name, note, script="verify_load.gd"):
    """在真实 Godot 引擎里加载层 6 交付物并逐项核对。

    用 --import 先过一遍导入器（glb 的导入产物是引擎自己生成的，
    首次运行没有 .godot/imported 会直接 load 失败），再跑验证脚本。
    """
    t0 = time.time()
    if not os.path.exists(GODOT):
        return "SKIP", f"未安装 Godot（{GODOT}），{note}", 0.0
    if not os.path.exists(os.path.join(GODOT_PROJ, "project.godot")):
        return "MISSING", f"找不到验证工程 {GODOT_PROJ}", 0.0
    try:
        subprocess.run([GODOT, "--headless", "--path", GODOT_PROJ, "--import"],
                       capture_output=True, text=True, timeout=600)
        proc = subprocess.run([GODOT, "--headless", "--path", GODOT_PROJ,
                               "--script", script],
                              cwd=GODOT_PROJ, capture_output=True, text=True,
                              timeout=900)
    except subprocess.TimeoutExpired:
        return "TIMEOUT", "引擎验证超时", time.time() - t0
    dt = time.time() - t0
    tail = [ln.strip() for ln in (proc.stdout or "").splitlines()
            if "ACCEPT" in ln]
    ok = proc.returncode == 0
    return ("PASS" if ok else f"FAIL({proc.returncode})"), \
        (tail[-1] if tail else (note if ok else "见引擎 stderr")), dt


def main():
    only = sys.argv[1:] or None
    print(f"仓库根目录：{REPO}")
    print(f"bpy 解释器：{BPY_PY}")
    print(f"独立检查解释器：{SYS_PY}")
    print(f"引擎验证：{GODOT}\n")
    rows = []
    for script, interp, note in CASES:
        if only and script not in only and script[:-3] not in only:
            continue
        # run() 返回 (script, status, detail, dt)。第一版按三个变量解包，
        # status 拿到的是脚本名，于是 11 条用例全被判 FAIL——
        # 而每条详情里明明写着 ACCEPT: PASS。判据错位比判据不严更坏。
        _, status, detail, dt = run(script, interp, note)
        rows.append((script, status, detail, dt))
        print(f"  {status:<8} {script:<24} {dt:6.1f}s  {detail}")

    for name, note, script in ENGINE_CASES:
        if only and name not in only:
            continue
        status, detail, dt = run_godot(name, note, script)
        rows.append((name, status, detail, dt))
        print(f"  {status:<8} {name:<24} {dt:6.1f}s  {detail}")

    bad = [r for r in rows if r[1] not in ("PASS", "SKIP")]
    print(f"\n== RUN-ALL: {'ALL PASS' if not bad else 'FAIL -> ' + ', '.join(r[0] for r in bad)} "
          f"({len(rows) - len(bad)}/{len(rows)}) ==")
    return 1 if bad else 0


if __name__ == "__main__":
    raise SystemExit(main())

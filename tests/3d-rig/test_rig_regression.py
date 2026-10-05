"""3D rig 回归测试。

与 workspace/ 下那些一次性分析脚本的区别：那些只打印数字，靠人肉比对；
这里把实测基线固化成断言，跑不过就红。

Blender 探针**整个测试会话只跑一次**（session 级 fixture）——启动 Blender
一次要几十秒，不能每个用例启一次，否则测试本身就成了速率瓶颈。

跑法：
    pytest tests/3d-rig/ -v
基线确需刷新时（且必须写明理由）：
    pytest tests/3d-rig/ --update-baseline
"""
import json
import os
import re
import shutil
import subprocess
import sys

import pytest

HERE = os.path.dirname(os.path.abspath(__file__))
REPO = os.path.abspath(os.path.join(HERE, "..", ".."))
BASELINE = os.path.join(HERE, "baseline.json")
PROBE = os.path.join(HERE, "blender_probe.py")
BLEND = os.path.join(REPO, "workspace", "result", "character_rigged.blend")


@pytest.fixture(scope="session")
def blender_path():
    for cand in (shutil.which("blender"),
                 os.environ.get("BLENDER", ""),
                 "/home/user/miniforge3/bin/blender"):
        if cand and os.path.exists(cand):
            return cand
    pytest.skip("找不到 blender，跳过 rig 回归")


@pytest.fixture(scope="session")
def probe(blender_path):
    bl = blender_path
    if not os.path.exists(BLEND):
        pytest.skip("缺少被测资产 %s" % BLEND)
    r = subprocess.run([bl, "-b", "--factory-startup", "-P", PROBE],
                       capture_output=True, text=True, timeout=1800, cwd=REPO)
    m = re.search(r"^PROBE_JSON=(.*)$", r.stdout, re.M)
    if not m:
        pytest.fail("探针没有输出 PROBE_JSON。stdout 尾部:\n%s\nstderr 尾部:\n%s"
                    % (r.stdout[-800:], r.stderr[-800:]))
    return json.loads(m.group(1))


@pytest.fixture(scope="session")
def base():
    with open(BASELINE, encoding="utf-8") as f:
        return json.load(f)


# ---------------------------------------------------------------- 绑定结构

def test_骨骼数量与配对(probe, base):
    assert probe["bone_count"] == base["structure"]["bone_count"], \
        "骨骼数变了：%s（基线 %s）" % (probe["bone_count"], base["structure"]["bone_count"])
    assert probe["bone_pairs"] == base["structure"]["bone_pairs"], \
        "左右配对数变了：%s" % probe["bone_pairs"]


def test_无游离顶点(probe, base):
    assert probe["free_vertices"] == base["structure"]["free_vertices"], \
        "有 %d 个顶点没绑到任何骨" % probe["free_vertices"]


def test_Armature修改器在首位(probe):
    assert probe["armature_modifier_first"] is True, \
        "Armature 必须排在其他修改器之前，否则形变链顺序不对"


def test_体积保持DQ已开启(probe):
    """回归护栏：DQ 是修前臂塌陷的关键，曾因重建 blend 被静默关掉过。"""
    assert probe["use_deform_preserve_volume"] is True, \
        "use_deform_preserve_volume 被关掉了——前臂塌陷会立刻回来（flat 0.87→0.76）"


def test_空骨集合未变化(probe, base):
    """躯干链 hips/neck/root/spine 没有顶点以它们为最大权重骨。
    这是已知的热权重特性，不是缺陷；但如果集合变了说明权重方案被动过。"""
    assert probe["empty_bones"] == base["structure"]["empty_bones"], \
        "空骨集合变了：%s（基线 %s）" % (probe["empty_bones"], base["structure"]["empty_bones"])


# ---------------------------------------------------------------- 探针自检

def test_选中集自检_前臂不能选成整条臂(probe, base):
    """防呆：选错顶点集会让所有 flatness 指标变成废数（历史上真发生过——
    startswith('hand.') 同时圈进左右手，跨度 1.6m 覆盖整个展臂宽度）。"""
    ext = probe["selfcheck_forearm_extent_m"]
    assert ext is not None
    assert max(ext) < base["selftest"]["forearm_extent_max_m"], \
        "forearm.L 选中集跨度 %s 过大，选中集选错了" % ext


# ---------------------------------------------------------------- 形变质量

@pytest.mark.parametrize("pose", ["limit", "curl", "lean"])
def test_边长中位数保持(probe, base, pose):
    p = probe["poses"][pose]
    assert p["p50"] >= base["deformation"]["p50_min"], \
        "%s 姿势 p50=%.4f 低于 %.2f" % (pose, p["p50"], base["deformation"]["p50_min"])


@pytest.mark.parametrize("pose", ["limit", "curl", "lean"])
def test_边长1分位不塌(probe, base, pose):
    p = probe["poses"][pose]
    floor = base["deformation"]["p01_min"][pose]
    assert p["p01"] >= floor, \
        "%s 姿势 p01=%.4f 跌破基线 %.2f" % (pose, p["p01"], floor)


# ---------------------------------------------------------------- 局部质量

def test_前臂不摊平(probe, base):
    v = probe["forearm_flat_ratio"]
    floor = base["local_quality"]["forearm_flat_ratio_min"]
    assert v >= floor, "forearm.L flat 比值 %.4f 跌破 %.2f" % (v, floor)


def test_手掌保持刚性(probe, base):
    """手掌是 blockout 里的扁盒子，不是塌陷。回归护栏：手部一旦真的被压扁，
    说明权重出了问题，而不是当初误判成那样。"""
    v = probe["hand_flat_ratio"]
    floor = base["local_quality"]["hand_flat_ratio_min"]
    assert v >= floor, "hand.L flat 比值 %.4f 跌破 %.2f" % (v, floor)


def test_肘部无系统性压短(probe, base):
    v = probe["elbow_edge_median_ratio"]
    floor = base["local_quality"]["elbow_edge_median_ratio_min"]
    assert v >= floor, "肘部边长中位数比 %.4f 跌破 %.2f" % (v, floor)


def test_肘部压短边占比未恶化(probe, base):
    v = probe["elbow_edge_pct_below_085"]
    ceil = base["local_quality"]["elbow_edge_pct_below_085_max"]
    assert v <= ceil, "肘部压短边占比 %.1f%% 超过 %.0f%%" % (v * 100, ceil * 100)

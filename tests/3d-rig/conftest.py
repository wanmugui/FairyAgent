"""刷新 3D rig 回归基线用的选项。

刻意做成显式开关：基线静默更新等于把回归测试变成摆设——测不过就调基线，
等于没测。确需刷新时必须显式加 --update-baseline，并在提交信息里写明理由。
"""
import json
import os
import sys

import pytest


def pytest_addoption(parser):
    parser.addoption(
        "--update-baseline",
        action="store_true",
        default=False,
        help="用本次探针实测值刷新 baseline.json（需在提交信息里写明理由）",
    )


@pytest.hookimpl(trylast=True)
def pytest_sessionfinish(session, exitstatus):
    if not session.config.getoption("--update-baseline"):
        return
    if exitstatus != 0:
        print("\n测试有失败，拒绝刷新基线——先修好再刷新。")
        return
    here = os.path.dirname(os.path.abspath(__file__))
    bp = os.path.join(here, "baseline.json")
    with open(bp, encoding="utf-8") as f:
        b = json.load(f)
    probe = session.config.stash.get("rig_probe") if hasattr(session.config, "stash") else None
    if not probe:
        print("\n未取到探针结果（可能 blender 不可用被 skip），基线未改动。")
        return
    b["observed"] = {
        "forearm_flat_ratio": probe["forearm_flat_ratio"],
        "hand_flat_ratio": probe["hand_flat_ratio"],
        "elbow_edge_median_ratio": probe["elbow_edge_median_ratio"],
        "elbow_edge_pct_below_085": probe["elbow_edge_pct_below_085"],
        "poses": probe["poses"],
    }
    with open(bp, "w", encoding="utf-8") as f:
        json.dump(b, f, ensure_ascii=False, indent=2)
    print("\n基线已刷新：%s" % bp)

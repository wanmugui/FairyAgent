"""定时任务接口与会话路由的回归测试（纯静态分析，不启服务，秒级）。

守护的是一个真实踩过的坑：外层 POST 白名单漏了 /update，而 update 处理器就挂
在那个 if 里面，于是它是死代码，编辑定时任务一律 404。人只能删掉重建，而重建
时设置页不构造 session/guard —— 定时任务随即掉回主会话、每 30 分钟空转唤醒一次。

这类"处理器存在但不可达"的缺陷，靠人工点一次界面未必能发现：界面不报错的话，
人会以为改成功了。所以固化成测试。
"""
import os
import re

import pytest

REPO = os.path.abspath(os.path.join(os.path.dirname(os.path.abspath(__file__)), "..", ".."))
SERVER = os.path.join(REPO, "frontend", "server.cjs")
SCHEDULER = os.path.join(REPO, "frontend", "scheduler.cjs")
UI = os.path.join(REPO, "frontend", "src", "components", "ScheduledTasksSection.jsx")


@pytest.fixture(scope="module")
def server_src():
    assert os.path.exists(SERVER), "找不到 %s" % SERVER
    with open(SERVER, encoding="utf-8") as f:
        return f.read()


def _post_whitelist(src):
    """取出调度相关 POST 路由的外层白名单条件文本。

    必须锚定到含 scheduled-tasks 的那个条件：文件里有多个
    `if (method === "POST" && (...))`，不加锚会抓到更靠前的别的路由块。
    """
    m = re.search(
        r'if \(method === "POST" && \(([^)]*scheduled-tasks[^)]*?)\)\)\s*\{', src, re.S)
    assert m, ("没找到含 scheduled-tasks 的 POST 白名单条件，路由结构可能变了。\n"
               "提示：若确实改成了路由表结构，请同步更新本用例的匹配方式。")
    return m.group(1)


@pytest.mark.parametrize("route", [
    "/api/scheduled-tasks",
    "/api/scheduled-tasks/toggle",
    "/api/scheduled-tasks/update",   # ← 曾漏，处理器变死代码
    "/api/scheduled-tasks/delete",
    "/api/scheduled-tasks/run",
])
def test_定时任务接口都在POST白名单里(server_src, route):
    wl = _post_whitelist(server_src)
    assert '"%s"' % route in wl, \
        "%s 不在 POST 白名单里——它的处理器在同一个 if 内部，将永远不可达（404）" % route


def test_update处理器确实存在(server_src):
    """与上一个用例配对：既要在白名单里，也要有实现，缺一不可。"""
    assert 'pathname === "/api/scheduled-tasks/update"' in server_src
    assert re.search(r'pathname === "/api/scheduled-tasks/update"\)\s*\{', server_src), \
        "找不到 /update 的处理块"


def test_分支会话占位符被支持():
    """scheduler 侧必须支持 {today}，否则分支子会话名每天都要手改、一重建就丢。"""
    assert os.path.exists(SCHEDULER)
    with open(SCHEDULER, encoding="utf-8") as f:
        src = f.read()
    assert re.search(r'\.replace\(/\\\{today\\\}', src) or '"{today}"' in src or "{today}" in src, \
        "scheduler 里找不到 {today} 占位符替换逻辑"
    assert "defaultSessionName" in src


def test_子会话会被创建为分支():
    """带 __ 的会话名必须先经 /api/sessions 建分支，否则 /api/chat 只收顶层会话。"""
    with open(SCHEDULER, encoding="utf-8") as f:
        src = f.read()
    assert "/api/sessions" in src
    assert "parent_session" in src
    assert re.search(r'includes\("__"\)', src), \
        "scheduler 里没有对 __ 分支会话的判断"

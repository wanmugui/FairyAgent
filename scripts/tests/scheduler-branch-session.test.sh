#!/usr/bin/env bash
# 回归：定时任务使用 {today}__<分支> 时，必须先通过 POST /api/sessions
# 以 parent_session 建好分支，否则 /api/chat 会返回
# 400 top_level_only_today（顶层会话只能是纯日期）。
set -uo pipefail
PASS=0; FAIL=0
ok(){ echo "  PASS  $1"; PASS=$((PASS+1)); }
ng(){ echo "  FAIL  $1"; FAIL=$((FAIL+1)); }
S="${1:-frontend/scheduler.cjs}"

echo "[1] 语法检查"
node --check "$S" 2>/dev/null && ok "语法正确" || ng "语法错误"

echo "[2] 分支会话必须先创建再对话"
if grep -q 'api/sessions' "$S"; then ok "存在建分支会话的调用"; else ng "缺少 POST /api/sessions 建分支"; fi

echo "[3] 必须带 parent_session"
if grep -q 'parent_session' "$S"; then ok "使用 parent_session"; else ng "未传 parent_session"; fi

echo "[4] {today} 模板仍保留"
grep -q '{today}' "$S" && ok "保留 {today} 占位符" || ng "{today} 丢失"

echo "[5] 仅对含 __ 的分支名建会话（顶层日期名不建）"
if grep -qE 'includes\(["'\''`]__["'\''`]|indexOf\(["'\''`]__|split\(["'\''`]__' "$S"; then ok "按 __ 判定分支"; else ng "未按 __ 判定分支"; fi

echo "[6] 不得调用不存在的 log()（本文件没有该函数，会导致每轮运行报错）"
if grep -qE '^[[:space:]]*log\(' "$S"; then
  ng "存在裸 log( 调用 -> log is not defined"
else
  ok "无裸 log( 调用"
fi

echo "[7] 建分支必须传完整会话名（传裸分支名会生成 -2 空壳）"
if grep -qE 'name: targetSession' "$S"; then
  ok "传 targetSession 完整名"
else
  ng "未传完整名"
fi

echo "[8] parent_session 取父级，不取分支名"
if grep -qE 'split\("__", 2\)' "$S"; then ok "按 __ 切分取父级"; else ng "未切分父级"; fi

echo
echo "结果: PASS=$PASS FAIL=$FAIL"
[ "$FAIL" -eq 0 ]

#!/usr/bin/env bash
# inject-attachments 验收。
#
# 注意：旧版这个脚本是「grep 到某字符串就算过」，特性坏掉时依然 5/5 全绿，
# 根本没法当门禁。真正的判据在 inject-attachments.test.cjs 里跑真函数，
# 本脚本只负责编排 + 编译门禁。
set -uo pipefail
cd "$(dirname "$0")/../.."
ROOT="$PWD"
fail=0
ok()   { echo "  ok   $1"; }
fail() { echo "  FAIL $1"; fail=1; }

echo "== 1. 功能验收（真函数 + 注入回显契约）=="
if node scripts/tests/inject-attachments.test.cjs; then ok "inject-attachments.test.cjs"; else fail "inject-attachments.test.cjs"; fi

echo "== 2. 编译门禁 =="
if node --check frontend/src/api/chat.js 2>/dev/null; then ok "chat.js 可解析"; else fail "chat.js 可解析"; fi
if (cd frontend && ./node_modules/.bin/vite build --logLevel error) >/tmp/inject-attachments-build.log 2>&1; then
  ok "vite build 通过"
else
  fail "vite build 通过"; tail -20 /tmp/inject-attachments-build.log
fi

if [ "$fail" = 0 ]; then echo "注入附件：全部通过"; else echo "注入附件：有失败项"; fi
exit "$fail"

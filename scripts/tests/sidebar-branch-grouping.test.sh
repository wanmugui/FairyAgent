#!/usr/bin/env bash
# 回归：定时任务分支跨零点后被兜底挂到昨天根下，导致
#   (a) 侧边栏把"今天的定时任务"归到昨天那一组；
#   (b) 下一轮"同名不同爹"判定失败，再生出 -2/-3/... 空壳。
#
# 起一个隔离实例（临时 FAIRY_MEMORY_ROOT + 空闲端口），打真实 HTTP，
# 复现跨零点漂移形状，断言 POST /api/sessions 复用并就地归位。
# 不依赖、也不影响正在服务的那个 server.cjs 进程。
set -uo pipefail
PASS=0; FAIL=0
ok(){ echo "  PASS  $1"; PASS=$((PASS+1)); }
ng(){ echo "  FAIL  $1"; FAIL=$((FAIL+1)); }

REPO="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
TMP="$(mktemp -d)"
export FAIRY_MEMORY_ROOT="$TMP/memory"
SESS="$FAIRY_MEMORY_ROOT/sessions"
PORT="${2:-18099}"
SRV_PID=""

cleanup(){ [ -n "$SRV_PID" ] && kill "$SRV_PID" 2>/dev/null; rm -rf "$TMP"; }
trap cleanup EXIT

mkroot(){ # $1=日期名
  mkdir -p "$SESS/$1"
  printf '{"messages":[],"kind":"main","parent_session":null,"daily_date":"%s"}\n' "$1" \
    > "$SESS/$1/$1.json"
}
mkbranch(){ # $1=会话名 $2=父
  mkdir -p "$SESS/$1"
  printf '{"messages":[{"role":"user","text":"hi"}],"kind":"branch","parent_session":"%s"}\n' "$2" \
    > "$SESS/$1/$1.json"
}
parent_of(){ python3 -c "import json,sys;print(json.load(open(sys.argv[1]))['parent_session'])" "$1"; }

echo "[1] 启动隔离 server.cjs（端口 $PORT，临时 memory root）"
node "$REPO/frontend/server.cjs" "$PORT" >"$TMP/server.log" 2>&1 &
SRV_PID=$!
for _ in $(seq 1 60); do
  curl -sf --max-time 1 "http://127.0.0.1:$PORT/api/sessions" -o /dev/null && break
  sleep 0.25
done
curl -sf --max-time 2 "http://127.0.0.1:$PORT/api/sessions" -o /dev/null \
  && ok "服务已就绪" || { ng "服务启动失败"; cat "$TMP/server.log"; exit 1; }

echo "[2] 复现跨零点漂移：2026-10-09 的分支被误挂在 2026-10-08 根下"
mkroot 2026-10-08
mkroot 2026-10-09
mkbranch "2026-10-09__st_deadbeef" "2026-10-08"
[ "$(parent_of "$SESS/2026-10-09__st_deadbeef/2026-10-09__st_deadbeef.json")" = "2026-10-08" ] \
  && ok "漂移形状已就位（父=2026-10-08）" || ng "漂移形状构造失败"

echo "[3] 调度器再跑一轮：同名 + 正确父（2026-10-09）"
RESP=$(curl -s --max-time 5 -X POST "http://127.0.0.1:$PORT/api/sessions" \
  -H 'Content-Type: application/json' \
  -d '{"name":"2026-10-09__st_deadbeef","parent_session":"2026-10-09","created_by":"model"}')
echo "$RESP" | grep -q '"name":"2026-10-09__st_deadbeef"' \
  && ok "复用原会话，没有生成 -2 空壳" || ng "又生成了新分支: $RESP"
[ -d "$SESS/2026-10-09__st_deadbeef-2" ] \
  && ng "磁盘上出现了 -2 空壳" || ok "磁盘上无 -2 空壳"

echo "[4] 父会话已就地归位到今天"
[ "$(parent_of "$SESS/2026-10-09__st_deadbeef/2026-10-09__st_deadbeef.json")" = "2026-10-09" ] \
  && ok "parent_session 已改写为 2026-10-09" || ng "父会话仍漂在 2026-10-08"

echo "[5] 回归：同父同名的正常幂等复用仍然生效"
curl -s --max-time 5 -X POST "http://127.0.0.1:$PORT/api/sessions" \
  -H 'Content-Type: application/json' \
  -d '{"name":"2026-10-09__st_deadbeef","parent_session":"2026-10-09","created_by":"model"}' \
  | grep -q '"name":"2026-10-09__st_deadbeef"' && ok "再次调用仍复用" || ng "再次调用没复用"

echo "[6] 回归：合法嵌套分支（子任务套子任务）不得被拍平"
mkbranch "2026-10-09__dev-backlog" "2026-10-09"
mkbranch "2026-10-09__dev-backlog__子任务" "2026-10-09__dev-backlog"
curl -s --max-time 5 -X POST "http://127.0.0.1:$PORT/api/sessions" \
  -H 'Content-Type: application/json' \
  -d '{"name":"2026-10-09__dev-backlog__子任务","parent_session":"2026-10-09__dev-backlog","created_by":"model"}' \
  >/dev/null
[ "$(parent_of "$SESS/2026-10-09__dev-backlog__子任务/2026-10-09__dev-backlog__子任务.json")" = "2026-10-09__dev-backlog" ] \
  && ok "嵌套父保持不变" || ng "嵌套分支被错误拍平"

echo "[7] 回归：UI 手动新建分支（不传名字）仍走去重出 -2 的老语义"
# 第一次创建：名字由 domain 派生，此时不该有 -2
R1=$(curl -s --max-time 5 -X POST "http://127.0.0.1:$PORT/api/sessions" \
  -H 'Content-Type: application/json' \
  -d '{"parent_session":"2026-10-09","domain":"手动分支"}')
echo "$R1" | grep -q '"name":"2026-10-09__手动分支"' \
  && ok "首次创建不套 -2" || ng "首次创建就多套了后缀: $R1"
# 第二次同名：UI 手动连点两次的去重语义必须保留
R2=$(curl -s --max-time 5 -X POST "http://127.0.0.1:$PORT/api/sessions" \
  -H 'Content-Type: application/json' \
  -d '{"parent_session":"2026-10-09","domain":"手动分支"}')
echo "$R2" | grep -q '"name":"2026-10-09__手动分支-2"' \
  && ok "不传名时重复创建仍出 -2（原语义未变）" || ng "手动新建分支去重语义被改坏: $R2"

echo
echo "结果: PASS=$PASS FAIL=$FAIL"
[ "$FAIL" -eq 0 ]
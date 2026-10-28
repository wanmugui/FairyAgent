#!/usr/bin/env bash
# 回归：scheduler 定时任务重复触发时，是否会不断新建 "<完整名>-2/-3/-4" 空壳分支会话。
#
# 为什么需要它：scheduler.cjs 每轮都以 targetSession 完整名（YYYY-MM-DD__domain）
# 调 POST /api/sessions 建分支，注释里写的是"已存在则复用"。但 server.cjs 的去重
# 循环是无条件的：只要目标名已存在就 +1/-2 递增，从不返回已有会话。
# 结果每 15 分钟的空跑轮次都在侧边栏留下一串 0 条消息的"（分支会话，空的）"。
#
# 之前的 scheduler-branch-session.test.sh 只做静态 grep 断言"传了完整名"，
# 从未真正调用过那个去重循环，所以 bug 一直在绿测试下活着。本测试起真实服务、
# 连发两次相同请求，直接检查磁盘上有没有多出 -2 目录。
#
# 预期修复前：FAIL（出现 -2）
# 预期修复后：PASS（复用原会话，目录数不变）
set -uo pipefail
PASS=0; FAIL=0
ok(){ echo "  PASS  $1"; PASS=$((PASS+1)); }
ng(){ echo "  FAIL  $1"; FAIL=$((FAIL+1)); }

REPO="$(cd "$(dirname "$0")/../.." && pwd)"
PORT=18841
SESSIONS_DIR="$REPO/memory/sessions"
# 固定测试名，带端口后缀避免和真实会话撞名
BASE="branchreuse-$$"
MARK="2026-10-02__${BASE}"

cleanup() {
  if [ -n "${SRV_PID:-}" ]; then
    # 子进程在 subshell 里起，kill 父 pid 可能留 node 孤儿占着端口，
    # 所以按端口再兜底杀一次。
    kill -9 "$SRV_PID" 2>/dev/null
    pkill -9 -f "server\.cjs $PORT" 2>/dev/null
    wait "$SRV_PID" 2>/dev/null
  fi
  rm -rf "$SESSIONS_DIR/$MARK" "$SESSIONS_DIR/${MARK}-2" \
         "$SESSIONS_DIR/${MARK}-3" "$SESSIONS_DIR/${MARK}-4" 2>/dev/null
  # 兜底：万一分支被派生到别的层级（如带日期子层的 SESSIONS），一并清掉
  find "$REPO/memory" -maxdepth 3 -type d -name "${MARK}*" -exec rm -rf {} + 2>/dev/null
  return 0
}
trap cleanup EXIT

echo "[1] 语法检查 server.cjs"
if node --check "$REPO/frontend/server.cjs" 2>/dev/null; then ok "server.cjs 语法正确"; else ng "server.cjs 语法错误"; fi

echo "[2] 启动隔离测试服务 (PORT=$PORT)"
if ss -tln 2>/dev/null | grep -q ":$PORT"; then
  ng "测试端口 $PORT 被占用，换一个"
  echo; echo "结果: PASS=$PASS FAIL=$FAIL"; exit 1
fi
( cd "$REPO/frontend" && node server.cjs "$PORT" >/tmp/branchreuse-srv.log 2>&1 ) &
SRV_PID=$!
for _ in $(seq 1 40); do
  curl -s -o /dev/null "http://127.0.0.1:$PORT/api/sessions" && break
  sleep 0.25
done
if curl -s -o /dev/null "http://127.0.0.1:$PORT/api/sessions"; then
  ok "服务已监听 $PORT"
else
  ng "服务启动失败"; tail -5 /tmp/branchreuse-srv.log
  echo; echo "结果: PASS=$PASS FAIL=$FAIL"; exit 1
fi

post_branch() {
  curl -s -X POST "http://127.0.0.1:$PORT/api/sessions" \
    -H 'Content-Type: application/json' \
    -d "{\"parent_session\":\"2026-10-02__dev-backlog\",\"domain\":\"$BASE\",\"name\":\"$MARK\"}"
}

echo "[3] 第一次建分支"
r1=$(post_branch)
echo "    响应: $r1"
n1=$(echo "$r1" | node -e "let s='';process.stdin.on('data',d=>s+=d).on('end',()=>{try{const j=JSON.parse(s);console.log(j.name||'')}catch(e){console.log('')}})")
if [ "$n1" = "$MARK" ]; then ok "首次创建得到预期名 $MARK"; else ng "首次创建名字异常: '$n1'"; fi

echo "[4] 第二次请求同名分支（模拟下一轮定时任务）"
r2=$(post_branch)
echo "    响应: $r2"
n2=$(echo "$r2" | node -e "let s='';process.stdin.on('data',d=>s+=d).on('end',()=>{try{const j=JSON.parse(s);console.log(j.name||'')}catch(e){console.log('')}})")
if [ "$n2" = "$MARK" ]; then
  ok "同名请求被复用，返回 $MARK"
else
  ng "同名请求未复用，被去重成 '$n2'（旧行为：每轮新建 -2/-3 空壳）"
fi

echo "[5] 磁盘上不得出现 -2/-3/-4 空壳目录"
extra=0
for suf in "-2" "-3" "-4"; do
  if [ -d "$SESSIONS_DIR/${MARK}${suf}" ]; then
    ng "多出空壳目录 ${MARK}${suf}"
    extra=1
  fi
done
[ "$extra" -eq 0 ] && ok "磁盘上只有 $MARK 一个目录"

echo "[6] 复用时不得把已有内容清空（先写一条消息，再触发第二次请求）"
mf="$SESSIONS_DIR/$MARK/$MARK.json"
if [ -f "$mf" ]; then
  node -e "
const fs=require('fs');const p=process.argv[1];
const j=JSON.parse(fs.readFileSync(p,'utf8'));
j.messages=[{role:'user',content:'回归哨兵消息'},{role:'assistant',content:'哨兵回复'}];
fs.writeFileSync(p,JSON.stringify(j,null,2),'utf-8');
" "$mf"
  before=$(node -e "try{const j=require('$mf');console.log((j.messages||[]).length)}catch(e){console.log('ERR')}")
  post_branch >/dev/null   # 再来一次同样的请求，此时应走复用
  after=$(node -e "try{const j=require('$mf');console.log((j.messages||[]).length)}catch(e){console.log('ERR')}")
  if [ "$after" = "$before" ] && [ "$after" = "2" ]; then
    ok "复用后原有 $after 条消息完整保留"
  else
    ng "复用把内容写坏了：before=$before after=$after"
  fi
else
  ng "会话文件不存在: $mf"
fi

echo
echo "结果: PASS=$PASS FAIL=$FAIL"
[ "$FAIL" -eq 0 ]

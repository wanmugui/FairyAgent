#!/usr/bin/env bash
# repo-guard.sh —— 防止「冲突标记 / 坏 JSON」把 Fairy 打挂
#
# 背景：config/config.json 里残留过 git 冲突标记，前端每次读它都 JSON.parse 失败，
# API 进程退出 → systemd 拉起 → 再崩，用户看到的就是"前端一直刷新"。
#
# 用法：
#   bash scripts/repo-guard.sh                # 全仓库体检
#   bash scripts/repo-guard.sh --staged       # 只查已 staged 的文件（git hook 用）
#   bash scripts/repo-guard.sh --fix-config   # 若 config/config.json 坏了，用最近备份还原
#   bash scripts/repo-guard.sh --quiet
#
# 退出码：0 = 正常，1 = 发现问题

set -uo pipefail
cd "$(dirname "$0")/.." || exit 2

QUIET=0
STAGED=0
FIX=0
for a in "$@"; do
  case "$a" in
    --quiet) QUIET=1 ;;
    --staged) STAGED=1 ;;
    --fix-config) FIX=1 ;;
  esac
done

say() { [ "$QUIET" = 1 ] || printf '%s\n' "$*"; }
FAIL=0

EXCLUDES=(--exclude-dir=node_modules --exclude-dir=.git --exclude-dir=runs --exclude-dir=dist
          --exclude='*.bak*' --exclude='*.conflictbak*' --exclude='*.orig' --exclude='*.log')

if [ "$STAGED" = 1 ]; then
  mapfile -t FILES < <(git diff --cached --name-only --diff-filter=ACM)
else
  mapfile -t FILES < <(git ls-files)
fi

# 1) 冲突标记
HIT=()
for f in "${FILES[@]:-}"; do
  [ -n "$f" ] || continue
  [ -f "$f" ] || continue
  case "$f" in *.bak*|*.conflictbak*|*.orig) continue ;; esac
  if grep -qE '^(<<<<<<< |>>>>>>> )' "$f" 2>/dev/null; then HIT+=("$f"); fi
done
if [ "${#HIT[@]}" -gt 0 ]; then
  say "!! 发现 git 冲突标记（会让前端/服务崩）："
  printf '   %s\n' "${HIT[@]}"
  FAIL=1
fi

# 2) JSON 合法性
JSONS=()
for f in "${FILES[@]:-}"; do
  [ -n "$f" ] || continue
  case "$f" in *.json) ;; *) continue ;; esac
  case "$f" in *.bak*|*.conflictbak*) continue ;; esac
  [ -f "$f" ] && JSONS+=("$f")
done
if [ "${#JSONS[@]}" -gt 0 ]; then
  if ! python3 - "${JSONS[@]}" <<'PY'
import json, sys
bad = []
for f in sys.argv[1:]:
    try:
        # utf-8-sig：仓库里有两个文件带 BOM，属于正常
        with open(f, encoding="utf-8-sig") as fh:
            text = fh.read()
    except Exception as e:
        bad.append((f, e))
        continue
    if not text.strip():
        continue  # 空占位文件不算坏
    try:
        json.loads(text)
    except Exception as e:
        bad.append((f, e))
for f, e in bad:
    print(f"!! JSON 解析失败：{f}\n   {e}")
sys.exit(1 if bad else 0)
PY
  then
    FAIL=1
  fi
fi

# 3) 关键配置修复（只在明确要求时动文件）
CFG="config/config.json"
if [ "$FIX" = 1 ] && [ -f "$CFG" ] && grep -qE '^(<<<<<<< |>>>>>>> )' "$CFG" 2>/dev/null; then
  BAK=$(ls -1t "$CFG".conflictbak* "$CFG".bak* 2>/dev/null | head -1)
  if [ -n "$BAK" ]; then
    cp "$BAK" "$CFG"
    say "已用备份还原 $CFG <- $BAK"
  else
    say "!! $CFG 有冲突标记，但找不到备份，需要人工处理"
  fi
fi

# 4) 「大文件缩水」检查：agent 误写/截断会把整段内容删掉（2026-10-03
#    filemanager/index-fm.html 就被删了 436 行，远端 UI 直接变形）
while read -r f; do
  [ -n "$f" ] || continue
  case "$f" in *.bak*|*.conflictbak*|*.orig) continue ;; esac
  [ -f "$f" ] || continue
  head_len=$(git show "HEAD:$f" 2>/dev/null | wc -c)
  work_len=$(wc -c < "$f")
  [ "$head_len" -ge 5000 ] || continue
  if [ "$work_len" -lt $(( head_len * 80 / 100 )) ]; then
    say "!! $f 比 HEAD 少了 $(( 100 - work_len * 100 / head_len ))%（$work_len / $head_len 字节），疑似被截断：
   确认无误用：git add $f（提交）或 git restore --source=HEAD $f（回滚）"
    FAIL=1
  fi
done < <(git diff --name-only --diff-filter=ACM 2>/dev/null)

[ "$FAIL" = 0 ] && say "repo-guard: OK"
exit "$FAIL"

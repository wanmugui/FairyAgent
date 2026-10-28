#!/usr/bin/env bash
# 安装 Fairy 仓库的 git hooks：任何 merge/pull/commit 之后立刻体检，
# 冲突标记或坏 JSON 不会再悄悄留到运行时。
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
HOOKS="$ROOT/.git/hooks"
mkdir -p "$HOOKS"

write_hook() {  # write_hook <名字> [额外参数]
  local name="$1"; shift
  cat > "$HOOKS/$name" <<EOF
#!/usr/bin/env bash
exec bash "$ROOT/scripts/repo-guard.sh" --quiet $*
EOF
  chmod +x "$HOOKS/$name"
}

write_hook pre-commit --staged
write_hook post-merge
write_hook post-rewrite
write_hook post-checkout

echo "已安装 git hooks 到 $HOOKS"
ls -1 "$HOOKS" | grep -E 'pre-commit|post-merge|post-rewrite|post-checkout'

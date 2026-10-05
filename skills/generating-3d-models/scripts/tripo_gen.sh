#!/usr/bin/env bash
# Tripo 3D 生成薄封装：登录检查 -> 选端点 -> 落外接盘 -> 校验产物
#
# 设计取舍：
#   * 不吞 stderr。认证/额度/参数错误都靠它定位，静默是本项目踩过的坑。
#   * 默认加 --no-open --json，非交互环境下 CLI 不会去开浏览器。
#   * 产物默认落外接盘（机械盘 143/168 MB/s），小文本不落这里。
#   * region=ov 时自动挂 mihomo 代理：海外站在本机直连不通，实测必须走 7897。
set -euo pipefail

export PATH="$HOME/.local/bin:$PATH"

ASSET_ROOT="${TRIPO_OUT_ROOT:-/media/harry/SHENSHENG/Fairy/assets/3d}"
TRIPO_BIN="$(command -v tripo || true)"
PROXY="${TRIPO_PROXY:-http://127.0.0.1:7897}"
CONFIG="$HOME/.tripo/config.json"

die() { echo "错误: $*" >&2; exit 1; }

usage() {
  cat >&2 <<'EOF'
用法:
  tripo_gen.sh concept2views <概念图> <资产名>        单图 -> 4视图正交表
  tripo_gen.sh views2model <资产名> <视图1> [视图2...]  2~4视图 -> 3D模型(带PBR)
  tripo_gen.sh text2model  <资产名> "<提示词>"        文本 -> 3D模型
  tripo_gen.sh image2model <资产名> <单图>            单图 -> 3D模型(一致性差)
  tripo_gen.sh doctor                                  环境与登录自检
  tripo_gen.sh balance                                 查额度
  tripo_gen.sh region <cn|ov>                          切换区域(ov 会自动用代理)

环境变量:
  TRIPO_OUT_ROOT   产物根目录，默认 /media/harry/SHENSHENG/Fairy/assets/3d
  TRIPO_PROXY      代理地址，默认 http://127.0.0.1:7897
EOF
  exit 2
}

current_region() {
  python3 - "$CONFIG" <<'PY' 2>/dev/null || echo ""
import json,sys
try:
    d=json.load(open(sys.argv[1]))
    for v in (d.get("profiles") or d).values():
        if isinstance(v,dict) and v.get("region"):
            print(v["region"]); break
except Exception:
    pass
PY
}

# 海外站直连不通，必须走代理；国内站走代理反而会连崩（实测过）
apply_proxy() {
  local r; r="$(current_region)"
  if [ "$r" = "ov" ]; then
    export HTTPS_PROXY="$PROXY" HTTP_PROXY="$PROXY"
    export https_proxy="$PROXY" http_proxy="$PROXY"
    if ! (exec 3<>/dev/tcp/127.0.0.1/7897) 2>/dev/null; then
      die "当前区域是 ov(海外)，但代理 $PROXY 没在监听。
     先启动: bash ~/.mihomo/mihomoctl.sh start"
    fi
  else
    unset HTTPS_PROXY HTTP_PROXY https_proxy http_proxy || true
  fi
}

preflight() {
  [ -n "$TRIPO_BIN" ] || die "找不到 tripo CLI。装法: npm install -g --prefix \$HOME/.local tripo-cli"
  apply_proxy
  if ! "$TRIPO_BIN" doctor --json 2>/dev/null | grep -q '"name":"api key","ok":true'; then
    die "未登录 Tripo。

  国内站(无需代理): tripo login --key tsk_... --region cn
  海外站(需 mihomo): tripo login --key tsk_... --region ov"
  fi
  [ -d "$ASSET_ROOT" ] || mkdir -p "$ASSET_ROOT" \
    || die "外接盘目录不可写: $ASSET_ROOT（盘挂载了吗？）"
}

run() {
  local out="$ASSET_ROOT/$1"; shift
  mkdir -p "$out"
  echo "→ 输出: $out"
  "$TRIPO_BIN" "$@" -o "$out" --json --no-open
  echo
  echo "产物:"
  find "$out" -type f -printf '  %s\t%p\n' 2>/dev/null | head -20
  local n; n=$(find "$out" -type f 2>/dev/null | wc -l)
  [ "$n" -gt 0 ] || die "目录里没有任何产物，生成多半失败了（看上面的错误输出）"
}

[ $# -ge 1 ] || usage
case "$1" in
  region)        [ $# -eq 2 ] || usage
    if [ "$2" = ov ]; then export HTTPS_PROXY="$PROXY" HTTP_PROXY="$PROXY"; fi
    echo "当前区域: $(current_region)（改区域请用 tripo login --region cn|ov）" ;;
  doctor)        apply_proxy; "$TRIPO_BIN" doctor ;;
  balance)       apply_proxy; "$TRIPO_BIN" balance ;;
  concept2views) [ $# -eq 3 ] || usage; preflight
    run "$2" generate image-to-multiview "$3" ;;
  views2model)   [ $# -ge 4 ] || usage; shift; local name="$1"; shift; preflight
    run "$name" generate multiview-to-model "$@" -p pbr=true -p texture=true -p texture_quality=detailed ;;
  text2model)    [ $# -eq 3 ] || usage; preflight
    run "$2" generate text-to-model "$3" ;;
  image2model)   [ $# -eq 3 ] || usage; preflight
    run "$2" generate image-to-model "$3" -p pbr=true -p texture=true ;;
  *)             usage ;;
esac

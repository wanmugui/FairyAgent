#!/usr/bin/env sh
# Fairy launcher (macOS / Linux).
# 1) desktop binary published in the repo root
# 2) raw cargo target binary
# 3) fall back to `pnpm dev`
set -e
cd "$(dirname "$0")"

if [ -x "./Fairy" ]; then
  exec "./Fairy"
fi
if [ -x "./src-tauri/target/release/fairy" ]; then
  exec "./src-tauri/target/release/fairy"
fi

if ! command -v pnpm >/dev/null 2>&1; then
  echo "[Fairy] pnpm was not found in PATH. Install Node.js + pnpm first." >&2
  exit 1
fi
echo "[Fairy] Desktop binary not found; starting pnpm dev ..."
exec pnpm dev

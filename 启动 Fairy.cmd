@echo off
REM Fairy launcher (Windows).
REM 1) desktop binary published in the repo root
REM 2) raw cargo target binary
REM 3) fall back to `pnpm dev`
setlocal
cd /d "%~dp0"

if exist "%~dp0Fairy.exe" (
  start "" "%~dp0Fairy.exe"
  exit /b 0
)
if exist "%~dp0src-tauri\target\release\fairy.exe" (
  start "" "%~dp0src-tauri\target\release\fairy.exe"
  exit /b 0
)

echo [Fairy] Desktop binary not found; starting "pnpm dev" instead ...
where pnpm >NUL 2>NUL
if errorlevel 1 (
  echo [Fairy] pnpm was not found in PATH. Install Node.js + pnpm first.
  pause
  exit /b 1
)
start "Fairy dev" cmd /k pnpm dev

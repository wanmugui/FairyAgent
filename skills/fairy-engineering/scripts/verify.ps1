param(
  [switch]$FrontendClickTestPassed,
  [string]$ClickTestEvidence = ""
)

$ErrorActionPreference = "Stop"
$scriptDir = Split-Path -Parent $MyInvocation.MyCommand.Path
$repo = (Resolve-Path (Join-Path $scriptDir "..\..\..")).Path
Set-Location $repo

function Get-ChangedFiles {
  $lines = & git -C $repo status --porcelain=v1
  foreach ($line in $lines) {
    if (-not $line -or $line.Length -lt 4) { continue }
    $path = $line.Substring(3).Trim()
    if ($path -match " -> ") { $path = ($path -split " -> ")[-1].Trim() }
    $path = $path.Trim('"')
    if ($path) { $path }
  }
}

function Invoke-External {
  param(
    [Parameter(Mandatory = $true)][string]$Name,
    [Parameter(Mandatory = $true)][scriptblock]$Action
  )
  Write-Host ""
  Write-Host "== $Name ==" -ForegroundColor Cyan
  & $Action
  if ($LASTEXITCODE -ne 0) {
    throw "$Name failed with exit code $LASTEXITCODE"
  }
}

$changed = @(Get-ChangedFiles | Select-Object -Unique)
$agentChanged = @($changed | Where-Object { $_ -match "^agent/" })
$frontendChanged = @($changed | Where-Object { $_ -match "^frontend/" })
$promptChanged = @($changed | Where-Object { $_ -match "^config/system/|^config/tools/schemas\.json$|^config/config\.json$" })
$uiChanged = @($changed | Where-Object { $_ -match "^frontend/|^src-tauri/" -and $_ -notmatch "\.(md|txt)$" }).Count -gt 0

Write-Host "Fairy verification gate" -ForegroundColor Green
Write-Host "Repository: $repo"
Write-Host "Changed files: $($changed.Count)"
$changed | ForEach-Object { Write-Host "  $_" }

if ($agentChanged.Count -gt 0) {
  Invoke-External "Go regression (agent)" {
    Push-Location (Join-Path $repo "agent")
    try { & go test ./... } finally { Pop-Location }
  }
}

if ($frontendChanged.Count -gt 0) {
  Invoke-External "Frontend server syntax" {
    & node --check (Join-Path $repo "frontend\server.cjs")
  }

  $vite = Join-Path $repo "frontend\node_modules\.bin\vite.cmd"
  if (-not (Test-Path -LiteralPath $vite)) {
    throw "Vite is not installed at $vite. Install frontend dependencies or run the project's documented build command."
  }
  $outDir = Join-Path $env:TEMP ("fairy-verify-build-" + [Guid]::NewGuid().ToString("N"))
  Invoke-External "Frontend production build" {
    Push-Location (Join-Path $repo "frontend")
    try { & $vite build --outDir $outDir --emptyOutDir } finally { Pop-Location }
  }
  Write-Host "Build output: $outDir"
}

if ($promptChanged.Count -gt 0) {
  Invoke-External "Prompt contract check" {
    & pnpm prompt:check
  }
}

if ($uiChanged) {
  if (-not $FrontendClickTestPassed) {
    throw "UI changed. Run a real browser click test, then rerun with -FrontendClickTestPassed -ClickTestEvidence '<URL/actions/result>'."
  }
  if ([string]::IsNullOrWhiteSpace($ClickTestEvidence)) {
    throw "UI changed and -FrontendClickTestPassed was supplied, but -ClickTestEvidence is empty."
  }
  Write-Host ""
  Write-Host "Browser click evidence: $ClickTestEvidence" -ForegroundColor Green
}

if ($changed.Count -eq 0) {
  Write-Host "No changed files detected." -ForegroundColor Yellow
}

Write-Host ""
Write-Host "Verification gate passed." -ForegroundColor Green
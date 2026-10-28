<#
  End to end check for the pixel path used by computer control.

  The point of this test is that it does not trust the vision service: it takes
  the label and rectangle of a control from UI Automation (exact, free) and then
  asks the vision service to find the same label from pixels alone. The OCR
  answer must land inside the UI Automation rectangle, and a real click on the
  returned point must produce a visible change.

      UI Automation  ->  ground truth label + rectangle
      OCR (pixels)   ->  independently located point
      SendInput      ->  click at the OCR point
      image_diff     ->  prove the click did something

  Usage:
    powershell -ExecutionPolicy Bypass -File tools\computer-broker\vision-smoke-test.ps1
#>
[CmdletBinding()]
param(
    [string]$Broker,
    [int]$Port = 8791,
    [int]$BoundsTolerancePx = 12,
    [switch]$KeepNotepad
)

$ErrorActionPreference = 'Stop'
$repoRoot = Split-Path -Parent (Split-Path -Parent $PSScriptRoot)
$python = Join-Path $repoRoot '.tools\venv\Scripts\python.exe'
$serviceScript = Join-Path $repoRoot '.tools\vision-service\cua_vision_service.py'

if (-not $Broker) {
    $Broker = Join-Path $repoRoot '.tools\computer-broker\FairyComputerBroker.exe'
}
if (-not (Test-Path -LiteralPath $Broker)) { throw "broker not found: $Broker" }
if (-not (Test-Path -LiteralPath $python)) { throw "project python not found: $python" }
if (-not (Test-Path -LiteralPath $serviceScript)) { throw "vision service not found: $serviceScript" }

$artifacts = Join-Path $repoRoot ('workspace\result\vision-smoke\' + (Get-Date -Format 'yyyyMMdd-HHmmss'))
New-Item -ItemType Directory -Force -Path $artifacts | Out-Null
$baseUrl = "http://127.0.0.1:$Port"

function Invoke-Broker {
    param([hashtable]$Request)
    $json = $Request | ConvertTo-Json -Compress -Depth 10
    $lines = $json | & $Broker
    $line = $lines | Where-Object { $_ -like '__FAIRY_CUA_RESULT__*' } | Select-Object -Last 1
    if (-not $line) { throw "broker returned no result: $($lines -join ' ')" }
    return ($line -replace '^__FAIRY_CUA_RESULT__', '') | ConvertFrom-Json
}

$failures = New-Object System.Collections.Generic.List[string]
function Assert-True {
    param([bool]$Condition, [string]$Message)
    if ($Condition) { Write-Host "  [ok] $Message" } else { Write-Host "  [FAIL] $Message"; $failures.Add($Message) }
}

# Windows PowerShell 5.1 encodes Invoke-RestMethod bodies as ASCII by default,
# which silently destroys non-ASCII control labels and turns every lookup into
# "not found". Always hand the service explicit UTF-8 bytes.
function Invoke-VisionJson {
    param(
        [string]$Path,
        [hashtable]$Payload,
        [int]$TimeoutSec = 300
    )
    $json = $Payload | ConvertTo-Json -Compress -Depth 8
    $bytes = [System.Text.Encoding]::UTF8.GetBytes($json)
    return Invoke-RestMethod "$baseUrl/$Path" -Method Post -Body $bytes `
        -ContentType 'application/json; charset=utf-8' -TimeoutSec $TimeoutSec
}

# --- start (or adopt) the vision service ---------------------------------
Write-Host '0. vision service'
$started = $false
$healthy = $false
try {
    $health = Invoke-RestMethod "$baseUrl/health" -TimeoutSec 3
    $healthy = $true
    Write-Host "  adopting already running service (uptime $($health.uptime_s)s)"
} catch { }
if (-not $healthy) {
    $log = Join-Path $artifacts 'vision-service.log'
    Start-Process -FilePath $python -ArgumentList @(
        $serviceScript, '--port', "$Port", '--warmup', '--idle-timeout', '300'
    ) -RedirectStandardOutput $log -RedirectStandardError "$log.err" -WindowStyle Hidden
    $started = $true
    foreach ($attempt in 1..90) {
        Start-Sleep -Milliseconds 500
        try { $health = Invoke-RestMethod "$baseUrl/health" -TimeoutSec 2; $healthy = $true; break } catch { }
    }
}
if (-not $healthy) { throw 'vision service did not become healthy' }
Assert-True ($health.ok -eq $true) 'vision service reports healthy'
Write-Host "  capture backend: $($health.capture_backend)"

# --- Notepad must be in the foreground before we read either source ------
Write-Host '1. focus Notepad'
$startedNotepad = $false
if (-not (Get-Process -Name notepad -ErrorAction SilentlyContinue)) {
    Start-Process notepad.exe
    $startedNotepad = $true
    Start-Sleep -Seconds 2
}
$window = $null
$found = Invoke-Broker @{ tool = 'computer_window'; action = 'find'; title = 'Notepad' }
if ($found.count -gt 0) { $window = @($found.windows)[0] }
if (-not $window) { throw 'Notepad window was not found' }
$handle = $window.handle
$activated = Invoke-Broker @{ tool = 'computer_window'; action = 'activate'; handle = $handle; settle_ms = 500 }
Assert-True ($activated.active -eq $true) 'Notepad is the foreground window'
# Re-read the frame *after* activation rather than reusing the one from find:
# a minimized window reports the (-32000, -32000) sentinel there, and cropping
# that region makes OCR look broken when the real problem is a minimized window.
$frame = $activated.window.frame
Assert-True ($activated.frame_valid -eq $true) 'the window has usable geometry after activation'
Write-Host ("  frame {0},{1} {2}x{3}" -f $frame.x, $frame.y, $frame.width, $frame.height)

# --- ground truth from UI Automation -------------------------------------
Write-Host '2. ground truth from UI Automation'
$tree = Invoke-Broker @{ tool = 'computer_observe'; action = 'ui_tree'; handle = $handle; max_depth = 6; max_nodes = 250 }
$truth = $null
foreach ($node in @($tree.nodes)) {
    if (($node.automation_id -eq 'File' -or $node.name -eq 'File') -and $node.bounds) {
        if ($node.bounds.width -gt 0 -and $node.bounds.height -gt 0) { $truth = $node; break }
    }
}
if (-not $truth) { throw 'UI Automation did not expose the File menu item' }
Write-Host ("  UIA label='{0}' bounds={1},{2} {3}x{4}" -f $truth.name, $truth.bounds.x, $truth.bounds.y, $truth.bounds.width, $truth.bounds.height)

# --- vision locates the same label from pixels ---------------------------
Write-Host '3. vision locates it from pixels'
$visionBody = @{
    text   = $truth.name
    region = @{ x = [int]$frame.x; y = [int]$frame.y; width = [int]$frame.width; height = [int]$frame.height }
}

$watch = [System.Diagnostics.Stopwatch]::StartNew()
$located = Invoke-VisionJson -Path 'locate' -Payload $visionBody
$watch.Stop()
$coldMs = $watch.ElapsedMilliseconds
Write-Host ("  cold lookup {0} ms  found={1} name='{2}' point={3},{4}" -f $coldMs, $located.found, $located.name, $located.point.x, $located.point.y)

Assert-True ($located.found -eq $true) 'vision found a matching label from pixels'
if (-not $located.found) {
    Write-Host "  reason: $($located.reason)"
    foreach ($candidate in @($located.candidates)) {
        Write-Host ("  candidate ratio={0} text='{1}'" -f $candidate.ratio, $candidate.text)
    }
    throw 'vision could not locate the label, the rest of the test cannot run'
}

# The located point must fall inside the rectangle UI Automation reported.
$px = [int]$located.point.x
$py = [int]$located.point.y
$inside = ($px -ge ($truth.bounds.x - $BoundsTolerancePx)) -and
          ($px -le ($truth.bounds.x + $truth.bounds.width + $BoundsTolerancePx)) -and
          ($py -ge ($truth.bounds.y - $BoundsTolerancePx)) -and
          ($py -le ($truth.bounds.y + $truth.bounds.height + $BoundsTolerancePx))
Assert-True $inside "OCR point ($px,$py) lands inside the UIA rectangle (tolerance ${BoundsTolerancePx}px)"

$watch.Restart()
$cached = Invoke-VisionJson -Path 'locate' -Payload $visionBody
$watch.Stop()
Write-Host ("  repeat lookup {0} ms" -f $watch.ElapsedMilliseconds)
Assert-True ($watch.ElapsedMilliseconds -lt $coldMs) 'a repeat lookup on the same frame is cheaper than the cold one'

# --- click the point vision returned -------------------------------------
Write-Host '4. click the vision point'
$beforePath = Join-Path $artifacts 'before.png'
$before = Invoke-Broker @{ tool = 'computer_observe'; action = 'screenshot'; output_path = $beforePath }
$click = Invoke-Broker @{ tool = 'computer_pointer'; action = 'click'; x = $px; y = $py; coordinate_space = 'screen'; expected_geometry_hash = $before.geometry_hash }
Assert-True ($click.verified -eq $true) ("cursor landed exactly (delta {0},{1} via {2})" -f $click.delta_x, $click.delta_y, $click.move_method)
Start-Sleep -Milliseconds 550
$afterPath = Join-Path $artifacts 'after.png'
$after = Invoke-Broker @{ tool = 'computer_observe'; action = 'screenshot'; output_path = $afterPath }

$diff = Invoke-Broker @{
    tool        = 'computer_observe'
    action      = 'image_diff'
    before_path = $beforePath
    after_path  = $afterPath
    diff_path   = (Join-Path $artifacts 'diff.png')
    tolerance   = 4
    threshold   = 0.0005
    region      = @{ x = [int]$frame.x; y = [int]$frame.y; width = [int]$frame.width; height = [int]$frame.height }
}
Assert-True ($diff.changed -eq $true) ("the click changed the window ({0} px)" -f $diff.changed_pixels)

# --- cleanup -------------------------------------------------------------
Invoke-Broker @{ tool = 'computer_keyboard'; action = 'press'; key = 'escape' } | Out-Null
if ($startedNotepad -and -not $KeepNotepad) {
    Start-Sleep -Milliseconds 250
    Get-Process -Name notepad -ErrorAction SilentlyContinue | Stop-Process -Force -ErrorAction SilentlyContinue
}
if ($started) {
    try { Invoke-RestMethod "$baseUrl/shutdown" -Method Post -Body '{}' -ContentType 'application/json' -TimeoutSec 5 | Out-Null } catch { }
}

Write-Host ''
if ($failures.Count -gt 0) {
    Write-Host ("FAILED: {0}" -f ($failures -join '; '))
    exit 1
}
Write-Host 'PASS: vision located a control from pixels and clicked it'
Write-Host "evidence: $artifacts"
exit 0

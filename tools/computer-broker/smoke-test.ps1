<#
  Real desktop click regression for the Windows computer broker.

  It does not trust "the API returned success": every step is verified against a
  screenshot.

    1. self test        - DPI aware geometry is readable
    2. find + activate  - Notepad is located and focused
    3. ui_tree          - the "File" menu item is located through UI Automation
    4. screenshot       - "before" frame
    5. click            - raw SendInput at the menu item centre; the cursor
                          position is read back and must match the request
    6. screenshot       - "after" frame
    7. image_diff       - the menu region must have actually changed

  Usage:
    powershell -ExecutionPolicy Bypass -File tools\computer-broker\smoke-test.ps1
#>
[CmdletBinding()]
param(
    [string]$Broker,
    [string]$ArtifactDirectory,
    [switch]$KeepNotepad
)

$ErrorActionPreference = 'Stop'
$repoRoot = Split-Path -Parent (Split-Path -Parent $PSScriptRoot)

function Resolve-Broker {
    param([string]$Explicit)
    if ($Explicit) {
        if (-not (Test-Path -LiteralPath $Explicit)) { throw "broker not found: $Explicit" }
        return (Resolve-Path -LiteralPath $Explicit).Path
    }
    $cached = Join-Path $repoRoot '.tools\computer-broker\FairyComputerBroker.exe'
    if (-not (Test-Path -LiteralPath $cached)) {
        Write-Host '[..] building computer broker'
        & (Join-Path $PSScriptRoot 'build-broker.ps1')
    }
    if (-not (Test-Path -LiteralPath $cached)) { throw "broker not found: $cached" }
    return $cached
}

$brokerPath = Resolve-Broker -Explicit $Broker
if (-not $ArtifactDirectory) {
    $ArtifactDirectory = Join-Path $repoRoot ('workspace\result\computer-broker-smoke\' + (Get-Date -Format 'yyyyMMdd-HHmmss'))
}
New-Item -ItemType Directory -Force -Path $ArtifactDirectory | Out-Null

function Invoke-Broker {
    param([hashtable]$Request)
    $json = $Request | ConvertTo-Json -Compress -Depth 8
    $lines = $json | & $brokerPath
    $line = $lines | Where-Object { $_ -like '__FAIRY_CUA_RESULT__*' } | Select-Object -Last 1
    if (-not $line) {
        throw "broker returned no result for $($Request.action): $($lines -join ' ')"
    }
    return ($line -replace '^__FAIRY_CUA_RESULT__', '') | ConvertFrom-Json
}

$failures = New-Object System.Collections.Generic.List[string]
function Assert-True {
    param([bool]$Condition, [string]$Message)
    if ($Condition) {
        Write-Host "  [ok] $Message"
    } else {
        Write-Host "  [FAIL] $Message"
        $failures.Add($Message)
    }
}

Write-Host "broker : $brokerPath"
Write-Host "artifacts: $ArtifactDirectory"

# --- 1. self test ---------------------------------------------------------
Write-Host '1. self test'
$probe = & $brokerPath --self-test | Where-Object { $_ -like '__FAIRY_CUA_RESULT__*' }
$info = ($probe -replace '^__FAIRY_CUA_RESULT__', '') | ConvertFrom-Json
Assert-True ($info.ok -eq $true) 'broker reports ok'
Assert-True ([bool]$info.geometry_hash) 'geometry hash is present'
Assert-True ($info.monitors.Count -ge 1) 'at least one monitor is reported'
Write-Host ("  screen {0}x{1} scale={2}" -f $info.width, $info.height, $info.monitors[0].scale)

# --- 2. locate and activate Notepad --------------------------------------
Write-Host '2. locate + activate Notepad'
$startedHere = $false
$existing = Get-Process -Name notepad -ErrorAction SilentlyContinue
if (-not $existing) {
    Start-Process notepad.exe
    $startedHere = $true
    Start-Sleep -Seconds 2
}

$window = $null
foreach ($title in @('Notepad')) {
    $found = Invoke-Broker @{ tool = 'computer_window'; action = 'find'; title = $title }
    if ($found.count -gt 0) {
        $window = @($found.windows)[0]
        break
    }
}
if (-not $window) { throw 'Notepad window was not found' }
$handle = $window.handle
Write-Host ("  handle={0} title={1} frame={2},{3} {4}x{5}" -f $handle, $window.title, $window.frame.x, $window.frame.y, $window.frame.width, $window.frame.height)

$activated = Invoke-Broker @{ tool = 'computer_window'; action = 'activate'; handle = $handle; settle_ms = 450 }
Assert-True ($activated.active -eq $true) 'Notepad is the foreground window'

# --- 3. locate the File menu through UIA ---------------------------------
Write-Host '3. locate the File menu through UI Automation'
$tree = Invoke-Broker @{ tool = 'computer_observe'; action = 'ui_tree'; handle = $handle; max_depth = 6; max_nodes = 250 }
$menuItem = $null
foreach ($node in @($tree.nodes)) {
    if ($node.automation_id -eq 'File' -or $node.name -eq 'File') {
        if ($node.bounds -and $node.bounds.width -gt 0) { $menuItem = $node; break }
    }
}
if (-not $menuItem) { throw 'The File menu item was not exposed through UI Automation' }
$clickX = [int]($menuItem.bounds.x + [math]::Floor($menuItem.bounds.width / 2))
$clickY = [int]($menuItem.bounds.y + [math]::Floor($menuItem.bounds.height / 2))
Write-Host ("  menu '{0}' bounds={1},{2} {3}x{4} -> click {5},{6}" -f $menuItem.name, $menuItem.bounds.x, $menuItem.bounds.y, $menuItem.bounds.width, $menuItem.bounds.height, $clickX, $clickY)

# --- 4. screenshot before ------------------------------------------------
Write-Host '4. screenshot before'
$beforePath = Join-Path $ArtifactDirectory 'before.png'
$before = Invoke-Broker @{ tool = 'computer_observe'; action = 'screenshot'; output_path = $beforePath }
Assert-True (Test-Path -LiteralPath $beforePath) 'before screenshot was written'

# --- 5. click ------------------------------------------------------------
Write-Host '5. click the menu item'
$click = Invoke-Broker @{ tool = 'computer_pointer'; action = 'click'; x = $clickX; y = $clickY; coordinate_space = 'screen'; expected_geometry_hash = $before.geometry_hash }
Assert-True ($click.verified -eq $true) ("cursor landed on the requested point (delta {0},{1} via {2})" -f $click.delta_x, $click.delta_y, $click.move_method)
Assert-True ($click.cursor_after.x -eq $clickX -and $click.cursor_after.y -eq $clickY) 'cursor read-back matches the request'
Start-Sleep -Milliseconds 550

# --- 6. screenshot after -------------------------------------------------
Write-Host '6. screenshot after'
$afterPath = Join-Path $ArtifactDirectory 'after.png'
$after = Invoke-Broker @{ tool = 'computer_observe'; action = 'screenshot'; output_path = $afterPath }
Assert-True (Test-Path -LiteralPath $afterPath) 'after screenshot was written'

# --- 7. diff the window region -------------------------------------------
Write-Host '7. verify the click had a visible effect'
$diffPath = Join-Path $ArtifactDirectory 'diff.png'
$diff = Invoke-Broker @{
    tool        = 'computer_observe'
    action      = 'image_diff'
    before_path = $beforePath
    after_path  = $afterPath
    diff_path   = $diffPath
    tolerance   = 4
    threshold   = 0.0005
    region      = @{ x = $window.frame.x; y = $window.frame.y; width = $window.frame.width; height = $window.frame.height }
}
Assert-True ($diff.changed -eq $true) ("the window region changed ({0} px, ratio {1})" -f $diff.changed_pixels, $diff.changed_ratio)
$box = if ($diff.dense_bounding_box) { $diff.dense_bounding_box } else { $diff.bounding_box }
Assert-True ($box.y -ge $window.frame.y) 'changed area lies inside the window'
Assert-True ($box.height -gt 0 -and $box.width -gt 0) 'changed area has a measurable extent'
Write-Host ("  changed bbox {0},{1} {2}x{3}" -f $box.x, $box.y, $box.width, $box.height)
Assert-True ($box.height -lt $window.frame.height) 'changed area is a panel, not the whole window'

# --- cleanup -------------------------------------------------------------
Invoke-Broker @{ tool = 'computer_keyboard'; action = 'press'; key = 'escape' } | Out-Null
if ($startedHere -and -not $KeepNotepad) {
    Start-Sleep -Milliseconds 250
    Get-Process -Name notepad -ErrorAction SilentlyContinue | Stop-Process -Force -ErrorAction SilentlyContinue
}

Write-Host ''
if ($failures.Count -gt 0) {
    Write-Host ("FAILED: {0}" -f ($failures -join '; '))
    exit 1
}
Write-Host 'PASS: broker click regression succeeded'
Write-Host "evidence: $ArtifactDirectory"
exit 0

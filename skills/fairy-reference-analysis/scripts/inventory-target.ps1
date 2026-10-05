param(
  [Parameter(Mandatory = $true)][string]$TargetPath,
  [string]$OutputPath = ""
)

$ErrorActionPreference = "Stop"
$target = (Resolve-Path -LiteralPath $TargetPath).Path
if (-not (Test-Path -LiteralPath $target -PathType Container)) {
  throw "TargetPath must be a directory: $TargetPath"
}

$scriptDir = Split-Path -Parent $MyInvocation.MyCommand.Path
$repo = (Resolve-Path (Join-Path $scriptDir "..\..\..")).Path
if ([string]::IsNullOrWhiteSpace($OutputPath)) {
  $slug = (Split-Path $target -Leaf) -replace "[^A-Za-z0-9_.-]", "_"
  $OutputPath = Join-Path $repo ("workspace\reference-analysis\" + $slug + "-inventory.md")
}
$outDir = Split-Path -Parent $OutputPath
if ($outDir) { New-Item -ItemType Directory -Force -Path $outDir | Out-Null }

function Read-JsonSafe([string]$Path) {
  try { return Get-Content -LiteralPath $Path -Raw | ConvertFrom-Json } catch { return $null }
}

function Get-RelativePath([string]$Path) {
  $rel = $Path.Substring($target.Length).TrimStart('\', '/')
  return ($rel -replace '\\', '/')
}

$files = @(Get-ChildItem -LiteralPath $target -Recurse -File -ErrorAction SilentlyContinue)
$extensions = $files | Group-Object { if ($_.Extension) { $_.Extension.ToLowerInvariant() } else { '(none)' } } | Sort-Object Count -Descending | Select-Object -First 18
$encrypted = @($files | Where-Object { $_.Extension -match '^\.(png_|ogg_|rpgmvp|rpgmvo|rpgmvm|rpgmva)$' })
$images = @($files | Where-Object { $_.Extension -match '^\.(png|jpg|jpeg|webp|svg|gif)$' })
$dataJson = @($files | Where-Object { $_.Extension -ieq '.json' -and $_.FullName -match '\\data(Ex)?\\' })
$scriptFiles = @($files | Where-Object { $_.Extension -match '^\.(js|mjs|cjs|ts|tsx|jsx)$' })
$nativeFiles = @($files | Where-Object { $_.Extension -match '^\.(exe|dll|so|dylib|bin|dat|pak|pck|asar)$' })

$engine = "Unknown"
$engineEvidence = New-Object System.Collections.Generic.List[string]
$electronPackagePath = Join-Path $target "resources\app\package.json"
$rmmzPackagePath = Join-Path $target "resources\app\app\package.json"
$unityPath = Join-Path $target "UnityPlayer.dll"
$godotProject = Join-Path $target "project.godot"
$uePak = @($files | Where-Object { $_.Extension -ieq '.pak' }).Count -gt 0

if (Test-Path -LiteralPath $electronPackagePath) {
  $engineEvidence.Add("resources/app/package.json")
}
if (Test-Path -LiteralPath $rmmzPackagePath) {
  $rmmzPackage = Read-JsonSafe $rmmzPackagePath
  if ($rmmzPackage -and $rmmzPackage.name -match 'rmmz') {
    $engine = "Electron + RPG Maker MZ"
    $engineEvidence.Add("resources/app/app/package.json name=$($rmmzPackage.name)")
  }
} elseif (Test-Path -LiteralPath $unityPath) {
  $engine = "Unity"
  $engineEvidence.Add("UnityPlayer.dll")
} elseif (Test-Path -LiteralPath $godotProject) {
  $engine = "Godot"
  $engineEvidence.Add("project.godot")
} elseif ($uePak) {
  $engine = "Unreal Engine (probable)"
  $engineEvidence.Add("*.pak present")
} elseif (Test-Path -LiteralPath $electronPackagePath) {
  $engine = "Electron (probable)"
}

$systemJsonPath = Join-Path $target "resources\app\app\data\System.json"
$systemSummary = $null
if (Test-Path -LiteralPath $systemJsonPath) {
  $system = Read-JsonSafe $systemJsonPath
  if ($system) {
    $systemSummary = [ordered]@{
      gameTitle = $system.gameTitle
      screenWidth = $system.advanced.screenWidth
      screenHeight = $system.advanced.screenHeight
      battleSystem = $system.battleSystem
      hasEncryptedImages = $system.hasEncryptedImages
      hasEncryptedAudio = $system.hasEncryptedAudio
      menuCommands = $system.menuCommands
      itemCategories = $system.itemCategories
    }
  }
}

$uiDirs = @("resources\app\app\img\system", "resources\app\app\img\pictures", "resources\app\app\img\faces", "resources\app\app\effects", "resources\app\app\css", "resources\app\app\js\plugins", "resources\app\app\data", "resources\app\app\dataEx")
$uiDirSummary = foreach ($rel in $uiDirs) {
  $full = Join-Path $target $rel
  if (Test-Path -LiteralPath $full -PathType Container) {
    $count = @(Get-ChildItem -LiteralPath $full -Recurse -File -ErrorAction SilentlyContinue).Count
    [pscustomobject]@{ Path = ($rel -replace '\\', '/'); Files = $count }
  }
}

$mechanicsCandidates = @(
  "resources\app\app\data\System.json",
  "resources\app\app\data\Actors.json",
  "resources\app\app\data\Classes.json",
  "resources\app\app\data\Skills.json",
  "resources\app\app\data\Items.json",
  "resources\app\app\data\Enemies.json",
  "resources\app\app\data\States.json",
  "resources\app\app\data\Troops.json",
  "resources\app\app\data\CommonEvents.json"
) | Where-Object { Test-Path -LiteralPath (Join-Path $target $_) }

$now = Get-Date -Format "yyyy-MM-dd HH:mm:ss"
$sb = [System.Text.StringBuilder]::new()
[void]$sb.AppendLine("# Reference Target Inventory")
[void]$sb.AppendLine()
[void]$sb.AppendLine("- Target: ``$target``")
[void]$sb.AppendLine("- Generated: $now")
[void]$sb.AppendLine("- Access: read-only inventory; no decryption, extraction, or modification")
[void]$sb.AppendLine()
[void]$sb.AppendLine("## Detected stack")
[void]$sb.AppendLine()
[void]$sb.AppendLine("- Engine: **$engine**")
foreach ($evidence in $engineEvidence) { [void]$sb.AppendLine("- Evidence: ``$evidence``") }
[void]$sb.AppendLine("- Files: $($files.Count)")
[void]$sb.AppendLine("- Images: $($images.Count)")
[void]$sb.AppendLine("- Scripts: $($scriptFiles.Count)")
[void]$sb.AppendLine("- Native/bundles: $($nativeFiles.Count)")
[void]$sb.AppendLine("- Encrypted-looking assets: $($encrypted.Count)")
[void]$sb.AppendLine()
[void]$sb.AppendLine("## File extensions")
[void]$sb.AppendLine()
[void]$sb.AppendLine("| Extension | Count |")
[void]$sb.AppendLine("|---|---:|")
foreach ($ext in $extensions) { [void]$sb.AppendLine("| $($ext.Name) | $($ext.Count) |") }
[void]$sb.AppendLine()
[void]$sb.AppendLine("## UI / data directories")
[void]$sb.AppendLine()
[void]$sb.AppendLine("| Path | Files |")
[void]$sb.AppendLine("|---|---:|")
foreach ($item in $uiDirSummary) { [void]$sb.AppendLine("| ``$($item.Path)`` | $($item.Files) |") }
[void]$sb.AppendLine()
if ($null -ne $systemSummary) {
  [void]$sb.AppendLine("## RPG Maker System summary (unencrypted JSON)")
  [void]$sb.AppendLine()
  [void]$sb.AppendLine('```json')
  [void]$sb.AppendLine(($systemSummary | ConvertTo-Json -Depth 5))
  [void]$sb.AppendLine('```')
  [void]$sb.AppendLine()
}
[void]$sb.AppendLine("## Mechanics data candidates")
[void]$sb.AppendLine()
foreach ($rel in $mechanicsCandidates) {
  $full = Join-Path $target $rel
  $size = (Get-Item -LiteralPath $full).Length
  [void]$sb.AppendLine("- ``$($rel -replace '\\','/')`` ($size bytes)")
}
[void]$sb.AppendLine()
[void]$sb.AppendLine("## Unencrypted image samples")
[void]$sb.AppendLine()
$uiImageSamples = @($images | Where-Object { $_.FullName -match '\\img\\(system|pictures|faces)\\' } | Select-Object -First 15)
if ($uiImageSamples.Count -gt 0) {
  foreach ($image in $uiImageSamples) { [void]$sb.AppendLine("- ``$(Get-RelativePath $image.FullName)``") }
} else {
  $imageSamples = $images | Select-Object -First 15
  foreach ($image in $imageSamples) { [void]$sb.AppendLine("- ``$(Get-RelativePath $image.FullName)``") }
  if ($images.Count -gt $imageSamples.Count) { [void]$sb.AppendLine("- ... $($images.Count - $imageSamples.Count) more") }
}
[void]$sb.AppendLine()
[void]$sb.AppendLine("## Encryption / protection notes")
[void]$sb.AppendLine()
if ($encrypted.Count -gt 0) {
  [void]$sb.AppendLine("- Found $($encrypted.Count) encrypted-looking image/audio assets.")
  [void]$sb.AppendLine("- This script intentionally does not decrypt or extract them.")
  [void]$sb.AppendLine("- For UI reference, use screenshots/recordings or authorized source files supplied by the user.")
} else {
  [void]$sb.AppendLine("- No encrypted-looking asset extensions found by this heuristic.")
}
[void]$sb.AppendLine()
[void]$sb.AppendLine("## Next steps")
[void]$sb.AppendLine()
[void]$sb.AppendLine("1. Capture the main UI states and interaction flow.")
[void]$sb.AppendLine("2. Extract layout, color, type, shape, motion, and density tokens.")
[void]$sb.AppendLine("3. Separate observed mechanics from inferred formulas.")
[void]$sb.AppendLine("4. Re-express the result as an original implementation brief.")

Set-Content -LiteralPath $OutputPath -Value $sb.ToString() -Encoding UTF8
Write-Output $OutputPath
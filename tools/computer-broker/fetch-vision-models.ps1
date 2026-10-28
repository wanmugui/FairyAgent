<#
  Downloads and converts the YOLO detectors used by the vision service.

  Two models, both already trained - nothing here trains anything:

    ui-elements-detection      multi-class UI elements (YOLO11-L). Names what it
                               found: AXButton / AXDisclosureTriangle / AXImage /
                               AXLink / AXTextArea. Trained on macOS screenshots
                               but generalises to Windows controls.
    omniparser-icon-detect-v2  single-class "icon" detector from Microsoft
                               OmniParser. Localises icons without naming them, so
                               pair it with OCR rather than using it alone.

  Both ship as PyTorch checkpoints. The runtime is onnxruntime only, so this
  script converts them to ONNX once and the agent never needs torch.

  Why hf-mirror.com: github.com is reachable from this network but the release
  CDN (objects.githubusercontent.com) is not, so the official Ultralytics assets
  time out. The mirror serves the same weights.

  Usage:
    powershell -ExecutionPolicy Bypass -File tools\computer-broker\fetch-vision-models.ps1
    powershell ... -SkipConvert     # download only; convert later
#>
[CmdletBinding()]
param(
    [string]$ModelDirectory,
    [string]$TorchPython,
    [switch]$SkipConvert
)

$ErrorActionPreference = 'Stop'
$repoRoot = Split-Path -Parent (Split-Path -Parent $PSScriptRoot)
if (-not $ModelDirectory) { $ModelDirectory = Join-Path $repoRoot '.tools\vision-models' }
New-Item -ItemType Directory -Force -Path $ModelDirectory | Out-Null

$models = @(
    @{
        Name = 'ui-elements-detection'
        Url  = 'https://hf-mirror.com/macpaw-research/yolov11l-ui-elements-detection/resolve/main/ui-elements-detection.pt'
    },
    @{
        Name = 'omniparser-icon-detect-v2'
        Url  = 'https://hf-mirror.com/microsoft/OmniParser-v2.0/resolve/main/icon_detect/model.pt'
    }
)

foreach ($model in $models) {
    $pt = Join-Path $ModelDirectory ($model.Name + '.pt')
    $onnx = Join-Path $ModelDirectory ($model.Name + '.onnx')
    if (Test-Path -LiteralPath $onnx) {
        Write-Host ("[skip] {0} already converted" -f $model.Name)
        continue
    }
    if (-not (Test-Path -LiteralPath $pt)) {
        Write-Host ("[..] downloading {0}" -f $model.Name)
        $code = & curl.exe -s -L --max-time 900 -o $pt -w '%{http_code}' $model.Url
        if ($code -ne '200' -or -not (Test-Path -LiteralPath $pt)) {
            throw ("download failed for {0} (http {1})" -f $model.Name, $code)
        }
    }
    $sizeMb = [math]::Round((Get-Item -LiteralPath $pt).Length / 1MB, 1)
    Write-Host ("[ok] {0}.pt ({1} MB)" -f $model.Name, $sizeMb)
}

if ($SkipConvert) {
    Write-Host '[..] skipping ONNX conversion as requested'
    exit 0
}

if (-not $TorchPython) {
    $candidates = @(
        'D:\Anaconda\envs\GPTSoVits\python.exe',
        'D:\Anaconda\python.exe'
    )
    foreach ($candidate in $candidates) {
        if (-not (Test-Path -LiteralPath $candidate)) { continue }
        $probe = & $candidate -c "import importlib.util as u;print(bool(u.find_spec('torch') and u.find_spec('ultralytics')))" 2>&1
        if ("$probe".Trim() -eq 'True') { $TorchPython = $candidate; break }
    }
}
if (-not $TorchPython) {
    throw "No Python with torch+ultralytics was found. Install it with: pip install ultralytics"
}
Write-Host ("[..] converting with {0}" -f $TorchPython)

# Keep Ultralytics settings inside .tools so the repo root stays clean.
$env:YOLO_CONFIG_DIR = Join-Path $repoRoot '.tools\ultralytics'
New-Item -ItemType Directory -Force -Path $env:YOLO_CONFIG_DIR | Out-Null

$convert = @'
import json, os, sys
from ultralytics import YOLO

root = sys.argv[1]
for name in ("ui-elements-detection", "omniparser-icon-detect-v2"):
    pt = os.path.join(root, name + ".pt")
    onnx = os.path.join(root, name + ".onnx")
    if not os.path.exists(pt):
        print("[skip] missing", pt)
        continue
    model = YOLO(pt)
    names = model.names
    print("[..] %s classes(%d): %s" % (name, len(names), names))
    if not os.path.exists(onnx):
        exported = model.export(format="onnx", imgsz=640, opset=12, simplify=False, dynamic=False)
        if os.path.abspath(exported) != os.path.abspath(onnx):
            import shutil
            shutil.copyfile(exported, onnx)
    with open(os.path.join(root, name + ".meta.json"), "w", encoding="utf-8") as handle:
        json.dump({"names": names, "imgsz": 640}, handle, ensure_ascii=False, indent=2)
    print("[ok] %s.onnx %.1f MB" % (name, os.path.getsize(onnx) / 1e6))
'@

$scriptPath = Join-Path $env:TEMP 'fairy-yolo-export.py'
$convert | Set-Content -LiteralPath $scriptPath -Encoding utf8
& $TorchPython $scriptPath $ModelDirectory
if ($LASTEXITCODE -ne 0) { throw "ONNX conversion failed with exit code $LASTEXITCODE" }

Write-Host ''
Write-Host 'Models ready:'
Get-ChildItem -LiteralPath $ModelDirectory -Filter '*.onnx' |
    Select-Object Name, @{ n = 'MB'; e = { [math]::Round($_.Length / 1MB, 1) } } |
    Format-Table -AutoSize

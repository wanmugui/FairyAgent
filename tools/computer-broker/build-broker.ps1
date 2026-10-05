<#
  Compiles the C# Windows computer broker for local development.

  Production does not need this script: the Go tool compiles the same embedded
  source on demand into <repo>\.tools\computer-broker\ and caches it by content
  hash (see agent/internal/biz/tool/local/computer_broker.go). This script only
  exists so computer_broker.cs can be iterated on without starting the agent.
#>
[CmdletBinding()]
param(
    [string]$Source,
    [string]$Output
)

$ErrorActionPreference = 'Stop'

$repoRoot = Split-Path -Parent (Split-Path -Parent $PSScriptRoot)
if (-not $Source) {
    $Source = Join-Path $repoRoot 'agent\internal\biz\tool\local\computer_broker.cs'
}
if (-not $Output) {
    $Output = Join-Path $repoRoot '.tools\computer-broker\FairyComputerBroker.exe'
}
if (-not (Test-Path -LiteralPath $Source)) {
    throw "broker source not found: $Source"
}

$frameworkCandidates = @(
    (Join-Path $env:WINDIR 'Microsoft.NET\Framework64\v4.0.30319'),
    (Join-Path $env:WINDIR 'Microsoft.NET\Framework\v4.0.30319')
)
$framework = $frameworkCandidates | Where-Object { Test-Path -LiteralPath $_ } | Select-Object -First 1
if (-not $framework) {
    throw 'No .NET Framework 4.x directory was found; csc.exe is required.'
}
$csc = Join-Path $framework 'csc.exe'
if (-not (Test-Path -LiteralPath $csc)) {
    throw "csc.exe not found: $csc"
}

$gac = Join-Path $env:WINDIR 'Microsoft.NET\assembly\GAC_MSIL'
$references = @(
    (Join-Path $framework 'System.dll')
    (Join-Path $framework 'System.Core.dll')
    (Join-Path $framework 'System.Drawing.dll')
    (Join-Path $framework 'System.Windows.Forms.dll')
    (Join-Path $framework 'System.Web.Extensions.dll')
)
foreach ($name in @('UIAutomationClient', 'UIAutomationTypes', 'WindowsBase')) {
    $match = Get-ChildItem -Path (Join-Path $gac $name) -Recurse -Filter "$name.dll" -ErrorAction SilentlyContinue |
        Sort-Object FullName |
        Select-Object -Last 1
    if (-not $match) {
        throw "Reference assembly $name.dll was not found under $gac."
    }
    $references += $match.FullName
}

$outputDirectory = Split-Path -Parent $Output
if ($outputDirectory -and -not (Test-Path -LiteralPath $outputDirectory)) {
    New-Item -ItemType Directory -Force -Path $outputDirectory | Out-Null
}

$arguments = @('/nologo', '/target:exe', '/platform:anycpu', '/optimize+', "/out:$Output")
foreach ($reference in $references) {
    $arguments += "/r:$reference"
}
$arguments += $Source

& $csc @arguments
if ($LASTEXITCODE -ne 0) {
    throw "csc.exe failed with exit code $LASTEXITCODE"
}

$info = Get-Item -LiteralPath $Output
Write-Host ("[ok] built {0} ({1:N0} bytes)" -f $info.FullName, $info.Length)

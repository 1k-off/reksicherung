# Daily catalog coverage sweep (agent picks never-checked / oldest).
# Usage:
#   .\scripts\Run-CatalogSweep.ps1
#   .\scripts\Run-CatalogSweep.ps1 -DailyBatch 2 -OldestWindow 10 -Cleanup delete
#   .\scripts\Run-CatalogSweep.ps1 -NoHealthchecks
#
# Task Scheduler (example):
#   Program: powershell.exe
#   Arguments:
#     -NoProfile -ExecutionPolicy Bypass -File "<PACKAGE_PATH>\scripts\Run-CatalogSweep.ps1"
#   Store OPENAI_API_KEY in the task env or User env.
#
# Assumes OpenBao + SQL Server (+ reconciler) are already up (managed separately).
# This script only runs Airon; it does not start/unseal the support stack.
param(
    [int]$DailyBatch = 2,
    [int]$OldestWindow = 10,
    [string]$Cleanup = 'delete',
    [string]$SourceId = '',
    [string]$AironPath = '',
    [switch]$NoHealthchecks
)

$ErrorActionPreference = 'Stop'
$PackageRoot = Split-Path -Parent (Split-Path -Parent $MyInvocation.MyCommand.Path)
$JobDir = Join-Path $PackageRoot 'job'

function Resolve-Airon {
    if ($AironPath) { return $AironPath }
    if ($env:AIRON_BIN) { return $env:AIRON_BIN }
    $cmd = Get-Command airon -ErrorAction SilentlyContinue
    if ($cmd) { return $cmd.Source }
    Write-Error 'airon not found. Set AIRON_BIN or pass -AironPath, or put airon on PATH.'
}

$airon = Resolve-Airon
Write-Host "Using airon: $airon"

$ComposeEnv = Join-Path $PackageRoot 'compose\.env'
if ([string]::IsNullOrWhiteSpace($env:REKSICHERUNG_OPENBAO_TOKEN) -and (Test-Path -LiteralPath $ComposeEnv)) {
    $tokenLine = Get-Content -LiteralPath $ComposeEnv |
        Where-Object { $_ -match '^REKSICHERUNG_OPENBAO_TOKEN=' } |
        Select-Object -First 1
    if ($tokenLine) {
        $env:REKSICHERUNG_OPENBAO_TOKEN = (($tokenLine -split '=', 2)[1]).Trim().Trim('"')
    }
}
if ([string]::IsNullOrWhiteSpace($env:REKSICHERUNG_OPENBAO_TOKEN) -or $env:REKSICHERUNG_OPENBAO_TOKEN -like 'replace*') {
    Write-Error 'REKSICHERUNG_OPENBAO_TOKEN missing — unseal OpenBao / check compose/.env'
}

if ([string]::IsNullOrWhiteSpace($env:OPENAI_API_KEY) -and [string]::IsNullOrWhiteSpace($env:ANTHROPIC_API_KEY)) {
    Write-Error 'Set OPENAI_API_KEY (or ANTHROPIC_API_KEY + provider swap) for the scheduled user'
}

$lines = @(
    "cleanup=$Cleanup",
    "daily_batch=$DailyBatch",
    "oldest_window=$OldestWindow"
)
if ($SourceId) {
    $lines += "source_id=$SourceId"
}
$InputText = $lines -join "`n"

$aironArgs = @(
    'run',
    '--config', (Join-Path $JobDir 'airon-catalog-sweep.yaml'),
    '--input', $InputText
)
if ($NoHealthchecks) { $aironArgs += '--no-healthchecks' }

Push-Location $JobDir
try {
    & $airon @aironArgs
    exit $LASTEXITCODE
} finally {
    Pop-Location
}

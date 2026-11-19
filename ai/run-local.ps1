param([Parameter(ValueFromRemainingArguments = $true)][string[]]$CliArgs)
$ErrorActionPreference = 'Stop'
# Compact CPU profile. Explicit process environment settings take precedence.
$taskDefaults = @{}
$taskProfile = Get-Content -LiteralPath (Join-Path $PSScriptRoot 'local-models.json') -Raw | ConvertFrom-Json
foreach ($taskProperty in $taskProfile.PSObject.Properties) { $taskDefaults[$taskProperty.Name] = [string]$taskProperty.Value }

$taskPrevious = @{}
$taskOldUtf8 = $env:PYTHONUTF8
try {
    foreach ($taskKey in $taskDefaults.Keys) {
        $taskPrevious[$taskKey] = [Environment]::GetEnvironmentVariable($taskKey, 'Process')
        if (-not $taskPrevious[$taskKey]) {
            [Environment]::SetEnvironmentVariable($taskKey, $taskDefaults[$taskKey], 'Process')
        }
    }
    if ($CliArgs.Count -eq 1 -and $CliArgs[0] -in @('verify', 'verify-refusal')) {
        $taskCheck = if ($CliArgs[0] -eq 'verify') { 'verify_local.py' } else { 'verify_refusal.py' }
        $env:PYTHONUTF8 = '1'
        & (Join-Path $PSScriptRoot '.venv\Scripts\python.exe') (Join-Path $PSScriptRoot "scripts\$taskCheck")
    } else {
        if ($CliArgs -notcontains '--db') {
            $CliArgs = @('--db', 'data/local-qwen.sqlite3') + $CliArgs
        }
        & (Join-Path $PSScriptRoot 'run.ps1') @CliArgs
    }
    $taskExit = $LASTEXITCODE
} finally {
    foreach ($taskKey in $taskPrevious.Keys) {
        [Environment]::SetEnvironmentVariable($taskKey, $taskPrevious[$taskKey], 'Process')
    }
    $env:PYTHONUTF8 = $taskOldUtf8
}
exit $taskExit

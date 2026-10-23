param([Parameter(ValueFromRemainingArguments = $true)][string[]]$CliArgs)
$ErrorActionPreference = 'Stop'
$taskPython = Join-Path $PSScriptRoot '.venv\Scripts\python.exe'
if (-not (Test-Path -LiteralPath $taskPython)) {
    throw 'Нет .venv. Сначала выполни .\setup.ps1.'
}
$env:PYTHONUTF8 = '1'
Push-Location -LiteralPath $PSScriptRoot
try {
    & $taskPython -m faq_ai @CliArgs
    $taskExit = $LASTEXITCODE
} finally {
    Pop-Location
}
exit $taskExit


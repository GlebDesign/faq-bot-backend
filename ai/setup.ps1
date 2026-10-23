[CmdletBinding()]
param([string]$Python)
$ErrorActionPreference = 'Stop'
$taskCandidates = @()
if ($Python) {
    $taskCandidates += $Python
} else {
    $taskBundled = Join-Path $env:USERPROFILE '.cache\codex-runtimes\codex-primary-runtime\dependencies\python\python.exe'
    if (Test-Path -LiteralPath $taskBundled) { $taskCandidates += $taskBundled }
    $taskCommand = Get-Command python.exe -ErrorAction SilentlyContinue
    if ($taskCommand -and $taskCommand.Source -notlike '*\Microsoft\WindowsApps\*') {
        $taskCandidates += $taskCommand.Source
    }
}
$taskBasePython = $null
foreach ($taskCandidate in $taskCandidates) {
    try {
        & $taskCandidate -c 'import sys; raise SystemExit(0 if sys.version_info >= (3, 11) else 1)' *> $null
        if ($LASTEXITCODE -eq 0) { $taskBasePython = $taskCandidate; break }
    } catch { }
}
if (-not $taskBasePython) {
    throw 'Нужен Python 3.11+. Укажи путь: .\setup.ps1 -Python C:\Python312\python.exe'
}
$taskOldTemp = $env:TEMP
$taskOldTmp = $env:TMP
$taskOldUtf8 = $env:PYTHONUTF8
Push-Location -LiteralPath $PSScriptRoot
try {
    $taskTemp = Join-Path $PSScriptRoot '.tmp'
    New-Item -ItemType Directory -Path $taskTemp -Force | Out-Null
    $env:TEMP = $taskTemp
    $env:TMP = $taskTemp
    $env:PYTHONUTF8 = '1'
    $taskVenvPython = Join-Path $PSScriptRoot '.venv\Scripts\python.exe'
    if (-not (Test-Path -LiteralPath $taskVenvPython)) {
        & $taskBasePython -m venv (Join-Path $PSScriptRoot '.venv')
        if ($LASTEXITCODE -ne 0) { throw 'Не удалось создать виртуальное окружение.' }
    }
    & $taskVenvPython -m pip install --disable-pip-version-check -r (Join-Path $PSScriptRoot 'requirements.lock.txt')
    if ($LASTEXITCODE -ne 0) { throw 'Не удалось установить закреплённые зависимости.' }
    & $taskVenvPython -m pip install --disable-pip-version-check --no-deps -e $PSScriptRoot
    if ($LASTEXITCODE -ne 0) { throw 'Не удалось установить AI-модуль.' }
    Write-Host 'Окружение готово. Запуск: .\run.ps1 demo'
} finally {
    $env:TEMP = $taskOldTemp
    $env:TMP = $taskOldTmp
    $env:PYTHONUTF8 = $taskOldUtf8
    Pop-Location
}

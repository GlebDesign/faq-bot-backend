param([string]$OllamaPath = (Join-Path $PSScriptRoot '.runtime\ollama\ollama.exe'))
$ErrorActionPreference = 'Stop'
try {
    $taskVersion = Invoke-RestMethod -Uri 'http://127.0.0.1:11434/api/version' -TimeoutSec 2
    Write-Host ('Ollama is already running: ' + $taskVersion.version)
    return
} catch { }
if (-not (Test-Path -LiteralPath $OllamaPath)) {
    throw 'Ollama not found. Supply -OllamaPath with the installed ollama.exe path.'
}
$taskRuntime = Join-Path $PSScriptRoot '.runtime'
New-Item -ItemType Directory -Path (Join-Path $taskRuntime 'models'), (Join-Path $taskRuntime 'logs') -Force | Out-Null
$taskValues = @{
    OLLAMA_HOST = '127.0.0.1:11434'
    OLLAMA_MODELS = (Join-Path $taskRuntime 'models')
    OLLAMA_MAX_LOADED_MODELS = '1'
    OLLAMA_NUM_PARALLEL = '1'
    OLLAMA_MAX_QUEUE = '8'
    OLLAMA_CONTEXT_LENGTH = '4096'
    OLLAMA_NO_CLOUD = '1'
}
$taskPrevious = @{}
try {
    foreach ($taskKey in $taskValues.Keys) {
        $taskPrevious[$taskKey] = [Environment]::GetEnvironmentVariable($taskKey, 'Process')
        [Environment]::SetEnvironmentVariable($taskKey, $taskValues[$taskKey], 'Process')
    }
    $taskServer = Start-Process -FilePath $OllamaPath -ArgumentList 'serve' -WindowStyle Hidden -PassThru `
        -RedirectStandardOutput (Join-Path $taskRuntime 'logs\server.out.log') `
        -RedirectStandardError (Join-Path $taskRuntime 'logs\server.err.log')
    $taskServer.Id | Set-Content -LiteralPath (Join-Path $taskRuntime 'server.pid')
    for ($taskAttempt = 0; $taskAttempt -lt 15; $taskAttempt++) {
        if ($taskServer.HasExited) { throw 'Ollama exited. See .runtime/logs/server.err.log.' }
        try {
            $taskVersion = Invoke-RestMethod -Uri 'http://127.0.0.1:11434/api/version' -TimeoutSec 2
            Write-Host ('Ollama started on localhost:11434, version ' + $taskVersion.version)
            return
        } catch { Start-Sleep -Seconds 1 }
    }
    throw 'Ollama did not become ready. See .runtime/logs/server.err.log.'
} finally {
    foreach ($taskKey in $taskPrevious.Keys) {
        [Environment]::SetEnvironmentVariable($taskKey, $taskPrevious[$taskKey], 'Process')
    }
}

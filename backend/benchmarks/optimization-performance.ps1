param(
    [ValidatePattern('^[a-zA-Z0-9_-]+$')][string]$Label = 'candidate',
    [string]$ArtifactDirectory = '',
    [int]$Count = 3,
    [string]$BenchTime = '100ms',
    [switch]$SkipBuild
)
$ErrorActionPreference = 'Stop'
$taskRepo = Split-Path -Parent (Split-Path -Parent $PSScriptRoot)
if (-not $ArtifactDirectory) {
    $ArtifactDirectory = Join-Path $taskRepo '.git/task-artifacts/performance-20261105'
}
New-Item -ItemType Directory -Force -Path $ArtifactDirectory | Out-Null
$ArtifactDirectory = [IO.Path]::GetFullPath($ArtifactDirectory)
$taskBinary = Join-Path $ArtifactDirectory "$Label-service.test.exe"
$taskOldDirectory = Get-Location
$taskEnv = @{}
foreach ($name in @('GOTOOLCHAIN', 'GOEXPERIMENT', 'GOMAXPROCS', 'SUB2API_PERFORMANCE_PROCESS', 'SUB2API_PERFORMANCE_HEAP')) {
    $taskEnv[$name] = [Environment]::GetEnvironmentVariable($name, 'Process')
}
try {
    Set-Location (Join-Path $taskRepo 'backend')
    $env:GOTOOLCHAIN = 'auto'
    $env:GOEXPERIMENT = 'jsonv2'
    $env:GOMAXPROCS = '4'
    $taskVersion = & go version
    if ($LASTEXITCODE -ne 0) { throw 'go version failed' }
    @($taskVersion, "GOEXPERIMENT=$env:GOEXPERIMENT", "GOMAXPROCS=$env:GOMAXPROCS", "count=$Count", "benchtime=$BenchTime", "host=$([Environment]::OSVersion.VersionString)", "captured_utc=$([DateTime]::UtcNow.ToString('o'))") | Set-Content (Join-Path $ArtifactDirectory "$Label-environment.txt")
    if (-not $SkipBuild) {
        & go test -c ./internal/service -o $taskBinary
        if ($LASTEXITCODE -ne 0) { throw 'service benchmark build failed' }
    }
    & $taskBinary '-test.run=^TestOptimizationIntegrityReference$' '-test.count=1' *>&1 | Set-Content (Join-Path $ArtifactDirectory "$Label-reference.txt")
    if ($LASTEXITCODE -ne 0) { throw 'reference differential failed' }
    $taskGatewayTests = '^(TestOpenAIIntegrityGatewayReportsEncryptedContentRecoveryPerPhysicalAttempt|TestOpenAIIntegrityGatewayFailoverRetainsBaselineAndFrozenSwitch|TestOpenAIIntegrityGatewayToggleDoesNotChangeSentBodyOrResponse|TestForwardOpenAIWSV2_ClientCancellationDrainsWithoutSyntheticFailure|TestOpenAIGatewayService_ProxyResponsesWebSocketFromClient_HTTPBridgeModeRelaysHTTPStream|TestOpenAIGatewayService_Forward_WSv2RetryFiveTimesThenFallbackHTTP)$'
    & $taskBinary "-test.run=$taskGatewayTests" '-test.count=1' '-test.v' '-test.timeout=60s' *>&1 | Set-Content (Join-Path $ArtifactDirectory "$Label-gateway-regression.txt")
    if ($LASTEXITCODE -ne 0) { throw 'mock gateway regression failed' }
    & $taskBinary '-test.run=^$' '-test.bench=^BenchmarkOptimization' '-test.benchmem' "-test.count=$Count" "-test.benchtime=$BenchTime" *>&1 | Set-Content (Join-Path $ArtifactDirectory "$Label-bench.txt")
    if ($LASTEXITCODE -ne 0) { throw 'benchmark execution failed' }
    $env:SUB2API_PERFORMANCE_PROCESS = '1'
    $env:SUB2API_PERFORMANCE_HEAP = Join-Path $ArtifactDirectory "$Label-heap.pprof"
    $taskProcess = Start-Process -FilePath $taskBinary -ArgumentList @('-test.run', '^TestOptimizationMemoryProfile$', '-test.v', '-test.count', '1') -PassThru -WindowStyle Hidden -RedirectStandardOutput (Join-Path $ArtifactDirectory "$Label-memory.txt") -RedirectStandardError (Join-Path $ArtifactDirectory "$Label-memory-stderr.txt")
    $taskPeakWorkingSet = 0L
    $taskPeakPrivateBytes = 0L
    $taskSamples = 0
    while (-not $taskProcess.HasExited) {
        $taskProcess.Refresh()
        $taskPeakWorkingSet = [Math]::Max($taskPeakWorkingSet, $taskProcess.WorkingSet64)
        $taskPeakPrivateBytes = [Math]::Max($taskPeakPrivateBytes, $taskProcess.PrivateMemorySize64)
        $taskSamples++
        Start-Sleep -Milliseconds 10
    }
    $taskProcess.WaitForExit()
    if ($taskProcess.ExitCode -ne 0) { throw 'process memory workload failed' }
    "peak_working_set_sampled=$taskPeakWorkingSet peak_private_bytes_sampled=$taskPeakPrivateBytes samples=$taskSamples" | Set-Content (Join-Path $ArtifactDirectory "$Label-process-memory.txt")
    & go tool pprof -top -sample_index=inuse_space $taskBinary $env:SUB2API_PERFORMANCE_HEAP *>&1 | Set-Content (Join-Path $ArtifactDirectory "$Label-heap-top.txt")
    if ($LASTEXITCODE -ne 0) { throw 'heap profile analysis failed' }
    Write-Output "Performance results: $ArtifactDirectory ($Label)"
} finally {
    Set-Location $taskOldDirectory
    foreach ($name in $taskEnv.Keys) { [Environment]::SetEnvironmentVariable($name, $taskEnv[$name], 'Process') }
}

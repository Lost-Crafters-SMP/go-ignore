# Process working-set sampler for Windows resource tests.
# Compile with: mise exec -- go test -c -o <output-dir>/ignore.test.exe
param([Parameter(Mandatory=$true)][string]$Executable)
$env:IGNORE_RESOURCE = '1'
$root = $PSScriptRoot
$process = Start-Process -FilePath $Executable -ArgumentList '-test.run=^Test(RetainedLiteralMemory|RetainedRuleSets|DefaultCapBoundaries)$', '-test.v' -PassThru -RedirectStandardOutput (Join-Path $root 'rss-test-output.txt') -RedirectStandardError (Join-Path $root 'rss-test-stderr.txt')
$peak = 0L
$samples = 0
while (-not $process.HasExited) {
    $process.Refresh()
    $peak = [Math]::Max($peak, $process.WorkingSet64)
    $samples++
    $null = $process.WaitForExit(2)
}
$process.WaitForExit()
[pscustomobject]@{ Platform = 'Windows amd64'; Metric = 'sampled process working-set bytes (not retained Go heap)'; PeakBytes = $peak; Samples = $samples; RequestedIntervalMilliseconds = 2; ExitCode = $process.ExitCode } | ConvertTo-Json | Set-Content (Join-Path $root 'sampled-rss.json')
if ($process.ExitCode -ne 0) { throw "resource child exited $($process.ExitCode)" }

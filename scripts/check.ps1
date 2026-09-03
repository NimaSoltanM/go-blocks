param([switch]$Race)

$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest

function Invoke-Checked {
    param([string]$Program, [string[]]$Arguments)
    & $Program @Arguments
    if ($LASTEXITCODE -ne 0) {
        throw "$Program $($Arguments -join ' ') failed with exit code $LASTEXITCODE."
    }
}

$repoRoot = Split-Path -Parent $PSScriptRoot
$previousGoWork = [Environment]::GetEnvironmentVariable('GOWORK')
$env:GOWORK = 'off'
Push-Location -LiteralPath $repoRoot
try {
    $goFiles = @(git -c core.quotepath=false ls-files --cached --others --exclude-standard -- '*.go')
    if ($LASTEXITCODE -ne 0) { throw 'Could not list Go source files.' }
    if ($goFiles.Count -eq 0) { throw 'No Go source files found.' }

    $unformatted = @(gofmt -l @goFiles)
    if ($LASTEXITCODE -ne 0) { throw 'gofmt failed.' }
    if ($unformatted.Count -ne 0) {
        throw "Run gofmt -w on these files: $($unformatted -join ', ')"
    }

    $testArgs = @('test', '-count=1')
    if ($Race) { $testArgs += '-race' }
    $testArgs += './...'
    $moduleFiles = @(git -c core.quotepath=false ls-files --cached --others --exclude-standard -- 'go.mod' '*/go.mod')
    if ($LASTEXITCODE -ne 0 -or $moduleFiles.Count -eq 0) { throw 'Could not find Go modules.' }
    foreach ($moduleFile in $moduleFiles) {
        $moduleDir = Split-Path -Parent $moduleFile
        if (-not $moduleDir) { $moduleDir = '.' }
        Push-Location -LiteralPath $moduleDir
        try {
            Write-Output "Checking $moduleFile"
            Invoke-Checked 'go' @('mod', 'tidy', '-diff')
            Invoke-Checked 'go' @('mod', 'verify')
            Invoke-Checked 'go' @('vet', '-tags=integration', './...')
            Invoke-Checked 'go' $testArgs
        }
        finally { Pop-Location }
    }
    Invoke-Checked 'go' @('test', '-tags=integration', '-run=^$', './tests/smoke')
    Write-Output 'Repository checks passed.'
}
finally {
    Pop-Location
    $env:GOWORK = $previousGoWork
}

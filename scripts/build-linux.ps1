[CmdletBinding()]
param(
    [switch]$SkipWeb,
    [ValidatePattern('^[0-9A-Za-z][0-9A-Za-z.+-]{0,63}$')]
    [string]$Version = '0.1.0'
)

$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest

function Invoke-Checked {
    param(
        [Parameter(Mandatory)] [string]$Command,
        [Parameter(ValueFromRemainingArguments)] [string[]]$Arguments
    )
    & $Command @Arguments
    if ($LASTEXITCODE -ne 0) {
        throw "Command failed with exit code ${LASTEXITCODE}: $Command $($Arguments -join ' ')"
    }
}

$repoRoot = Split-Path -Parent $PSScriptRoot
$distDir = Join-Path $repoRoot 'dist'
$oldGoos = $env:GOOS
$oldGoarch = $env:GOARCH
$oldCgo = $env:CGO_ENABLED

Push-Location $repoRoot
try {
    $goVersion = (& go env GOVERSION).Trim()
    if ($LASTEXITCODE -ne 0 -or $goVersion -notmatch '^go1\.26(?:\.|$)') {
        throw "Go 1.26.x is required; found '$goVersion'"
    }

    if (-not $SkipWeb) {
        Invoke-Checked -Command npm -Arguments @('--prefix', 'web', 'ci')
        Invoke-Checked -Command npm -Arguments @('--prefix', 'web', 'run', 'build')
    }
    elseif (-not (Test-Path (Join-Path $repoRoot 'internal/webui/dist/index.html'))) {
        throw '-SkipWeb requires an existing internal/webui/dist/index.html'
    }

    New-Item -ItemType Directory -Force -Path $distDir | Out-Null
    $artifacts = @()
    $ldflags = "-s -w -X main.version=$Version"
    foreach ($arch in @('amd64', 'arm64')) {
        $env:GOOS = 'linux'
        $env:GOARCH = $arch
        $env:CGO_ENABLED = '0'
        $output = Join-Path $distDir "sub2api-limit-portal-linux-$arch"
        Invoke-Checked -Command go -Arguments @('build', '-trimpath', "-ldflags=$ldflags", '-o', $output, './cmd/sub2api-limit-portal')
        $artifacts += Get-Item $output
    }

    $checksumLines = foreach ($artifact in $artifacts) {
        $hash = (Get-FileHash -Algorithm SHA256 -LiteralPath $artifact.FullName).Hash.ToLowerInvariant()
        "$hash  $($artifact.Name)"
    }
    [System.IO.File]::WriteAllLines((Join-Path $distDir 'SHA256SUMS'), $checksumLines)
    Write-Host "Built Linux amd64 and arm64 artifacts in $distDir"
}
finally {
    $env:GOOS = $oldGoos
    $env:GOARCH = $oldGoarch
    $env:CGO_ENABLED = $oldCgo
    Pop-Location
}

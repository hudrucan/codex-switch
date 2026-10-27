param(
    [string]$OutputDirectory,
    [ValidateSet('win-x64', 'win-arm64')][string]$Runtime = 'win-x64'
)
$ErrorActionPreference = 'Stop'
$Repo = Split-Path -Parent $PSScriptRoot
if (-not $OutputDirectory) { $OutputDirectory = Join-Path $Repo "dist\windows-$Runtime" }
$OutputDirectory = [IO.Path]::GetFullPath($OutputDirectory)
if (Test-Path -LiteralPath $OutputDirectory) { throw 'Output directory must be new; existing builds are never overwritten.' }
$Dotnet = Get-Command dotnet -ErrorAction Stop
$Go = Get-Command go -ErrorAction Stop
$Project = Join-Path $PSScriptRoot 'CodexSwitch.Tray\CodexSwitch.Tray.csproj'
& $Dotnet.Source publish $Project -c Release -r $Runtime --self-contained true -o $OutputDirectory
if ($LASTEXITCODE -ne 0) { throw 'Windows UI publish failed.' }
$Helpers = Join-Path $OutputDirectory 'Helpers'
New-Item -ItemType Directory -Path $Helpers -ErrorAction Stop | Out-Null
$PreviousOS = $env:GOOS
$PreviousArchitecture = $env:GOARCH
try {
    $env:GOOS = 'windows'
    $env:GOARCH = if ($Runtime -eq 'win-arm64') { 'arm64' } else { 'amd64' }
    Push-Location $Repo
    try { & $Go.Source build -trimpath -o (Join-Path $Helpers 'codex-switch.exe') ./cmd/codex-switch }
    finally { Pop-Location }
    if ($LASTEXITCODE -ne 0) { throw 'Bundled CLI build failed.' }
} finally {
    $env:GOOS = $PreviousOS
    $env:GOARCH = $PreviousArchitecture
}
Copy-Item -LiteralPath (Join-Path $Repo 'LICENSE') -Destination $OutputDirectory
Copy-Item -LiteralPath (Join-Path $Repo 'THIRD_PARTY_NOTICES.md') -Destination $OutputDirectory
Copy-Item -LiteralPath (Join-Path $PSScriptRoot 'README.md') -Destination (Join-Path $OutputDirectory 'WINDOWS-README.md')
Write-Output "Built: $(Join-Path $OutputDirectory 'CodexSwitch.Tray.exe')"
Write-Output 'No application was launched or installed. Keep the whole output directory together.'

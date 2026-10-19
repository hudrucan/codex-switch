$ErrorActionPreference = "Stop"
# Use the verifier from the same reviewed checkout. No remote script execution.
$Verifier = Join-Path $PSScriptRoot "install.py"
$Python = Get-Command python3 -ErrorAction SilentlyContinue
if ($Python) {
    & $Python.Source $Verifier
} else {
    $Launcher = Get-Command py -ErrorAction SilentlyContinue
    if (-not $Launcher) { throw "Python 3.9+ is required for verified installation" }
    & $Launcher.Source -3 $Verifier
}
if ($LASTEXITCODE -ne 0) { throw "Verified installation failed" }

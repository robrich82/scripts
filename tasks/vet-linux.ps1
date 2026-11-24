$ErrorActionPreference = 'Stop'; $PSNativeCommandUseErrorActionPreference = $true; if ($PSVersionTable.PSVersion -lt '7.4') { throw 'needs PowerShell 7.4+' }
$env:GOOS = 'linux'
$mods = if ($env:usage_module) { -split $env:usage_module } else { (Get-ChildItem -Directory | Where-Object { $_.Name -notlike '.*' -and (Test-Path -LiteralPath "$_/go.mod") }).Name }
if (-not $mods) { throw 'no modules found' }
foreach ($m in $mods) {
  $m = $m.Trim(".\/'".ToCharArray()); if (-not $m) { throw 'empty module name' }; "# $m"
  Push-Location -LiteralPath $m; try { go vet ./...; golangci-lint run ./... } finally { Pop-Location }
}

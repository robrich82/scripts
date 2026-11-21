$ErrorActionPreference = 'Stop'; $PSNativeCommandUseErrorActionPreference = $true; if ($PSVersionTable.PSVersion -lt '7.4') { throw 'needs PowerShell 7.4+' }
if (Test-Path dist) { Remove-Item -Recurse -Force dist }

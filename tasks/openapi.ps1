$ErrorActionPreference = 'Stop'; $PSNativeCommandUseErrorActionPreference = $true; if ($PSVersionTable.PSVersion -lt '7.4') { throw 'needs PowerShell 7.4+' }
# Plex has no download URL for the spec; it's embedded in the Redoc page as __redoc_state.
# jq -e fails on no match, so a page layout change errors instead of emptying openapi.json.
$ProgressPreference = 'SilentlyContinue'
$page = (Invoke-WebRequest -UseBasicParsing https://developer.plex.tv/pms/).Content
$state = [regex]::Match($page, '(?m)^\s*const __redoc_state = (.*);\s*$').Groups[1].Value
try { $state | jq -en 'input | .spec.data | select(.openapi and (.paths | length) > 0)' > openapi.json.tmp }
catch { Remove-Item -ErrorAction Ignore openapi.json.tmp; throw "openapi: no spec found on developer.plex.tv/pms/ ($_)" }
Move-Item -Force openapi.json.tmp openapi.json

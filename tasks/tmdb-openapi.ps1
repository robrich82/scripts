$ErrorActionPreference = 'Stop'; $PSNativeCommandUseErrorActionPreference = $true; if ($PSVersionTable.PSVersion -lt '7.4') { throw 'needs PowerShell 7.4+' }
$ProgressPreference = 'SilentlyContinue'
$url = 'https://developer.themoviedb.org/openapi/tmdb-api.json'
$raw = New-TemporaryFile
$out = Join-Path $PWD "tmdb-openapi.json.$PID.tmp" # per process, so parallel runs don't share it
try {
  Invoke-WebRequest -UseBasicParsing $url -OutFile $raw
  jq -e 'del(.. | .example?, .examples?) | select(.openapi and (.paths | length) > 0)' $raw > $out
} catch { Remove-Item -ErrorAction Ignore $out; throw "tmdb-openapi: no spec at $url ($_)" }
finally { Remove-Item -ErrorAction Ignore $raw }
# One-step replace, so a failed move keeps the old spec.
[IO.File]::Move($out, (Join-Path $PWD tmdb-openapi.json), $true)

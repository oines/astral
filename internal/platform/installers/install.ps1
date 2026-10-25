param(
  [string]$Server,
  [string]$ServerPin,
  [string]$Enrollment,
  [string]$Token,
  [string]$Root,
  [switch]$Container,
  [switch]$Update,
	[switch]$Foreground,
  [string]$ContainerID
)
$ErrorActionPreference = 'Stop'
# Executables always come from the official HTTPS release. Redirects are
# followed explicitly so a release download cannot downgrade to plain HTTP.
Add-Type -AssemblyName System.Net.Http
function Get-OfficialAsset([string]$Name, [string]$Target) {
  $uri = [Uri]("https://github.com/oines/astral/releases/latest/download/" + $Name)
  $handler = [Net.Http.HttpClientHandler]::new()
  $handler.AllowAutoRedirect = $false
  $client = [Net.Http.HttpClient]::new($handler)
  $client.Timeout = [TimeSpan]::FromMinutes(5)
  try {
    for ($redirect = 0; $redirect -lt 8; $redirect++) {
      if ($uri.Scheme -ne 'https' -or $uri.Host -notin @('github.com', 'release-assets.githubusercontent.com', 'objects.githubusercontent.com')) { throw 'Official release redirect is not trusted.' }
      $response = $client.GetAsync($uri, [Net.Http.HttpCompletionOption]::ResponseHeadersRead).GetAwaiter().GetResult()
      try {
        if ([int]$response.StatusCode -in @(301,302,303,307,308)) {
          if (-not $response.Headers.Location) { throw 'Official release redirect has no target.' }
          $uri = [Uri]::new($uri, $response.Headers.Location)
          continue
        }
        $response.EnsureSuccessStatusCode() | Out-Null
        $assetStream = $response.Content.ReadAsStreamAsync().GetAwaiter().GetResult()
        $destinationStream = [IO.File]::Create($Target)
        try { $assetStream.CopyTo($destinationStream) } finally { $destinationStream.Dispose(); $assetStream.Dispose() }
        return
      } finally { $response.Dispose() }
    }
    throw 'Too many official release redirects.'
  } finally { $client.Dispose(); $handler.Dispose() }
}
[Net.ServicePointManager]::SecurityProtocol = [Net.ServicePointManager]::SecurityProtocol -bor [Net.SecurityProtocolType]::Tls12
if (-not $Update) {
  $serverUri = [Uri]$Server
  if (-not $serverUri.IsAbsoluteUri -or -not $serverUri.Host -or $serverUri.Scheme -notin @('http', 'https') -or $serverUri.UserInfo -or $serverUri.Query -or $serverUri.Fragment) { throw 'Astral Server URL must use HTTP or HTTPS without credentials, a query or a fragment.' }
  if ($ServerPin -notmatch '^[a-fA-F0-9]{64}$') { throw 'A valid Server public-key pin is required; copy a new pairing command.' }
  $Server = $Server.TrimEnd('/')
  try {
    Invoke-WebRequest -UseBasicParsing -MaximumRedirection 0 -TimeoutSec 20 "$Server/health" | Out-Null
  } catch {
    throw "Cannot reach Astral at $Server. Check the Server address, firewall, and Tailscale/LAN connection, then copy the command again. $($_.Exception.Message)"
  }
} elseif ($Server -or $ServerPin -or $Enrollment -or $Token) {
  throw 'Updates use the existing Computer credentials; Server and enrollment arguments are not accepted.'
}
$cpu = [Environment]::GetEnvironmentVariable('PROCESSOR_ARCHITEW6432')
if (-not $cpu) { $cpu = [Environment]::GetEnvironmentVariable('PROCESSOR_ARCHITECTURE') }
switch ($cpu.ToUpperInvariant()) { 'AMD64' { $arch = 'amd64' }; 'ARM64' { $arch = 'arm64' }; default { throw "Unsupported CPU architecture: $cpu" } }
$localData = $env:LOCALAPPDATA
if (-not $localData) { $localData = [Environment]::GetFolderPath('LocalApplicationData') }
$installBase = Join-Path $localData 'Astral\bin'
$temporary = Join-Path $installBase ([Guid]::NewGuid().ToString('N'))
[IO.Directory]::CreateDirectory($temporary) | Out-Null
try {
  $name = "astral-windows-$arch.exe"
  $download = Join-Path $temporary 'astral.exe'
  Get-OfficialAsset $name $download
  $manifestPath = Join-Path $temporary 'checksums.txt'
  Get-OfficialAsset 'checksums.txt' $manifestPath
  $manifest = [IO.File]::ReadAllText($manifestPath)
  $matches = @($manifest -split "`n" | Where-Object { $_ -match ('^([a-fA-F0-9]{64})\s+' + [Regex]::Escape($name) + '\s*$') })
  if ($matches.Count -ne 1) { throw 'Astral distribution has no unique checksum.' }
  $expected = ($matches[0] -split '\s+')[0].ToLowerInvariant()
  $actual = (Get-FileHash -LiteralPath $download -Algorithm SHA256).Hash.ToLowerInvariant()
  if ($actual -ne $expected) { throw 'Astral checksum did not match; the installed version is unchanged.' }
  $release = Join-Path $installBase $actual
  [IO.Directory]::CreateDirectory($release) | Out-Null
  $executable = Join-Path $release 'astral.exe'
  if ((Test-Path -LiteralPath $executable) -and (Get-FileHash -LiteralPath $executable -Algorithm SHA256).Hash.ToLowerInvariant() -ne $actual) { throw 'An installed release is damaged. Remove that version directory and rerun this command.' }
  if (-not (Test-Path -LiteralPath $executable)) { Move-Item -LiteralPath $download -Destination $executable }
  $arguments = @('computer')
  if ($Update) { $arguments += @('update', '--update') }
  else { $arguments += @('connect', '--server', $Server, '--server-pin', $ServerPin, '--enrollment', $Enrollment, '--token', $Token) }
  if ($Root) { $arguments += @('--root', $Root) }
  if ($Foreground) { $arguments += '--foreground' }
  if ($ContainerID) { $arguments += @('--container', $ContainerID) }
  elseif ($Container) { $arguments += '--container' }
  & $executable @arguments
  if ($LASTEXITCODE -ne 0) { throw "Astral exited with code $LASTEXITCODE. See the diagnostic above." }
} finally {
  Remove-Item -LiteralPath $temporary -Recurse -Force -ErrorAction SilentlyContinue
}

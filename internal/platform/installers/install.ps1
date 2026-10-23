param(
  [Parameter(Mandatory=$true)][string]$Server,
  [string]$Enrollment,
  [string]$Token,
  [string]$Root,
  [switch]$Container,
  [switch]$Update,
	[switch]$Foreground,
  [string]$ContainerID
)
$ErrorActionPreference = 'Stop'
[Net.ServicePointManager]::SecurityProtocol = [Net.ServicePointManager]::SecurityProtocol -bor [Net.SecurityProtocolType]::Tls12
$serverUri = [Uri]$Server
if (-not $serverUri.IsAbsoluteUri -or ($serverUri.Scheme -ne 'https' -and -not ($serverUri.Scheme -eq 'http' -and $serverUri.IsLoopback))) { throw 'Remote Astral access requires HTTPS.' }
$Server = $Server.TrimEnd('/')
$cpu = [Environment]::GetEnvironmentVariable('PROCESSOR_ARCHITEW6432')
if (-not $cpu) { $cpu = [Environment]::GetEnvironmentVariable('PROCESSOR_ARCHITECTURE') }
switch ($cpu.ToUpperInvariant()) { 'AMD64' { $arch = 'amd64' }; 'ARM64' { $arch = 'arm64' }; default { throw "Unsupported CPU architecture: $cpu" } }
$installBase = Join-Path ([Environment]::GetFolderPath('LocalApplicationData')) 'Astral\bin'
$temporary = Join-Path $installBase ([Guid]::NewGuid().ToString('N'))
[IO.Directory]::CreateDirectory($temporary) | Out-Null
try {
  $name = "astral-windows-$arch.exe"
  $download = Join-Path $temporary 'astral.exe'
  Invoke-WebRequest -UseBasicParsing -MaximumRedirection 0 "$Server/downloads/$name" -OutFile $download
  $manifest = (Invoke-WebRequest -UseBasicParsing -MaximumRedirection 0 "$Server/downloads/checksums.txt").Content
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
  if ($Update) { $arguments += @('update', '--server', $Server, '--update') }
  else { $arguments += @('connect', '--server', $Server, '--enrollment', $Enrollment, '--token', $Token) }
  if ($Root) { $arguments += @('--root', $Root) }
  if ($Foreground) { $arguments += '--foreground' }
  if ($ContainerID) { $arguments += @('--container', $ContainerID) }
  elseif ($Container) { $arguments += '--container' }
  & $executable @arguments
  if ($LASTEXITCODE -ne 0) { throw "Astral exited with code $LASTEXITCODE. See the diagnostic above." }
} finally {
  Remove-Item -LiteralPath $temporary -Recurse -Force -ErrorAction SilentlyContinue
}

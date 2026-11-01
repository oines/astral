param(
  [string]$Server,
  [string]$ServerPin,
  [string]$Enrollment,
  [string]$Token,
  [string]$Root,
  [switch]$Container,
  [switch]$Update,
	[switch]$Foreground,
  [string]$ContainerID,
  [Alias('github-proxy', '-github-proxy')]
  [AllowEmptyString()]
  [string]$GitHubProxy
)
$ErrorActionPreference = 'Stop'
if (-not $PSBoundParameters.ContainsKey('GitHubProxy')) { $GitHubProxy = $env:ASTRAL_GITHUB_PROXY }
$script:GitHubProxyUri = $null
if ($GitHubProxy) {
  $proxy = $null
  if ($GitHubProxy -match '[\s\\?#]' -or -not [Uri]::TryCreate($GitHubProxy, [UriKind]::Absolute, [ref]$proxy) -or $proxy.Scheme -ne 'https' -or -not $proxy.Host -or $proxy.UserInfo -or $proxy.Port -lt 1 -or $proxy.Port -gt 65535) {
    throw 'GitHub proxy must be an HTTPS prefix without credentials, a query, a fragment or whitespace.'
  }
  $GitHubProxy = $proxy.AbsoluteUri.TrimEnd('/') + '/'
  $script:GitHubProxyUri = [Uri]$GitHubProxy
  Write-Host "GitHub downloads use the selected proxy: $GitHubProxy"
}
# Executables always come from the official HTTPS release. Redirects are
# followed explicitly so a release download cannot downgrade to plain HTTP.
Add-Type -AssemblyName System.Net.Http
function Get-FileSHA256([string]$Path) {
  # Hashing must also work when PowerShell is launched from another shell with
  # a PSModulePath that cannot discover Microsoft.PowerShell.Utility.
  $stream = [IO.File]::OpenRead($Path)
  $sha256 = [Security.Cryptography.SHA256]::Create()
  try {
    return [BitConverter]::ToString($sha256.ComputeHash($stream)).Replace('-', '').ToLowerInvariant()
  } finally { $sha256.Dispose(); $stream.Dispose() }
}
function Get-OfficialAssetUri([string]$Name) {
  if ($Name -notmatch '^[A-Za-z0-9._-]+$') { throw 'Invalid official release asset name.' }
  $official = "https://github.com/oines/astral/releases/latest/download/" + $Name
  return [Uri]($GitHubProxy + $official)
}
function Assert-OfficialDownloadUri([Uri]$Uri) {
  if ($Uri.Scheme -ne 'https' -or $Uri.UserInfo) { throw 'Official release redirect is not trusted.' }
  if ($script:GitHubProxyUri) {
    if ($Uri.Authority -ne $script:GitHubProxyUri.Authority -or -not $Uri.AbsolutePath.StartsWith($script:GitHubProxyUri.AbsolutePath, [StringComparison]::Ordinal)) { throw 'Official release redirect left the selected GitHub proxy.' }
  } elseif ($Uri.Host -notin @('github.com', 'release-assets.githubusercontent.com', 'objects.githubusercontent.com')) {
    throw 'Official release redirect is not trusted.'
  }
}
function Get-OfficialAsset([string]$Name, [string]$Target) {
  $uri = Get-OfficialAssetUri $Name
  $handler = [Net.Http.HttpClientHandler]::new()
  $handler.AllowAutoRedirect = $false
  $client = [Net.Http.HttpClient]::new($handler)
  $client.Timeout = [TimeSpan]::FromMinutes(5)
  try {
    for ($redirect = 0; $redirect -lt 8; $redirect++) {
      Assert-OfficialDownloadUri $uri
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
if (-not $localData) {
  $localData = [Environment]::GetFolderPath('LocalApplicationData')
  $env:LOCALAPPDATA = $localData
}
$installBase = Join-Path $localData 'Astral\bin'
$current = Join-Path $installBase 'current'
$commandPath = $null
$addCommandDirectory = $false
$commandBody = "@echo off`r`nrem Astral managed Computer command`r`n`"%LOCALAPPDATA%\Astral\bin\current\astral.exe`" %*`r`nexit /b %errorlevel%`r`n"
function Test-CommandDirectory([string]$Directory) {
  if (-not [IO.Path]::IsPathRooted($Directory) -or -not [IO.Directory]::Exists($Directory)) { return $false }
  $probe = Join-Path $Directory ('.astral-command-' + [Guid]::NewGuid().ToString('N'))
  try {
    $stream = [IO.File]::Open($probe, [IO.FileMode]::CreateNew, [IO.FileAccess]::Write, [IO.FileShare]::None)
    $stream.Dispose()
    return $true
  } catch { return $false }
  finally { if ([IO.File]::Exists($probe)) { [IO.File]::Delete($probe) } }
}
function Test-ManagedCommand([string]$Path) {
  return [IO.File]::Exists($Path) -and [IO.File]::ReadAllText($Path) -eq $commandBody
}
function Get-LongInstallerPath([string]$Path) {
  # Junction targets may use long names even when LOCALAPPDATA contains an 8.3
  # alias. Expand name spelling only; do not resolve other junctions and loosen
  # the requirement that the target is a direct SHA256 child of installBase.
  if (-not ('AstralInstaller.NativePath' -as [type])) {
    Add-Type -TypeDefinition @'
using System.ComponentModel;
using System.Runtime.InteropServices;
using System.Text;
namespace AstralInstaller {
  public static class NativePath {
    [DllImport("kernel32.dll", EntryPoint = "GetLongPathNameW", CharSet = CharSet.Unicode, ExactSpelling = true, SetLastError = true)]
    private static extern uint GetLongPathName(string path, StringBuilder buffer, uint capacity);
    public static string Expand(string path) {
      StringBuilder buffer = new StringBuilder(260);
      while (true) {
        uint length = GetLongPathName(path, buffer, (uint)buffer.Capacity);
        if (length == 0) throw new Win32Exception(Marshal.GetLastWin32Error(), "Cannot verify the existing Computer installation path.");
        if (length < buffer.Capacity) return buffer.ToString();
        buffer = new StringBuilder(checked((int)length));
      }
    }
  }
}
'@
  }
  return [AstralInstaller.NativePath]::Expand([IO.Path]::GetFullPath($Path)).TrimEnd('\')
}
if (Test-Path -LiteralPath $current) {
  $link = Get-Item -LiteralPath $current -Force
  $targets = @($link.Target)
  if ($link.LinkType -ne 'Junction' -or $targets.Count -ne 1) { throw 'The Computer current executable must be an installer-managed junction.' }
  $longInstallBase = Get-LongInstallerPath $installBase
  $longTarget = Get-LongInstallerPath $targets[0]
  if ($longTarget -notmatch ('^' + [Regex]::Escape($longInstallBase + '\') + '[a-f0-9]{64}$')) { throw 'The Computer current executable must be an installer-managed junction.' }
}
$existingCommand = Get-Command astral -ErrorAction SilentlyContinue
if ($existingCommand) {
  if ($existingCommand.CommandType -ne 'Application' -or -not (Test-ManagedCommand $existingCommand.Source)) { throw "An unrelated astral command already exists: $($existingCommand.Name). It was not replaced." }
  $commandPath = $existingCommand.Source
} else {
  foreach ($directory in ($env:PATH -split ';')) {
    $directory = $directory.Trim('"')
    if (Test-CommandDirectory $directory) { $commandPath = Join-Path $directory 'astral.cmd'; break }
  }
  if (-not $commandPath) { $commandPath = Join-Path $installBase 'astral.cmd'; $addCommandDirectory = $true }
  if ((Test-Path -LiteralPath $commandPath) -and -not (Test-ManagedCommand $commandPath)) { throw "Refusing to replace an unrelated command: $commandPath" }
}
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
  $actual = Get-FileSHA256 $download
  if ($actual -ne $expected) { throw 'Astral checksum did not match; the installed version is unchanged.' }
  $release = Join-Path $installBase $actual
  [IO.Directory]::CreateDirectory($release) | Out-Null
  $executable = Join-Path $release 'astral.exe'
  if ((Test-Path -LiteralPath $executable) -and (Get-FileSHA256 $executable) -ne $actual) { throw 'An installed release is damaged. Remove that version directory and rerun this command.' }
  if (-not (Test-Path -LiteralPath $executable)) { Move-Item -LiteralPath $download -Destination $executable }
  $arguments = @('computer')
  if ($Update) { $arguments += @('update', '--update') }
  else { $arguments += @('connect', '--server', $Server, '--server-pin', $ServerPin, '--enrollment', $Enrollment, '--token', $Token) }
  if ($Root) { $arguments += @('--root', $Root) }
  if ($Foreground) { $arguments += '--foreground' }
  if ($ContainerID) { $arguments += @('--container', $ContainerID) }
  elseif ($Container) { $arguments += '--container' }
  # A junction keeps the command stable without replacing an executable that
  # another terminal may still be running. The service uses its versioned path.
  $pendingCurrent = Join-Path $installBase ('.current-' + [Guid]::NewGuid().ToString('N'))
  $previousCurrent = Join-Path $installBase ('.previous-' + [Guid]::NewGuid().ToString('N'))
  $hadCurrent = Test-Path -LiteralPath $current
  $createdCommand = $false
  $currentActivated = $false
  $pathUpdated = $false
  $originalProcessPath = $env:PATH
  $originalUserPath = [Environment]::GetEnvironmentVariable('Path', 'User')
  try {
    New-Item -ItemType Junction -Path $pendingCurrent -Target $release | Out-Null
    if ($hadCurrent) { [IO.Directory]::Move($current, $previousCurrent) }
    [IO.Directory]::Move($pendingCurrent, $current)
    $currentActivated = $true
    if (-not (Test-Path -LiteralPath $commandPath)) {
      $stream = [IO.File]::Open($commandPath, [IO.FileMode]::CreateNew, [IO.FileAccess]::Write, [IO.FileShare]::None)
      $createdCommand = $true
      try {
        $bytes = [Text.Encoding]::ASCII.GetBytes($commandBody)
        $stream.Write($bytes, 0, $bytes.Length)
      } finally { $stream.Dispose() }
    }
    if ($addCommandDirectory) {
      $entries = @($originalUserPath -split ';' | Where-Object { $_ })
      $pathUpdated = $true
      if ($entries -notcontains $installBase) { [Environment]::SetEnvironmentVariable('Path', (($entries + $installBase) -join ';'), 'User') }
      $env:PATH = $installBase + ';' + $env:PATH
    }
    $selected = Get-Command astral -ErrorAction Stop
    if ($selected.Source -ne $commandPath) { throw 'The installed astral command is not selected by the current PATH.' }
    $expectedVersion = (& $executable version | Out-String).Trim()
    if ($LASTEXITCODE -ne 0 -or -not $expectedVersion) { throw 'The downloaded astral command could not report its version.' }
    $actualVersion = (& astral version | Out-String).Trim()
    if ($LASTEXITCODE -ne 0 -or $actualVersion -ne $expectedVersion) { throw 'The installed astral command could not run from the current PATH.' }
    # Register and verify the command before consuming a one-time enrollment
    # token. Any connection/update failure restores the previous command.
    $previousProxy = $env:ASTRAL_GITHUB_PROXY
    try {
      $env:ASTRAL_GITHUB_PROXY = $GitHubProxy
      & $executable @arguments
      if ($LASTEXITCODE -ne 0) { throw "Astral exited with code $LASTEXITCODE. See the diagnostic above." }
    } finally {
      $env:ASTRAL_GITHUB_PROXY = $previousProxy
    }
    if ($hadCurrent) { [IO.Directory]::Delete($previousCurrent) }
  } catch {
    if ($currentActivated -and (Test-Path -LiteralPath $current)) { [IO.Directory]::Delete($current) }
    if (Test-Path -LiteralPath $previousCurrent) { [IO.Directory]::Move($previousCurrent, $current) }
    if ($createdCommand) { [IO.File]::Delete($commandPath) }
    if ($pathUpdated) {
      [Environment]::SetEnvironmentVariable('Path', $originalUserPath, 'User')
      $env:PATH = $originalProcessPath
    }
    throw
  } finally {
    if (Test-Path -LiteralPath $pendingCurrent) { [IO.Directory]::Delete($pendingCurrent) }
  }
} finally {
  Remove-Item -LiteralPath $temporary -Recurse -Force -ErrorAction SilentlyContinue
}

import assert from 'node:assert/strict'
import { execFile } from 'node:child_process'
import path from 'node:path'
import { promisify } from 'node:util'

const execute = promisify(execFile)
const samePath = (a, b) => path.win32.normalize(a).toLowerCase() === path.win32.normalize(b).toLowerCase()
function validPID(pid) { assert.ok(Number.isSafeInteger(pid) && pid > 0 && pid <= 0xffffffff, 'Invalid Windows process ID') }
function identity(value) {
  assert.ok(value && typeof value === 'object' && !Array.isArray(value), 'Missing Windows process identity')
  validPID(value.pid)
  assert.ok(typeof value.birth === 'string' && /^[1-9][0-9]{0,19}$/.test(value.birth), 'Missing Windows process creation identity')
  assert.ok(typeof value.exe === 'string' && path.win32.isAbsolute(value.exe) && !/[\0\r\n]/.test(value.exe), 'Missing Windows process executable identity')
  assert.ok(Number.isSafeInteger(value.parent) && value.parent >= 0, 'Missing Windows process parent identity')
  return value
}

export function sameWindowsProcess(expected, observed) {
  identity(expected)
  if (observed === null) return false
  identity(observed)
  return expected.pid === observed.pid && expected.birth === observed.birth
}

export function assertWindowsProcessOwner(value, executable, parent) {
  identity(value)
  assert.ok(samePath(value.exe, executable), 'Windows process executable is outside this Desktop installation')
  if (parent !== undefined) assert.equal(value.parent, parent, 'Windows Server is not a child of the recorded Desktop App')
  return value
}

// StartTime from the held .NET Process handle is the authoritative birth value
// for both capture and cleanup. CIM supplies ownership metadata; its creation
// date is used only to reject stale tree-snapshot rows, at CIM's microsecond
// precision. No PID-only termination command is ever issued.
function script(request) {
  const encoded = Buffer.from(JSON.stringify(request)).toString('base64')
  return `$ErrorActionPreference = 'Stop'
[Console]::OutputEncoding = [Text.UTF8Encoding]::new()
$astralRequest = [Text.Encoding]::UTF8.GetString([Convert]::FromBase64String('${encoded}')) | ConvertFrom-Json
function Open-AstralProcess([int]$astralId) {
  try { $astralProcess = [Diagnostics.Process]::GetProcessById($astralId) }
  catch {
    $astralException = $_.Exception
    while ($null -ne $astralException.InnerException) { $astralException = $astralException.InnerException }
    if ($astralException -is [ArgumentException]) { return $null }
    throw
  }
  try {
    $null = $astralProcess.Handle
    if ($astralProcess.HasExited) { $astralProcess.Dispose(); return $null }
    return $astralProcess
  } catch { $astralProcess.Dispose(); throw }
}
function Read-AstralIdentity($astralProcess, $astralRecord) {
  try {
  if ($null -eq $astralRecord -or $null -eq $astralRecord.CreationDate -or $null -eq $astralRecord.ParentProcessId -or [string]::IsNullOrWhiteSpace($astralRecord.ExecutablePath)) { throw 'Windows process ownership metadata is unavailable.' }
  $astralBirth = $astralProcess.StartTime.ToUniversalTime().Ticks
  $astralCimBirth = $astralRecord.CreationDate.ToUniversalTime().Ticks
  if ([Math]::Abs([long]($astralBirth - $astralCimBirth)) -ge 10) { throw 'Windows process changed during ownership discovery.' }
  $astralExe = $astralProcess.MainModule.FileName
  if (-not [String]::Equals($astralExe, $astralRecord.ExecutablePath, [StringComparison]::OrdinalIgnoreCase)) { throw 'Windows process executable changed during ownership discovery.' }
  return [pscustomobject]@{ pid = [int]$astralProcess.Id; birth = $astralBirth.ToString([Globalization.CultureInfo]::InvariantCulture); exe = $astralExe; parent = [int]$astralRecord.ParentProcessId }
  } catch { if ($astralProcess.HasExited) { return $null }; throw }
}
function Stop-AstralHeldProcess($astralProcess) {
  try { if (-not $astralProcess.HasExited) { $astralProcess.Kill() } }
  catch { if (-not $astralProcess.HasExited) { throw } }
}
$astralRoot = Open-AstralProcess ([int]$astralRequest.pid)
if ($null -eq $astralRoot) {
  if ($astralRequest.action -eq 'stop') { [pscustomobject]@{ stopped = $false; current = $null } | ConvertTo-Json -Compress } else { 'null' }
  exit 0
}
$astralHeld = [Collections.Generic.List[Diagnostics.Process]]::new()
$astralHeld.Add($astralRoot)
try {
  if ($astralRequest.action -eq 'stop') {
    $astralBirth = $astralRoot.StartTime.ToUniversalTime().Ticks.ToString([Globalization.CultureInfo]::InvariantCulture)
    if ($astralBirth -ne $astralRequest.expected.birth) {
      [pscustomobject]@{ stopped = $false; current = [pscustomobject]@{ pid = $astralRoot.Id; birth = $astralBirth; exe = $astralRoot.MainModule.FileName; parent = 0 } } | ConvertTo-Json -Depth 3 -Compress
      exit 0
    }
    $astralRows = @(Get-CimInstance Win32_Process -Property ProcessId,ParentProcessId,CreationDate,ExecutablePath)
  } else {
    $astralRows = @(Get-CimInstance Win32_Process -Filter ('ProcessId = ' + $astralRequest.pid) -Property ProcessId,ParentProcessId,CreationDate,ExecutablePath)
  }
  if ($astralRoot.HasExited) {
    if ($astralRequest.action -eq 'stop') { [pscustomobject]@{ stopped = $false; current = $null } | ConvertTo-Json -Compress } else { 'null' }
    exit 0
  }
  $astralRecord = @($astralRows | Where-Object { $_.ProcessId -eq $astralRequest.pid })
  if ($astralRecord.Count -ne 1) { throw 'Windows process ownership is ambiguous.' }
  $astralCurrent = Read-AstralIdentity $astralRoot $astralRecord[0]
  if ($null -eq $astralCurrent) {
    if ($astralRequest.action -eq 'stop') { [pscustomobject]@{ stopped = $false; current = $null } | ConvertTo-Json -Compress } else { 'null' }
    exit 0
  }
  if ($astralRequest.action -ne 'stop') { $astralCurrent | ConvertTo-Json -Compress; exit 0 }
  if (-not [String]::Equals($astralCurrent.exe, $astralRequest.expected.exe, [StringComparison]::OrdinalIgnoreCase)) { throw 'The recorded Windows process executable no longer matches.' }
  $astralTree = @{}
  $astralTree[[int]$astralRoot.Id] = $astralRoot
  for ($astralPass = 0; $astralPass -lt 8; $astralPass++) {
    foreach ($astralRow in $astralRows) {
      if ($astralTree.ContainsKey([int]$astralRow.ProcessId) -or -not $astralTree.ContainsKey([int]$astralRow.ParentProcessId)) { continue }
      $astralParent = $astralTree[[int]$astralRow.ParentProcessId]
      if ($astralParent.HasExited) { continue }
      $astralChild = Open-AstralProcess ([int]$astralRow.ProcessId)
      if ($null -eq $astralChild) { continue }
      $astralHeld.Add($astralChild)
      if ($astralChild.HasExited) { continue }
      $astralChildIdentity = Read-AstralIdentity $astralChild $astralRow
      if ($null -eq $astralChildIdentity) { continue }
      if ($astralParent.HasExited -or [long]$astralChildIdentity.birth -lt $astralParent.StartTime.ToUniversalTime().Ticks) { continue }
      $astralTree[[int]$astralChild.Id] = $astralChild
    }
  }
  # All targets are held Process objects. A recycled numeric PID cannot retarget
  # Kill(), and stopping the root first prevents it from starting more children.
  Stop-AstralHeldProcess $astralRoot
  foreach ($astralChild in $astralTree.Values) {
    if ($astralChild.Id -ne $astralRoot.Id) { Stop-AstralHeldProcess $astralChild }
  }
  [pscustomobject]@{ stopped = $true; current = $astralCurrent } | ConvertTo-Json -Depth 3 -Compress
} finally { foreach ($astralProcess in $astralHeld) { $astralProcess.Dispose() } }
`
}

async function run(request, command) {
  let stdout
  try {
    ;({ stdout } = await command('powershell.exe', ['-NoLogo', '-NoProfile', '-NonInteractive', '-EncodedCommand', Buffer.from(script(request), 'utf16le').toString('base64')], { encoding: 'utf8', windowsHide: true, timeout: request.action === 'stop' ? 30_000 : 10_000, maxBuffer: 64 << 10 }))
  } catch (error) {
    const codes = ['ENOENT', 'EACCES', 'EPERM', 'ETIMEDOUT', 'ENOBUFS', 'ERR_CHILD_PROCESS_STDIO_MAXBUFFER']
    const code = Number.isSafeInteger(error?.code) && error.code >= 0 && error.code <= 0xffffffff || codes.includes(error?.code) ? error.code : 'unknown'
    const boundedOutput = value => typeof value === 'string' ? value.slice(-2000) : ''
    const diagnostic = { action: request.action, pid: request.pid, exit_code: code, stdout: boundedOutput(error?.stdout), stderr: boundedOutput(error?.stderr) }
    // eslint-disable-next-line preserve-caught-error -- Exec errors retain private arguments/environment; expose only the bounded allowlist.
    throw new Error(`Windows process identity query or handle-based cleanup failed: ${JSON.stringify(diagnostic)}`)
  }
  try { return JSON.parse(stdout) } catch { throw new Error('Windows process identity command returned invalid JSON.') }
}

export async function windowsProcessIdentity(pid, { command = execute } = {}) {
  validPID(pid)
  const value = await run({ action: 'read', pid }, command)
  if (value === null) return null
  identity(value)
  assert.equal(value.pid, pid, 'Windows process query returned another PID')
  return value
}

export async function windowsManagedProcessIdentity(pid, serverExecutable, appExecutable, { query = windowsProcessIdentity, allowMissing = false } = {}) {
  const server = await query(pid)
  if (allowMissing && server === null) return undefined
  const managedServer = assertWindowsProcessOwner(server, serverExecutable)
  const app = await query(managedServer.parent)
  if (allowMissing && app === null) return undefined
  const managedApp = assertWindowsProcessOwner(app, appExecutable)
  assert.ok(BigInt(managedServer.birth) >= BigInt(managedApp.birth), 'The managed Server predates its claimed Desktop parent')
  return { managedServer, managedApp }
}

export async function stopWindowsProcessTree(expected, { command = execute } = {}) {
  identity(expected)
  const value = await run({ action: 'stop', pid: expected.pid, expected }, command)
  assert.ok(value && typeof value.stopped === 'boolean', 'Windows cleanup returned an invalid result')
  if (value.current !== null) {
    identity(value.current)
    assert.equal(value.current.pid, expected.pid, 'Windows cleanup returned another PID')
  }
  assert.equal(value.stopped, sameWindowsProcess(expected, value.current), 'Windows cleanup did not preserve the recorded process identity')
  return value
}

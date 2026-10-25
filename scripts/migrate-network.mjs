#!/usr/bin/env node
// Versioned, offline conversion for the pre-HTTP Astral network layout.
// Runtime code deliberately has no reader for these removed fields/listeners.
import { promises as fs } from 'node:fs'
import path from 'node:path'
import os from 'node:os'
import { execFile } from 'node:child_process'
import { pathToFileURL } from 'node:url'

function runDocker(...values) {
  const options = typeof values.at(-1) === 'object' ? values.pop() : {}
  return new Promise((resolve, reject) => {
    const child = execFile('docker', values, { maxBuffer: 8 << 20, timeout: 60000, env: options.env || process.env }, (error, stdout) => error ? reject(error) : resolve(stdout))
    child.stdin.on('error', () => {})
    child.stdin.end(options.stdin)
  })
}
export function localDockerEndpoint(endpoint) {
  if (typeof endpoint !== 'string') return false
  const normal = endpoint.toLowerCase().replaceAll('\\', '/')
  const prefix = 'npipe:////./pipe/'
  if (normal.startsWith(prefix)) return normal.length > prefix.length && !normal.slice(prefix.length).includes('/') && !/[\0\r\n]/.test(normal.slice(prefix.length))
  try { const parsed = new URL(endpoint); return parsed.protocol === 'unix:' && parsed.host === '' && parsed.pathname.startsWith('/') } catch { return false }
}
function verifiedDocker() {
  let client
  return async (...args) => {
    if (!client) {
      let endpoint = process.env.DOCKER_CONTEXT ? undefined : process.env.DOCKER_HOST
      if (!endpoint) endpoint = JSON.parse((await runDocker('context', 'inspect', ...(process.env.DOCKER_CONTEXT ? [process.env.DOCKER_CONTEXT] : []), '--format', '{{json .Endpoints.docker.Host}}')).trim())
      if (!localDockerEndpoint(endpoint)) throw new Error('Network migration requires a local Docker Unix socket or current-machine Windows named pipe; remote Docker endpoints are refused.')
      const env = Object.fromEntries(Object.entries(process.env).filter(([key]) => !['DOCKER_CONTEXT', 'DOCKER_HOST', 'DOCKER_TLS', 'DOCKER_TLS_VERIFY', 'DOCKER_CERT_PATH', 'BUILDX_BUILDER', 'BUILDKIT_HOST'].includes(key.toUpperCase())))
      client = (...values) => {
        const options = typeof values.at(-1) === 'object' ? values.pop() : {}
        return runDocker('--host', endpoint, ...values, { ...options, env })
      }
    }
    return client(...args)
  }
}

export function executableArchive(bytes) {
  const header = Buffer.alloc(512)
  header.write('astral')
  header.write('0000755\0', 100)
  header.write('0000000\0', 108)
  header.write('0000000\0', 116)
  header.write(bytes.length.toString(8).padStart(11, '0') + '\0', 124)
  header.write('00000000000\0', 136)
  header.fill(32, 148, 156)
  header.write('0', 156)
  header.write('ustar\0', 257)
  header.write('00', 263)
  header.write([...header].reduce((sum, value) => sum + value, 0).toString(8).padStart(6, '0') + '\0 ', 148)
  return Buffer.concat([header, bytes, Buffer.alloc((512 - bytes.length % 512) % 512 + 1024)])
}
function binaryArchitecture(bytes) {
  if (bytes.length < 20 || !bytes.subarray(0, 4).equals(Buffer.from([0x7f, 0x45, 0x4c, 0x46])) || bytes[4] !== 2 || bytes[5] !== 1) throw new Error('--container-binary must be a downloaded 64-bit Linux ELF executable.')
  const architecture = { 62: 'amd64', 183: 'arm64' }[bytes.readUInt16LE(18)]
  if (!architecture) throw new Error('The container binary architecture is unsupported.')
  return architecture
}
export const migrationID = '001-http-main-listener'

function serverURL(raw) {
  const parsed = new URL(raw)
  if (!['http:', 'https:'].includes(parsed.protocol) || !parsed.hostname || parsed.username || parsed.password || parsed.search || parsed.hash) throw new Error('Server URL must be HTTP or HTTPS without credentials, query or fragment.')
  return raw.replace(/\/$/, '')
}
function listenPort(raw) {
  const parsed = new URL(`http://${raw}`)
  const port = Number(parsed.port || 80)
  if (!Number.isInteger(port) || port < 1 || port > 65535 || parsed.username || parsed.password || parsed.pathname !== '/') throw new Error('--listen requires a fixed HOST:PORT.')
  return port
}

export function convertConnection(value, options) {
  const result = { ...value }
  if (typeof value.server_url !== 'string') return { value: result, changed: false }
  const source = value.server_url.replace(/\/$/, '')
  if (options.fromURL && source === options.fromURL) result.server_url = options.serverURL
  if (value.server_ca) {
    const parsed = new URL(source)
    if (options.bridge && value.server_ca === options.bridge.certificate && parsed.hostname === 'host.docker.internal' && Number(parsed.port) === options.bridge.port) {
      result.server_url = `http://host.docker.internal:${options.port}`
    } else if (!options.fromURL || source !== options.fromURL) {
      throw new Error('An enrolled private CA has no explicit target mapping; supply --from-url and --server-url. No credentials were changed.')
    }
  }
  delete result.server_ca
  return { value: result, changed: JSON.stringify(result) !== JSON.stringify(value) }
}

async function readJSON(file) {
  try { return JSON.parse(await fs.readFile(file, 'utf8')) } catch (error) { if (error.code === 'ENOENT') return undefined; throw error }
}
async function configurationFiles(root) {
  const found = []
  async function visit(directory) {
    for (const entry of await fs.readdir(directory, { withFileTypes: true }).catch(error => { if (error.code === 'ENOENT') return []; throw error })) {
      if (entry.isSymbolicLink()) continue
      const file = path.join(directory, entry.name)
      // Agent homes can be very large and are entirely outside this migration.
      if (entry.isDirectory() && !['agents', 'node_modules', '.git', 'network-migrations'].includes(entry.name)) await visit(file)
      if (entry.isFile() && ['credentials.json', 'enrollment.json', 'container-enrollment.json'].includes(entry.name)) found.push(file)
    }
  }
  await visit(root)
  return found
}
export function hostMountSource(source, platform = process.platform) {
  if (platform === 'win32') {
    const translated = source.match(/^\/(?:run\/desktop\/mnt\/host|host_mnt)\/([a-z])(?:\/(.*))?$/i)
    if (translated) return `${translated[1].toUpperCase()}:\\${(translated[2] || '').replaceAll('/', '\\')}`
  }
  if (platform === 'darwin' && source.startsWith('/host_mnt/')) return source.slice('/host_mnt'.length)
  return source
}
async function canonicalPath(file) {
  try { return await fs.realpath(file) } catch (error) {
    if (error.code !== 'ENOENT') throw error
    const parent = path.dirname(file)
    if (parent === file) throw error
    return path.join(await canonicalPath(parent), path.basename(file))
  }
}
function contained(root, file) {
  const relative = path.relative(root, file)
  return relative !== '' && !relative.startsWith(`..${path.sep}`) && relative !== '..' && !path.isAbsolute(relative)
}
async function writePrivate(file, bytes) {
  await fs.mkdir(path.dirname(file), { recursive: true, mode: 0o700 })
  const temporary = `${file}.${process.pid}.migration`
  try {
    const output = await fs.open(temporary, 'wx', 0o600)
    try { await output.writeFile(bytes); await output.sync() } finally { await output.close() }
    await fs.rename(temporary, file)
  } finally { await fs.rm(temporary, { force: true }) }
}

async function assertMaintenanceLock({ serverData, computersDirectory }) {
  const holderPID = Number(process.env.ASTRAL_MAINTENANCE_LOCK_PID)
  const data = process.env.ASTRAL_MAINTENANCE_DATA
  const computers = process.env.ASTRAL_MAINTENANCE_COMPUTERS
  if (!Number.isSafeInteger(holderPID) || holderPID < 1 || holderPID !== process.ppid || !data || !computers) throw new Error('Apply and restore require native data locks. Run astral maintenance with-data-lock --data SERVER_DATA --computers-dir COMPUTERS -- node scripts/migrate-network.mjs ...')
  const sameDirectory = (actual, expected) => process.platform === 'win32' ? actual.toLowerCase() === expected.toLowerCase() : actual === expected
  if (!sameDirectory(await fs.realpath(data), serverData) || !sameDirectory(await fs.realpath(computers), computersDirectory)) throw new Error('The maintenance wrapper locks a different Server or Computers directory.')
  process.kill(holderPID, 0)
  return { holderPID, ownsLocks: true }
}

async function assertProcessStopped(pid) {
  if (!Number.isSafeInteger(pid) || pid < 1) throw new Error('An Astral process lock has no readable PID; verify every Astral process is stopped before removing the stale lock and retrying.')
  try { process.kill(pid, 0); throw new Error('Stop Astral Server/Desktop and its Daemons before applying this offline migration.') } catch (error) { if (error.code !== 'ESRCH') throw error }
}
async function assertLockStopped(file, maintenance) {
  // Windows LockFileEx can deny even reads of the held byte. The verified
  // native parent owns these OS locks for the child process's full lifetime.
  if (maintenance?.ownsLocks) return
  let raw
  try { raw = await fs.readFile(file, 'utf8') } catch (error) { if (error.code === 'ENOENT') return; throw error }
  await assertProcessStopped(Number(raw.trim()))
}
async function assertServerStopped(data, maintenance) {
  if (maintenance?.ownsLocks) return
  await assertLockStopped(path.join(data, 'server.lock'))
  const running = await readJSON(path.join(data, 'running.json'))
  if (running?.pid) await assertProcessStopped(running.pid)
}
async function assertContainerStopped(docker, item) {
  const current = JSON.parse(await docker('inspect', item.id))[0]
  if (current?.Id !== item.id || current.State?.Running || current.Config?.Labels?.['astral.operation'] !== item.identity || JSON.stringify(current.Mounts) !== JSON.stringify(item.mounts)) throw new Error('Stop the original container before migration; its identity and volumes must match the backup.')
}

export async function restoreNetworkBackup(directory, dependencies = {}) {
  const removeMarker = dependencies.removeMarker || (file => fs.rm(file, { force: true }))
  const docker = dependencies.docker || verifiedDocker()
  const root = path.resolve(directory)
  const manifest = await readJSON(path.join(root, 'manifest.json'))
  if (manifest?.id !== migrationID || !Array.isArray(manifest.files) || !Array.isArray(manifest.containers)) throw new Error('Not an Astral network migration backup.')
  if (!manifest.server_data || !manifest.computers_directory) throw new Error('The backup lacks its Server or Computers directory.')
  const data = await fs.realpath(manifest.server_data)
  const computers = await fs.realpath(manifest.computers_directory)
  const validateLock = () => (dependencies.assertMaintenanceLock || assertMaintenanceLock)({ serverData: data, computersDirectory: computers })
  const maintenance = await validateLock()
  await assertServerStopped(data, maintenance)
  for (const item of manifest.containers) await assertContainerStopped(docker, item)
  for (const item of manifest.files) if (path.basename(item.file) === 'credentials.json') await assertLockStopped(path.join(path.dirname(item.file), 'daemon.lock'), maintenance)
  await validateLock()
  // Recovery itself can be interrupted. Keep its pending marker until the
  // obsolete completion receipt has been removed successfully.
  await writePrivate(manifest.pending, JSON.stringify({ id: migrationID, backup: root }) + '\n')
  for (const item of manifest.files) {
    await validateLock()
    if (item.backup) await writePrivate(item.file, await fs.readFile(path.join(root, item.backup)))
    else await fs.rm(item.file, { force: true })
  }
  for (const item of manifest.containers) {
    await validateLock()
    await assertContainerStopped(docker, item)
    await docker('cp', path.join(root, `container-${item.id}.json`), `${item.id}:/var/lib/astral/credentials.json`)
    let bytes
    try { bytes = await fs.readFile(path.join(root, `container-${item.id}.astral`)) } catch (error) { if (error.code !== 'ENOENT') throw error }
    if (bytes) await docker('cp', '-', `${item.id}:/usr/local/bin`, { stdin: executableArchive(bytes) })
  }
  if (manifest.receipt) await removeMarker(manifest.receipt)
  if (manifest.pending) await removeMarker(manifest.pending)
  return { id: migrationID, restored: true, backup: root }
}

// Docker is injectable so acceptance tests prove that only file copies and
// inspection occur; the migration cannot rebuild or recreate a container.
export async function migrateNetwork(options, dependencies = {}) {
  const docker = dependencies.docker || verifiedDocker()
  const data = await fs.realpath(options.serverData)
  const computers = await fs.realpath(options.computersDirectory)
  if (!(await fs.stat(data)).isDirectory() || !(await fs.stat(computers)).isDirectory()) throw new Error('Server data and Computers paths must be existing directories.')
  const validateLock = () => (dependencies.assertMaintenanceLock || assertMaintenanceLock)({ serverData: data, computersDirectory: computers })
  const maintenance = options.apply ? await validateLock() : undefined
  const removeMarker = dependencies.removeMarker || (file => fs.rm(file, { force: true }))
  const receipt = path.join(data, 'network-migrations', `${migrationID}.json`)
  const pending = path.join(data, 'network-migrations', `${migrationID}.pending.json`)
  const interrupted = await readJSON(pending)
  if (interrupted) throw new Error(`An interrupted network migration requires recovery through astral maintenance with-data-lock first; pass --restore-backup '${interrupted.backup}' to this script.`)
  if (await readJSON(receipt)) return { id: migrationID, alreadyApplied: true, changes: [] }
  await assertServerStopped(data, maintenance)
  const bridgePath = path.join(data, 'container-bridge.json')
  const bridge = await readJSON(bridgePath)
  const networkPath = path.join(data, 'network.json')
  const network = await readJSON(networkPath)
  const legacyPath = options.installerRoot ? path.join(path.resolve(options.installerRoot), 'install-config.json') : undefined
  const legacy = legacyPath ? await readJSON(legacyPath) : undefined
  const listen = options.listen || network?.listen || legacy?.listen || '0.0.0.0:4545'
  const port = listenPort(listen)
  if (bridge && !['0.0.0.0', '[::]'].includes(new URL(`http://${listen}`).hostname)) throw new Error('Migrating Docker Desktop bridge users requires a wildcard main listener; pass --listen 0.0.0.0:4545 (or another main port).')
  const explicitURL = options.serverURL ? serverURL(options.serverURL) : undefined
  const fromURL = options.fromURL ? serverURL(options.fromURL) : undefined
  if (Boolean(fromURL) !== Boolean(explicitURL)) throw new Error('--from-url and --server-url must be supplied together.')
  if ((legacy?.tls_cert || legacy?.tls_key) && !explicitURL) throw new Error('Native TLS is removed. Choose the new HTTP or reverse-proxy HTTPS address explicitly with --from-url and --server-url.')
  const conversion = { bridge, port, fromURL, serverURL: explicitURL }
  const files = []
  let hasLocalContainerConfiguration = false
  for (const file of await configurationFiles(computers)) {
    if (path.basename(file) === 'credentials.json') await assertLockStopped(path.join(path.dirname(file), 'daemon.lock'), maintenance)
    const value = await readJSON(file)
    if (path.basename(file) !== 'credentials.json') hasLocalContainerConfiguration = true
    const converted = convertConnection(value, conversion)
    if (converted.changed) files.push({ file, bytes: JSON.stringify(converted.value, null, 2) + '\n' })
  }
  const nextNetwork = {
    ...(legacy?.listen ? { listen: legacy.listen } : {}),
    ...(legacy?.public_url ? { public_url: legacy.public_url } : {}),
    ...(network || {}),
    listen,
    allow_remote_management: network?.allow_remote_management ?? false,
  }
  if (explicitURL) nextNetwork.public_url = explicitURL
  // The canonical file contains only the final network schema.
  const finalNetwork = Object.fromEntries(Object.entries(nextNetwork).filter(([key]) => ['listen', 'public_url', 'allow_remote_management'].includes(key)))
  if (JSON.stringify(network) !== JSON.stringify(finalNetwork)) files.push({ file: networkPath, bytes: JSON.stringify(finalNetwork, null, 2) + '\n' })
  if (options.installerRoot) {
    const installerRoot = path.resolve(options.installerRoot)
    const unitFile = options.serviceUnit || path.join(process.env.XDG_CONFIG_HOME || path.join(os.homedir(), '.config'), 'systemd', 'user', 'astral-server.service')
    let unit
    try { unit = await fs.readFile(unitFile, 'utf8') } catch (error) { if (error.code !== 'ENOENT') throw error }
    if (unit !== undefined) {
      if (!unit.startsWith(`# Astral Server installer root: ${installerRoot}\n`)) throw new Error('The supplied user service does not belong to this installer root.')
      // Only the installer-generated quoted network arguments are removed.
      // Program/data paths and user-service ownership remain unchanged.
      const updated = unit.replace(/^(ExecStart=.*)$/m, line => line.replace(/ "(?:--listen|--public-url|--tls-cert|--tls-key)" "(?:\\.|[^"\\])*"/g, ''))
      if (updated !== unit) files.push({ file: unitFile, bytes: updated })
    }
  }
  const containers = []
  if (bridge || hasLocalContainerConfiguration || options.containers?.length) {
    const ids = [...new Set([...(await docker('ps', '-a', '--filter', 'label=astral.operation', '--format', '{{.ID}}')).trim().split(/\s+/).filter(Boolean), ...(options.containers || [])])]
    for (const candidate of ids) {
      const inspected = JSON.parse(await docker('inspect', candidate))[0]
      if (!inspected?.Id || !inspected.Config?.Labels?.['astral.operation']) throw new Error('Refusing a container without its Astral operation identity.')
      const enrollment = inspected.Mounts?.find(mount => mount.Destination === '/run/astral-enrollment.json')
      if (!enrollment?.Source || !contained(computers, await canonicalPath(hostMountSource(enrollment.Source)))) {
        if (options.containers?.includes(candidate)) throw new Error(`Container ${candidate} does not belong to the supplied Computers directory.`)
        continue
      }
      if (inspected.State?.Running) throw new Error(`Stop the existing container ${inspected.Name || inspected.Id} before migration. It will be retained, not recreated.`)
      const previous = containers.find(item => item.id === inspected.Id)
      if (previous) { previous.selected ||= options.containers?.includes(candidate); continue }
      containers.push({ selected: options.containers?.includes(candidate), id: inspected.Id, identity: inspected.Config.Labels['astral.operation'], mounts: inspected.Mounts, image: inspected.Image })
    }
  }
  const binaries = new Map()
  for (const [file, expected] of [[options.containerBinary], [options.containerBinaryAMD64, 'amd64'], [options.containerBinaryARM64, 'arm64']]) {
    if (!file) continue
    const bytes = await fs.readFile(path.resolve(file))
    const architecture = binaryArchitecture(bytes)
    if (expected && architecture !== expected) throw new Error(`The ${expected} binary option contains a different ELF architecture.`)
    binaries.set(architecture, bytes)
  }
  const temporary = await fs.mkdtemp(path.join(os.tmpdir(), 'astral-network-migration-'))
  try {
    for (const container of containers) {
      const file = path.join(temporary, `${container.id}.json`)
      await docker('cp', `${container.id}:/var/lib/astral/credentials.json`, file)
      const value = await readJSON(file)
      if (!value?.computer_id || !value.token) throw new Error(`Container ${container.id} has no usable enrolled Computer credentials.`)
      const converted = convertConnection(value, conversion)
      container.original = await fs.readFile(file)
      container.bytes = converted.changed ? JSON.stringify(converted.value, null, 2) + '\n' : undefined
      if (container.bytes) {
        const image = JSON.parse(await docker('image', 'inspect', container.image))[0]
        container.architecture = image?.Architecture
        container.binary = binaries.get(container.architecture)
        if (image?.Os !== 'linux' || !['amd64', 'arm64'].includes(container.architecture)) throw new Error('The existing container image is not a supported Linux architecture.')
        if (options.apply && !container.binary) throw new Error(`Supply --container-binary (or --container-binary-${container.architecture}) with this release’s verified Linux ${container.architecture} Daemon; the old program cannot use the converted HTTP enrollment.`)
        if (container.binary) {
          const oldBinary = path.join(temporary, `${container.id}.astral`)
          await docker('cp', `${container.id}:/usr/local/bin/astral`, oldBinary)
          container.originalBinary = await fs.readFile(oldBinary)
        }
      }
    }
    const changes = [...files.map(item => item.file), ...containers.filter(item => item.bytes).flatMap(item => [`container:${item.id}:/var/lib/astral/credentials.json`, `container:${item.id}:/usr/local/bin/astral`]), ...(bridge ? [bridgePath] : []), ...(legacy ? [legacyPath] : [])]
    if (!options.apply) return { id: migrationID, changes, containers: containers.map(({ id, identity, architecture }) => ({ id, identity, architecture })) }
    if (containers.some(item => item.bytes && !item.selected)) throw new Error('Choose every affected existing container ID explicitly with --container ID (repeat for multiple containers); inspect the complete plan without --apply first.')
    const backup = path.join(data, 'network-migrations', `${migrationID}-${Date.now()}.backup`)
    await fs.mkdir(backup, { recursive: true, mode: 0o700 })
    const originals = []
    for (const item of files) {
      const bytes = await fs.readFile(item.file).catch(error => { if (error.code === 'ENOENT') return undefined; throw error })
      originals.push({ file: item.file, bytes })
    }
    for (const file of [bridge ? bridgePath : undefined, legacy ? legacyPath : undefined].filter(Boolean)) originals.push({ file, bytes: await fs.readFile(file), remove: true })
    // Finish every backup before making the first mutation.
    for (let i = 0; i < originals.length; i++) if (originals[i].bytes) await writePrivate(path.join(backup, `file-${i}.json`), originals[i].bytes)
    for (const item of containers) {
      await writePrivate(path.join(backup, `container-${item.id}.json`), item.original)
      if (item.originalBinary) await writePrivate(path.join(backup, `container-${item.id}.astral`), item.originalBinary)
    }
    await writePrivate(path.join(backup, 'manifest.json'), JSON.stringify({ id: migrationID, server_data: data, computers_directory: computers, pending, receipt, files: originals.map(({ file, bytes }, i) => ({ file, backup: bytes ? `file-${i}.json` : null })), containers: containers.map(({ id, identity, mounts }) => ({ id, identity, mounts })) }, null, 2) + '\n')
    await writePrivate(pending, JSON.stringify({ id: migrationID, backup }) + '\n')
    const changedContainers = []
    try {
      await validateLock()
      await assertServerStopped(data, maintenance)
      for (const item of containers) await assertContainerStopped(docker, item)
      for (const item of files) if (path.basename(item.file) === 'credentials.json') await assertLockStopped(path.join(path.dirname(item.file), 'daemon.lock'), maintenance)
      for (const item of files) await writePrivate(item.file, item.bytes)
      for (const item of containers.filter(item => item.bytes)) {
        const file = path.join(temporary, `${item.id}.new.json`)
        await writePrivate(file, item.bytes)
        await validateLock()
        await assertContainerStopped(docker, item)
        changedContainers.push(item)
        await docker('cp', file, `${item.id}:/var/lib/astral/credentials.json`)
        await docker('cp', '-', `${item.id}:/usr/local/bin`, { stdin: executableArchive(item.binary) })
        const after = JSON.parse(await docker('inspect', item.id))[0]
        if (after.Id !== item.id || JSON.stringify(after.Mounts) !== JSON.stringify(item.mounts) || after.State?.Running) throw new Error('Container identity, volumes or stopped state changed during migration.')
      }
      for (const item of originals.filter(item => item.remove)) await fs.unlink(item.file)
      await writePrivate(receipt, JSON.stringify({ id: migrationID, applied_at: new Date().toISOString(), backup, changes }, null, 2) + '\n')
      await removeMarker(pending)
    } catch (error) {
      try { await validateLock() } catch (lockError) {
        throw new Error(`${error.message}. Native data lock was lost; recover through the maintenance wrapper before retrying. Backup: ${backup}. ${lockError.message}`, { cause: lockError })
      }
      const failures = []
      for (const item of originals) {
        try { if (item.bytes) await writePrivate(item.file, item.bytes); else await fs.rm(item.file, { force: true }) } catch (restoreError) { failures.push(restoreError.message) }
      }
      for (const item of changedContainers) {
        try {
          await docker('cp', path.join(backup, `container-${item.id}.json`), `${item.id}:/var/lib/astral/credentials.json`)
          if (item.originalBinary) await docker('cp', '-', `${item.id}:/usr/local/bin`, { stdin: executableArchive(item.originalBinary) })
        } catch (restoreError) { failures.push(restoreError.message) }
      }
      // A failed final marker cleanup rolls data back too. Never leave a
      // completed receipt describing data that was restored to the old layout.
      try { await removeMarker(receipt) } catch (restoreError) { failures.push(restoreError.message) }
      if (!failures.length) {
        try { await removeMarker(pending) } catch (restoreError) { failures.push(restoreError.message) }
      }
      if (failures.length) {
        // Even an uncertain unlink outcome must leave recovery authoritative
        // over the completion receipt on the next invocation.
        try { await writePrivate(pending, JSON.stringify({ id: migrationID, backup }) + '\n') } catch (restoreError) { failures.push(restoreError.message) }
      }
      throw new Error(`${error.message}. Original files backed up at ${backup}.${failures.length ? ` Recovery needs attention: ${failures.join('; ')}` : ' Original configuration restored.'}`, { cause: error })
    }
    return { id: migrationID, changes, backup, containers: containers.map(({ id, identity, architecture }) => ({ id, identity, architecture })) }
  } finally { await fs.rm(temporary, { recursive: true, force: true }) }
}

async function main() {
  const options = { containers: [] }
  const names = { '--server-data': 'serverData', '--computers-dir': 'computersDirectory', '--installer-root': 'installerRoot', '--service-unit': 'serviceUnit', '--container-binary': 'containerBinary', '--container-binary-amd64': 'containerBinaryAMD64', '--container-binary-arm64': 'containerBinaryARM64', '--listen': 'listen', '--from-url': 'fromURL', '--server-url': 'serverURL' }
  const args = process.argv.slice(2)
  if (args.includes('--help')) {
    console.log(`Astral offline network migration ${migrationID}
Stop Server/Desktop and the affected existing containers first.

Plan: node scripts/migrate-network.mjs --server-data PATH --computers-dir COMPUTERS [OPTIONS]
Apply: astral maintenance with-data-lock --data PATH --computers-dir COMPUTERS -- node scripts/migrate-network.mjs --server-data PATH --computers-dir COMPUTERS [OPTIONS] --apply
Restore: astral maintenance with-data-lock --data PATH --computers-dir COMPUTERS -- node scripts/migrate-network.mjs --restore-backup BACKUP
  --listen HOST:PORT       Main HTTP listener (Docker Desktop requires wildcard)
  --installer-root PATH    Convert legacy headless installer preferences
  --service-unit PATH      Installer-owned systemd user unit (optional override)
  --from-url OLD --server-url NEW
                           Explicit mapping when removing native Server TLS
  --container ID           Existing stopped container to update (repeatable)
  --container-binary PATH  Verified new Linux ELF Daemon for those containers
  --container-binary-amd64 PATH / --container-binary-arm64 PATH
                           Supply both if existing containers use both CPUs
  --apply                  Apply with backups through the native lock wrapper
  --restore-backup PATH    Recover an interrupted conversion before retrying

The migration retains Computer identity, container IDs, volumes and agent files.
It does not start, remove, rebuild or recreate containers. Install the new Server
before restarting the same containers. Backups contain credentials; keep private.`)
    return
  }
  for (let i = 0; i < args.length; i++) {
    if (args[i] === '--restore-backup' && args[i + 1]) options.restoreBackup = args[++i]
    else if (args[i] === '--apply') options.apply = true
    else if (args[i] === '--container' && args[i + 1]) options.containers.push(args[++i])
    else if (names[args[i]] && args[i + 1]) options[names[args[i]]] = args[++i]
    else throw new Error('Usage: node scripts/migrate-network.mjs --server-data PATH --computers-dir PATH [--installer-root PATH] [--service-unit PATH] [--listen 0.0.0.0:4545] [--from-url OLD --server-url NEW] [--container ID --container-binary LINUX_BINARY] [--apply]')
  }
  if (options.restoreBackup) { console.log(JSON.stringify(await restoreNetworkBackup(options.restoreBackup), null, 2)); return }
  if (!options.serverData || !options.computersDirectory) throw new Error('--server-data and --computers-dir are required. Stop Server/Desktop and affected containers first; omit --apply to inspect the plan.')
  const result = await migrateNetwork(options)
  console.log(JSON.stringify(result, null, 2))
  if (!options.apply) console.log('Plan only. Apply through astral maintenance with-data-lock with --apply, then install the new program before restarting the same containers.')
}
if (process.argv[1] && import.meta.url === pathToFileURL(path.resolve(process.argv[1])).href) main().catch(error => { console.error(error.message); process.exitCode = 1 })

import test from 'node:test'
import assert from 'node:assert/strict'
import { promises as fs } from 'node:fs'
import path from 'node:path'
import os from 'node:os'
import { convertConnection, migrateNetwork as migrateNetworkCommand, executableArchive } from './migrate-network.mjs'

// Tests inject only the native lock assertion, never production environment.
const testMaintenanceLock = async () => undefined
const migrateNetwork = (options, dependencies = {}) => migrateNetworkCommand(options, { assertMaintenanceLock: testMaintenanceLock, ...dependencies })

async function fixture(t) {
  const directory = await fs.mkdtemp(path.join(os.tmpdir(), 'astral-network-test-'))
  t.after(() => fs.rm(directory, { recursive: true, force: true }))
  const serverData = path.join(directory, 'server')
  const computersDirectory = path.join(directory, 'computers')
  await fs.mkdir(serverData)
  await fs.mkdir(computersDirectory)
  const binary = Buffer.alloc(32)
  binary.set([0x7f, 0x45, 0x4c, 0x46, 2, 1])
  binary.writeUInt16LE(183, 18)
  const containerBinary = path.join(directory, 'astral-linux-arm64')
  await fs.writeFile(containerBinary, binary)
  return { directory, serverData, computersDirectory, containerBinary, listen: '0.0.0.0:4545' }
}
async function json(file, value) {
  await fs.mkdir(path.dirname(file), { recursive: true })
  await fs.writeFile(file, JSON.stringify(value))
}

test('private bridge conversion removes only retired transport fields', () => {
  const before = { server_url: 'https://host.docker.internal:54321', server_ca: 'old-ca', computer_id: 'same-computer', token: 'same-token', enrollment_id: 'same-enrollment' }
  const after = convertConnection(before, { bridge: { certificate: 'old-ca', port: 54321 }, port: 4545 })
  assert.deepEqual(after.value, { server_url: 'http://host.docker.internal:4545', computer_id: 'same-computer', token: 'same-token', enrollment_id: 'same-enrollment' })
  assert.throws(() => convertConnection(before, { port: 4545 }), /explicit target mapping/)
  const proxy = { server_url: 'https://server.example', computer_id: 'remote', token: 'token' }
  assert.deepEqual(convertConnection(proxy, { port: 4545 }), { value: proxy, changed: false })
})

test('offline migration preserves container ID, volume, identity and agent files and is once-only', async t => {
  const options = await fixture(t)
  await json(path.join(options.serverData, 'container-bridge.json'), { certificate: 'old-ca', private_key: 'private', port: 54321 })
  const enrollmentFile = path.join(options.computersDirectory, 'provisioning', 'same-operation', 'enrollment.json')
  const connection = { server_url: 'https://host.docker.internal:54321', server_ca: 'old-ca', computer_id: 'same-computer', token: 'same-secret' }
  await json(enrollmentFile, connection)
  const agentFile = path.join(options.computersDirectory, 'local', 'agents', 'bot', 'workspace.txt')
  await fs.mkdir(path.dirname(agentFile), { recursive: true })
  await fs.writeFile(agentFile, 'untouched user workspace')
  const inspect = { Id: 'same-container', Image: 'same-image', Config: { Labels: { 'astral.operation': 'same-operation' } }, State: { Running: false }, Mounts: [{ Type: 'bind', Destination: '/run/astral-enrollment.json', Source: enrollmentFile }, { Type: 'volume', Name: 'same-volume', Destination: '/var/lib/astral' }] }
  let credentials = JSON.stringify(connection)
  let program = Buffer.from('original program')
  const calls = []
  const docker = async (...args) => {
    calls.push(args)
    if (args[0] === 'ps') return 'same-container\n'
    if (args[0] === 'inspect') return JSON.stringify([inspect])
    if (args[0] === 'image') return JSON.stringify([{ Architecture: 'arm64', Os: 'linux' }])
    assert.equal(args[0], 'cp', 'migration must not rebuild, stop, remove or create containers')
    if (args[1] === '-') { program = args[3].stdin.subarray(512, 544) }
    else if (args[1].startsWith('same-container:/usr/local')) await fs.writeFile(args[2], program)
    else if (args[1].startsWith('same-container:')) await fs.writeFile(args[2], credentials)
    else credentials = await fs.readFile(args[1], 'utf8')
    return ''
  }
  const planned = await migrateNetwork(options, { docker })
  assert.equal(planned.changes.length, 5)
  assert.equal(JSON.parse(await fs.readFile(enrollmentFile, 'utf8')).server_ca, 'old-ca')
  const result = await migrateNetwork({ ...options, containers: ['same-container'], apply: true }, { docker })
  assert.ok(result.backup)
  assert.equal(JSON.parse(credentials).computer_id, 'same-computer')
  assert.equal(JSON.parse(credentials).token, 'same-secret')
  assert.equal(JSON.parse(credentials).server_url, 'http://host.docker.internal:4545')
  assert.equal(JSON.parse(credentials).server_ca, undefined)
  assert.equal(await fs.readFile(agentFile, 'utf8'), 'untouched user workspace')
  await assert.rejects(fs.access(path.join(options.serverData, 'container-bridge.json')))
  assert.deepEqual(await migrateNetwork({ ...options, containers: ['same-container'], apply: true }, { docker }), { id: '001-http-main-listener', alreadyApplied: true, changes: [] })
  assert.ok(calls.every(args => ['ps', 'inspect', 'cp', 'image'].includes(args[0])))
  assert.deepEqual(program, await fs.readFile(options.containerBinary))
})

test('refuses running containers before mutating persistent configuration', async t => {
  const options = await fixture(t)
  await json(path.join(options.serverData, 'container-bridge.json'), { certificate: 'old-ca', port: 54321 })
  const docker = async (...args) => args[0] === 'ps' ? 'id' : JSON.stringify([{ Id: 'id', Name: '/astral-id', Config: { Labels: { 'astral.operation': 'id' } }, State: { Running: true }, Mounts: [{ Source: path.join(options.computersDirectory, 'enrollment.json'), Destination: '/run/astral-enrollment.json' }] }])
  await assert.rejects(migrateNetwork({ ...options, apply: true }, { docker }), /Stop the existing container/)
  await assert.rejects(fs.access(path.join(options.serverData, 'network.json')))
  assert.ok(await fs.readFile(path.join(options.serverData, 'container-bridge.json')))
})

test('native TLS needs explicit conversion and legacy installer preferences become the sole canonical file', async t => {
  const options = await fixture(t)
  const installerRoot = path.join(options.directory, 'installer')
  const serviceUnit = path.join(options.directory, 'astral-server.service')
  const old = { listen: '0.0.0.0:4545', public_url: 'https://old.example', tls_cert: '/certificate.pem', tls_key: '/private.pem' }
  await json(path.join(installerRoot, 'install-config.json'), old)
  await fs.writeFile(serviceUnit, `# Astral Server installer root: ${installerRoot}\n[Service]\nExecStart=:"/some path/astral" "serve" "--listen" "0.0.0.0:4545" "--data" "some data" "--public-url" "https://old.example" "--tls-cert" "/certificate.pem" "--tls-key" "/private.pem"\n`)
  await assert.rejects(migrateNetwork({ ...options, installerRoot, serviceUnit, apply: true }), /Native TLS is removed/)
  const result = await migrateNetwork({ ...options, installerRoot, serviceUnit, fromURL: 'https://old.example', serverURL: 'http://server.ts.net:4545', apply: true })
  const network = JSON.parse(await fs.readFile(path.join(options.serverData, 'network.json'), 'utf8'))
  assert.deepEqual(network, { listen: '0.0.0.0:4545', public_url: 'http://server.ts.net:4545', allow_remote_management: false })
  const unit = await fs.readFile(serviceUnit, 'utf8')
  assert.match(unit, /"--data" "some data"/)
  assert.doesNotMatch(unit, /--listen|--public-url|--tls-cert|--tls-key/)
  await assert.rejects(fs.access(path.join(installerRoot, 'install-config.json')))
  assert.ok(result.changes.includes(serviceUnit))
})

test('failed Docker file replacement restores original host and volume credentials', async t => {
  const options = await fixture(t)
  options.containers = ['id']
  await json(path.join(options.serverData, 'container-bridge.json'), { certificate: 'old-ca', port: 54321 })
  const enrollmentFile = path.join(options.computersDirectory, 'enrollment.json')
  const old = { server_url: 'https://host.docker.internal:54321', server_ca: 'old-ca', computer_id: 'original', token: 'secret' }
  await json(enrollmentFile, old)
  let credentials = JSON.stringify(old)
  let failed = false
  const docker = async (...args) => {
    if (args[0] === 'ps') return 'id'
    if (args[0] === 'image') return JSON.stringify([{ Architecture: 'arm64', Os: 'linux' }])
    if (args[0] === 'inspect') return JSON.stringify([{ Id: 'id', Image: 'image', Config: { Labels: { 'astral.operation': 'id' } }, State: { Running: false }, Mounts: [{ Source: enrollmentFile, Destination: '/run/astral-enrollment.json' }] }])
    if (args[1] === '-') return ''
    if (args[1].startsWith('id:/usr/local')) await fs.writeFile(args[2], 'old program')
    else if (args[1].startsWith('id:')) await fs.writeFile(args[2], credentials)
    else { credentials = await fs.readFile(args[1], 'utf8'); if (!failed) { failed = true; throw new Error('uncertain Docker copy') } }
    return ''
  }
  await assert.rejects(migrateNetwork({ ...options, apply: true }, { docker }), /Original configuration restored/)
  assert.deepEqual(JSON.parse(credentials), old)
  assert.deepEqual(JSON.parse(await fs.readFile(enrollmentFile, 'utf8')), old)
  await assert.rejects(fs.access(path.join(options.serverData, 'network.json')))
  assert.ok(await fs.readFile(path.join(options.serverData, 'container-bridge.json')))
})


test('executable archive preserves Linux executable mode even when migration runs on Windows', () => {
  const archive = executableArchive(Buffer.from('program'))
  assert.equal(archive.subarray(0, 6).toString(), 'astral')
  assert.equal(archive.subarray(100, 107).toString(), '0000755')
  assert.equal(archive.subarray(512, 519).toString(), 'program')
  assert.equal(archive.length % 512, 0)
})

test('an explicit subset cannot abandon another owned bridge container or remove the bridge', async t => {
  const options = await fixture(t)
  options.containers = ['first']
  await json(path.join(options.serverData, 'container-bridge.json'), { certificate: 'old-ca', port: 54321 })
  const credentials = { server_url: 'https://host.docker.internal:54321', server_ca: 'old-ca', computer_id: 'computer', token: 'token' }
  const docker = async (...args) => {
    if (args[0] === 'ps') return 'first\nsecond'
    if (args[0] === 'inspect') return JSON.stringify([{ Id: args[1], Image: 'image', Config: { Labels: { 'astral.operation': args[1] } }, State: { Running: false }, Mounts: [{ Source: path.join(options.computersDirectory, args[1], 'enrollment.json'), Destination: '/run/astral-enrollment.json' }] }])
    if (args[0] === 'image') return JSON.stringify([{ Architecture: 'arm64', Os: 'linux' }])
    assert.equal(args[0], 'cp')
    assert.match(args[1], /:/, 'must not mutate any container for an incomplete selection')
    await fs.writeFile(args[2], args[1].includes('/credentials.json') ? JSON.stringify(credentials) : 'old binary')
    return ''
  }
  await assert.rejects(migrateNetwork({ ...options, apply: true }, { docker }), /every affected existing container/)
  await assert.rejects(fs.access(path.join(options.serverData, 'network.json')))
  assert.ok(await fs.readFile(path.join(options.serverData, 'container-bridge.json')))
})

test('migration rejects remote Docker endpoints and accepts only local sockets/pipes', async () => {
  const { localDockerEndpoint } = await import('./migrate-network.mjs')
  for (const endpoint of ['unix:///var/run/docker.sock', 'unix:///Users/me/.docker/run/docker.sock', 'npipe:////./pipe/docker_engine']) assert.equal(localDockerEndpoint(endpoint), true)
  for (const endpoint of ['tcp://127.0.0.1:2375', 'ssh://server', 'npipe:////remote/pipe/docker_engine', 'unix://remote/var/run/docker.sock', 'npipe:////./pipe/../other']) assert.equal(localDockerEndpoint(endpoint), false)
})

test('explicit recovery restores interrupted configuration and clears its once-only marker', async t => {
  const { restoreNetworkBackup } = await import('./migrate-network.mjs')
  const options = await fixture(t)
  const original = { server_url: 'https://old.example', computer_id: 'original', token: 'token' }
  const file = path.join(options.computersDirectory, 'local', 'credentials.json')
  await json(file, original)
  const result = await migrateNetwork({ ...options, fromURL: 'https://old.example', serverURL: 'http://server.ts.net:4545', apply: true })
  await restoreNetworkBackup(result.backup, { assertMaintenanceLock: testMaintenanceLock })
  assert.deepEqual(JSON.parse(await fs.readFile(file, 'utf8')), original)
  await assert.rejects(fs.access(path.join(options.serverData, 'network.json')))
  await assert.rejects(fs.access(path.join(options.serverData, 'network-migrations', '001-http-main-listener.json')))
})

test('Docker Desktop VM bind paths are compared with their actual host paths', async () => {
  const { hostMountSource } = await import('./migrate-network.mjs')
  assert.equal(hostMountSource('/run/desktop/mnt/host/c/Users/owner/Astral/enrollment.json', 'win32'), 'C:\\Users\\owner\\Astral\\enrollment.json')
  assert.equal(hostMountSource('/host_mnt/d/Astral/enrollment.json', 'win32'), 'D:\\Astral\\enrollment.json')
  assert.equal(hostMountSource('/host_mnt/Users/owner/Astral/enrollment.json', 'darwin'), '/Users/owner/Astral/enrollment.json')
  assert.equal(hostMountSource('/srv/astral/enrollment.json', 'linux'), '/srv/astral/enrollment.json')
})

test('a container started after inspection is rejected before any volume or program copy', async t => {
  const options = await fixture(t)
  options.containers = ['id']
  await json(path.join(options.serverData, 'container-bridge.json'), { certificate: 'old-ca', port: 54321 })
  const old = { server_url: 'https://host.docker.internal:54321', server_ca: 'old-ca', computer_id: 'computer', token: 'token' }
  let inspections = 0
  const docker = async (...args) => {
    if (args[0] === 'ps') return 'id'
    if (args[0] === 'inspect') return JSON.stringify([{ Id: 'id', Image: 'image', Config: { Labels: { 'astral.operation': 'id' } }, State: { Running: ++inspections > 1 }, Mounts: [{ Source: path.join(options.computersDirectory, 'enrollment.json'), Destination: '/run/astral-enrollment.json' }] }])
    if (args[0] === 'image') return JSON.stringify([{ Architecture: 'arm64', Os: 'linux' }])
    assert.equal(args[0], 'cp')
    assert.match(args[1], /^id:/, 'the migration must not mutate a container that started during the plan')
    await fs.writeFile(args[2], args[1].includes('credentials.json') ? JSON.stringify(old) : 'old program')
    return ''
  }
  await assert.rejects(migrateNetwork({ ...options, apply: true }, { docker }), /Stop the original container/)
  await assert.rejects(fs.access(path.join(options.serverData, 'network.json')))
})

test('a live server.lock PID blocks migration before startup has written running.json', async t => {
  const options = await fixture(t)
  await fs.writeFile(path.join(options.serverData, 'server.lock'), `${process.pid}\n`)
  await assert.rejects(migrateNetwork({ ...options, apply: true }), /Stop Astral Server\/Desktop/)
  await assert.rejects(fs.access(path.join(options.serverData, 'network.json')))
})

test('a standalone live Daemon lock blocks changing its credential file', async t => {
  const options = await fixture(t)
  const directory = path.join(options.computersDirectory, 'remote')
  await json(path.join(directory, 'credentials.json'), { server_url: 'https://old.example', computer_id: 'id', token: 'token' })
  await fs.writeFile(path.join(directory, 'daemon.lock'), `${process.pid}\n`)
  await assert.rejects(migrateNetwork({ ...options, fromURL: 'https://old.example', serverURL: 'http://server:4545', apply: true }), /Stop Astral Server\/Desktop/)
  assert.equal(JSON.parse(await fs.readFile(path.join(directory, 'credentials.json'), 'utf8')).server_url, 'https://old.example')
})


test('production apply refuses to mutate without the native data-lock wrapper', async t => {
  const options = await fixture(t)
  await assert.rejects(migrateNetworkCommand({ ...options, apply: true }), /require native data locks/)
  await assert.rejects(fs.access(path.join(options.serverData, 'network.json')))
  const plan = await migrateNetworkCommand(options)
  assert.equal(plan.id, '001-http-main-listener')
})

test('production restore also requires the native data-lock wrapper', async t => {
  const { restoreNetworkBackup } = await import('./migrate-network.mjs')
  const options = await fixture(t)
  const result = await migrateNetwork({ ...options, apply: true })
  await assert.rejects(restoreNetworkBackup(result.backup), /require native data locks/)
  assert.ok(await fs.readFile(path.join(options.serverData, 'network.json')))
})

test('a verified native parent lock skips Windows-locked PID file reads', async t => {
  const options = await fixture(t)
  await fs.writeFile(path.join(options.serverData, 'server.lock'), 'held natively')
  const daemon = path.join(options.computersDirectory, 'local')
  await json(path.join(daemon, 'credentials.json'), { server_url: 'https://old.example', computer_id: 'id', token: 'token' })
  await fs.writeFile(path.join(daemon, 'daemon.lock'), 'held natively')
  const result = await migrateNetworkCommand({ ...options, apply: true }, { assertMaintenanceLock: async () => ({ holderPID: process.ppid, ownsLocks: true }) })
  assert.ok(result.backup)
})

test('loss of the native lock leaves recovery pending instead of mutating without ownership', async t => {
  const options = await fixture(t)
  let assertions = 0
  const assertMaintenanceLock = async () => {
    if (++assertions > 1) throw new Error('native parent exited')
    return { holderPID: process.ppid, ownsLocks: true }
  }
  await assert.rejects(migrateNetworkCommand({ ...options, apply: true }, { assertMaintenanceLock }), /Native data lock was lost/)
  await assert.rejects(fs.access(path.join(options.serverData, 'network.json')))
  const pending = JSON.parse(await fs.readFile(path.join(options.serverData, 'network-migrations', '001-http-main-listener.pending.json'), 'utf8'))
  assert.ok(await fs.readFile(path.join(pending.backup, 'manifest.json')))
})

test('failure to delete the final pending marker rolls back data and removes the completed receipt', async t => {
  const options = await fixture(t)
  let failed = false
  const removeMarker = async file => {
    if (file.endsWith('.pending.json') && !failed) { failed = true; throw Object.assign(new Error('pending unlink denied'), { code: 'EACCES' }) }
    await fs.rm(file, { force: true })
  }
  await assert.rejects(migrateNetwork({ ...options, apply: true }, { removeMarker }), /Original configuration restored/)
  await assert.rejects(fs.access(path.join(options.serverData, 'network.json')))
  await assert.rejects(fs.access(path.join(options.serverData, 'network-migrations', '001-http-main-listener.json')))
  await assert.rejects(fs.access(path.join(options.serverData, 'network-migrations', '001-http-main-listener.pending.json')))
  const retry = await migrateNetwork({ ...options, apply: true })
  assert.ok(retry.backup)
  assert.notEqual(retry.alreadyApplied, true)
})

test('receipt cleanup failure retains pending recovery and cannot report already applied', async t => {
  const { restoreNetworkBackup } = await import('./migrate-network.mjs')
  const options = await fixture(t)
  const removeMarker = async () => { throw Object.assign(new Error('marker unlink denied'), { code: 'EACCES' }) }
  await assert.rejects(migrateNetwork({ ...options, apply: true }, { removeMarker }), /Recovery needs attention/)
  await assert.rejects(fs.access(path.join(options.serverData, 'network.json')))
  assert.ok(await fs.readFile(path.join(options.serverData, 'network-migrations', '001-http-main-listener.json')))
  const pending = JSON.parse(await fs.readFile(path.join(options.serverData, 'network-migrations', '001-http-main-listener.pending.json'), 'utf8'))
  await assert.rejects(migrateNetwork({ ...options, apply: true }), /interrupted network migration requires recovery/)
  await restoreNetworkBackup(pending.backup, { assertMaintenanceLock: testMaintenanceLock })
  await assert.rejects(fs.access(path.join(options.serverData, 'network-migrations', '001-http-main-listener.json')))
})

test('an interrupted explicit restore keeps pending authoritative until receipt cleanup succeeds', async t => {
  const { restoreNetworkBackup } = await import('./migrate-network.mjs')
  const options = await fixture(t)
  const result = await migrateNetwork({ ...options, apply: true })
  await assert.rejects(restoreNetworkBackup(result.backup, {
    assertMaintenanceLock: testMaintenanceLock,
    removeMarker: async () => { throw Object.assign(new Error('restore receipt unlink denied'), { code: 'EACCES' }) },
  }), /restore receipt unlink denied/)
  await assert.rejects(fs.access(path.join(options.serverData, 'network.json')))
  assert.ok(await fs.readFile(path.join(options.serverData, 'network-migrations', '001-http-main-listener.pending.json')))
  await assert.rejects(migrateNetwork({ ...options, apply: true }), /interrupted network migration requires recovery/)
  await restoreNetworkBackup(result.backup, { assertMaintenanceLock: testMaintenanceLock })
  await assert.rejects(fs.access(path.join(options.serverData, 'network-migrations', '001-http-main-listener.json')))
  await assert.rejects(fs.access(path.join(options.serverData, 'network-migrations', '001-http-main-listener.pending.json')))
})

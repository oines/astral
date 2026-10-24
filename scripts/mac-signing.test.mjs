import assert from 'node:assert/strict'
import { mkdir, mkdtemp, readFile, rm, symlink, writeFile } from 'node:fs/promises'
import { createRequire } from 'node:module'
import { tmpdir } from 'node:os'
import path from 'node:path'
import test from 'node:test'
import { fileURLToPath } from 'node:url'
import { assertEntitlements, assertSigningMetadata, darwinNames, signBinaries, signingEnvironment, verifyApp, verifyBinaries, verifyChecksums } from './mac-signing.mjs'
import { digest, distributionNames } from './release-assets.mjs'

const environment = {
  APPLE_TEAM_ID: '6N6W6Z6739',
  MAC_SIGNING_IDENTITY: 'Developer ID Application: LIN KAIYUAN (6N6W6Z6739)',
  CSC_KEYCHAIN: '/private/tmp/astral-release.keychain-db',
}
const configuration = signingEnvironment(environment)
const details = `Authority=${configuration.identity}\nTeamIdentifier=${configuration.team}\nCodeDirectory v=20500 size=100 flags=0x10000(runtime) hashes=10+0 location=embedded\nTimestamp=Oct 24, 2026 at 12:00:00\n`

test('signing requires the intended personal Developer ID team and explicit keychain', () => {
  assert.throws(() => signingEnvironment({ ...environment, APPLE_TEAM_ID: 'OLDTEAM123' }), /must be/)
  assert.throws(() => signingEnvironment({ ...environment, MAC_SIGNING_IDENTITY: 'Apple Development: LIN KAIYUAN (6N6W6Z6739)' }), /Developer ID/)
  assert.throws(() => signingEnvironment({ ...environment, CSC_KEYCHAIN: '' }), /absolute/)
  assert.throws(() => signingEnvironment({ ...environment, MAC_SIGNING_IDENTITY: `${environment.MAC_SIGNING_IDENTITY}\n` }), /Developer ID/)
})

test('the locked electron-builder v26 schema loads the independent signed configuration', async () => {
  const require = createRequire(new URL('../apps/desktop/package.json', import.meta.url))
  const builderRequire = createRequire(require.resolve('electron-builder'))
  const { getConfig, validateConfiguration } = builderRequire('app-builder-lib/out/util/config/config.js')
  const previous = Object.fromEntries(Object.keys(environment).map(key => [key, process.env[key]]))
  try {
    Object.assign(process.env, environment)
    const config = await getConfig(fileURLToPath(new URL('../apps/desktop/', import.meta.url)), 'electron-builder.release.cjs')
    await validateConfiguration(config, { isEnabled: false })
    assert.equal(config.appId, 'com.oines.astral')
    assert.equal(config.forceCodeSigning, true)
    assert.equal(config.mac.identity, 'LIN KAIYUAN (6N6W6Z6739)')
    assert.equal(config.mac.hardenedRuntime, true)
    assert.equal(config.mac.notarize, false)
    assert.equal(typeof config.afterPack, 'function')
    assert.equal(typeof config.afterSign, 'function')
    assert.ok(config.extraResources.some(resource => resource.to === 'bin'))
    const ignored = new RegExp(config.mac.signIgnore[0])
    assert.ok(ignored.test('/release/Astral.app/Contents/Resources/bin/astral-darwin-arm64'))
    assert.ok(!ignored.test('/release/Astral.app/Contents/MacOS/Astral'))
    assert.ok(!ignored.test('/release/Astral.app/Contents/Resources/bin/astral-darwin-arm64-other'))
  } finally {
    for (const [key, value] of Object.entries(previous)) {
      if (value === undefined) delete process.env[key]
      else process.env[key] = value
    }
  }
})

test('strict metadata rejects ad-hoc or foreign signing, absent runtime and non-secure signing time', () => {
  assertSigningMetadata(details, configuration, 'Astral')
  assert.throws(() => assertSigningMetadata(details.replace(`Authority=${configuration.identity}`, 'Signature=adhoc'), configuration, 'Astral'), /Wrong Developer ID/)
  assert.throws(() => assertSigningMetadata(details.replace(configuration.team, 'OLDTEAM123'), configuration, 'Astral'), /Wrong Developer ID/)
  assert.throws(() => assertSigningMetadata(details.replace('runtime', 'none'), configuration, 'Astral'), /Runtime/)
  assert.throws(() => assertSigningMetadata(details.replace('Timestamp=', 'Signed Time='), configuration, 'Astral'), /secure/)
})

test('Electron only allows JIT and Go programs receive no entitlements', () => {
  const jit = '<plist><dict><key>com.apple.security.cs.allow-jit</key><true/></dict></plist>'
  assertEntitlements(jit, { allowJit: true, requireJit: true }, 'Electron')
  assertEntitlements('', {}, 'Go')
  assert.throws(() => assertEntitlements(jit, {}, 'Go'), /Unexpected/)
  assert.throws(() => assertEntitlements('', { allowJit: true, requireJit: true }, 'Electron'), /missing/)
  for (const key of ['com.apple.security.get-task-allow', 'com.apple.security.cs.disable-library-validation', 'com.apple.security.cs.allow-unsigned-executable-memory', 'com.apple.security.app-sandbox']) {
    assert.throws(() => assertEntitlements(`<plist><dict><key>${key}</key><true/></dict></plist>`, { allowJit: true }, 'Electron'), /Unexpected/)
  }
})

test('signing final bytes refreshes all six hashes before verification and rejects subsequent tampering', async () => {
  const directory = await mkdtemp(path.join(tmpdir(), 'astral-signing-fixture-'))
  try {
    for (const name of distributionNames) await writeFile(path.join(directory, name), `unsigned ${name}\n`)
    await writeFile(path.join(directory, 'checksums.txt'), 'stale pre-sign build manifest\n')
    const calls = []
    const command = async (executable, args) => {
      calls.push({ executable, args })
      const target = args.at(-1)
      if (args.includes('--sign')) {
        assert.ok(args.includes('--timestamp'))
        assert.ok(args.includes('--keychain'))
        assert.equal(args[args.indexOf('--options') + 1], 'runtime')
        assert.equal(args[args.indexOf('--sign') + 1], configuration.identity)
        await writeFile(target, `signed ${path.basename(target)}\n`)
      }
      if (executable.endsWith('/lipo')) return { stdout: target.endsWith('amd64') ? 'x86_64\n' : 'arm64\n', stderr: '' }
      if (args.includes('--entitlements')) return { stdout: '', stderr: '' }
      if (args.includes('--display')) return { stdout: '', stderr: details }
      return { stdout: '', stderr: '' }
    }
    await signBinaries(directory, { configuration, command })
    assert.equal(calls.filter(call => call.args.includes('--sign')).length, 2)
    assert.deepEqual(calls.slice(0, 2).map(call => path.basename(call.args.at(-1))), darwinNames)
    const manifest = await readFile(path.join(directory, 'checksums.txt'), 'utf8')
    for (const name of distributionNames) assert.ok(manifest.includes(`${await digest(path.join(directory, name))}  ${name}\n`))
    await verifyBinaries(directory, { configuration, command })
    await writeFile(path.join(directory, darwinNames[0]), 'tampered')
    await assert.rejects(verifyChecksums(directory), /checksum mismatch/)
  } finally { await rm(directory, { recursive: true, force: true }) }
})

test('bundle verification covers nested Electron helpers and every universal slice without following framework links', async () => {
  const directory = await mkdtemp(path.join(tmpdir(), 'astral-app-signing-fixture-'))
  try {
    const app = path.join(directory, 'Astral.app')
    const binaries = path.join(app, 'Contents/Resources/bin')
    const main = path.join(app, 'Contents/MacOS/Astral')
    const renderer = path.join(app, 'Contents/Frameworks/Astral Helper (Renderer).app/Contents/MacOS/Astral Helper (Renderer)')
    const framework = path.join(app, 'Contents/Frameworks/Electron Framework.framework/Versions/A/Electron Framework')
    const fixtureMachO = Buffer.from('cffaedfe00000000', 'hex')
    for (const file of [main, renderer, framework, ...distributionNames.map(name => path.join(binaries, name))]) {
      await mkdir(path.dirname(file), { recursive: true })
      await writeFile(file, file.includes('/bin/') && !darwinNames.includes(path.basename(file)) ? 'other platform' : fixtureMachO)
    }
    await symlink('A', path.join(path.dirname(path.dirname(framework)), 'Current'))
    await writeFile(path.join(binaries, 'checksums.txt'), `${(await Promise.all(distributionNames.map(async name => `${await digest(path.join(binaries, name))}  ${name}`))).join('\n')}\n`)
    const inspected = []
    let corruptFrameworkSlice = false
    const command = async (executable, args) => {
      const target = args.at(-1)
      if (executable.endsWith('/lipo')) return { stdout: target === framework ? 'x86_64 arm64\n' : target.endsWith('amd64') ? 'x86_64\n' : 'arm64\n', stderr: '' }
      if (args.includes('--entitlements')) return { stdout: darwinNames.includes(path.basename(target)) ? '' : '<plist><dict><key>com.apple.security.cs.allow-jit</key><true/></dict></plist>', stderr: '' }
      if (args.includes('--display')) {
        const architecture = args[args.indexOf('--architecture') + 1]
        inspected.push({ target, architecture })
        return { stdout: '', stderr: corruptFrameworkSlice && target === framework && architecture === 'x86_64' ? details.replace('Timestamp=', 'Signed Time=') : details }
      }
      return { stdout: '', stderr: '' }
    }
    await verifyApp(app, { configuration, command })
    for (const target of [main, renderer, framework, app]) assert.ok(inspected.some(record => record.target === target))
    assert.deepEqual(inspected.filter(record => record.target === framework).map(record => record.architecture), ['x86_64', 'arm64'])
    assert.ok(!inspected.some(record => record.target.includes('/Current/')))
    corruptFrameworkSlice = true
    await assert.rejects(verifyApp(app, { configuration, command }), /secure signing timestamp/)
  } finally { await rm(directory, { recursive: true, force: true }) }
})

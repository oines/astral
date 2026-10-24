import { execFile } from 'node:child_process'
import { lstat, open, readdir, readFile, writeFile } from 'node:fs/promises'
import path from 'node:path'
import { promisify } from 'node:util'
import { digest, distributionNames } from './release-assets.mjs'
import { isDirectInvocation } from './release-version.mjs'

const execute = promisify(execFile)
export const personalTeam = '6N6W6Z6739'
export const darwinNames = ['astral-darwin-amd64', 'astral-darwin-arm64']
const jitEntitlement = 'com.apple.security.cs.allow-jit'
const machoMagic = new Set(['feedface', 'cefaedfe', 'feedfacf', 'cffaedfe', 'cafebabe', 'bebafeca', 'cafebabf', 'bfbafeca'])

export function signingEnvironment(environment = process.env) {
  const team = environment.APPLE_TEAM_ID
  const identity = environment.MAC_SIGNING_IDENTITY
  const keychain = environment.CSC_KEYCHAIN
  if (team !== personalTeam || !identity?.startsWith('Developer ID Application: ') || !identity.endsWith(` (${team})`) || /[\r\n\0]/.test(identity)) {
    throw new Error(`APPLE_TEAM_ID must be ${personalTeam} and MAC_SIGNING_IDENTITY its full Developer ID Application certificate name.`)
  }
  if (!keychain || !path.isAbsolute(keychain) || /[\r\n\0]/.test(keychain)) throw new Error('CSC_KEYCHAIN must be an absolute keychain path.')
  return { team, identity, keychain }
}

async function macCommand(command, args) {
  if (process.platform !== 'darwin') throw new Error('Mac signing and verification require a macOS runner.')
  return execute(command, args, { encoding: 'utf8', env: { ...process.env, LC_ALL: 'C' }, timeout: 120_000, maxBuffer: 4 << 20 })
}

export function assertSigningMetadata(details, configuration, target) {
  const fields = details.split('\n')
  if (!fields.includes(`Authority=${configuration.identity}`) || !fields.includes(`TeamIdentifier=${configuration.team}`)) {
    throw new Error(`Wrong Developer ID Application identity or team: ${target}`)
  }
  if (!fields.some(line => /^CodeDirectory .*flags=.*\bruntime\b/.test(line))) throw new Error(`Hardened Runtime is missing: ${target}`)
  if (!fields.some(line => /^Timestamp=.+/.test(line) && !/^Timestamp=(none|unavailable)$/i.test(line))) throw new Error(`A secure signing timestamp is missing: ${target}`)
}

export function assertEntitlements(plist, { allowJit = false, requireJit = false } = {}, target) {
  const keys = [...plist.matchAll(/<key>([^<]+)<\/key>/g)].map(match => match[1])
  if (keys.some(key => !allowJit || key !== jitEntitlement)) throw new Error(`Unexpected signing entitlement: ${target}`)
  if (requireJit && (!keys.includes(jitEntitlement) || !/<key>com\.apple\.security\.cs\.allow-jit<\/key>\s*<true\s*\/>/.test(plist))) {
    throw new Error(`Electron JIT entitlement is missing: ${target}`)
  }
}

async function verifyCode(target, configuration, entitlements, command, architectures) {
  await command('/usr/bin/codesign', ['--verify', '--all-architectures', '--strict', '--verbose=2', target])
  // codesign display defaults to the runner's architecture. Inspect every slice
  // explicitly so a universal framework cannot hide different signing metadata.
  for (const architecture of architectures) {
    const display = await command('/usr/bin/codesign', ['--display', '--architecture', architecture, '--verbose=4', target])
    assertSigningMetadata(`${display.stdout}\n${display.stderr}`, configuration, target)
    const result = await command('/usr/bin/codesign', ['--display', '--architecture', architecture, '--entitlements', ':-', target])
    assertEntitlements(result.stdout, entitlements, target)
  }
}

async function architecturesOf(target, command) {
  const result = await command('/usr/bin/lipo', ['-archs', target])
  const architectures = result.stdout.trim().split(/\s+/)
  if (!architectures.length || architectures.some(architecture => !['x86_64', 'arm64'].includes(architecture)) || new Set(architectures).size !== architectures.length) {
    throw new Error(`Unexpected Mach-O architectures: ${target}`)
  }
  return architectures
}

async function distributionFiles(directory) {
  for (const name of distributionNames) {
    const file = path.join(directory, name)
    const info = await lstat(file)
    if (!info.isFile() || info.size === 0) throw new Error(`Missing regular distribution executable: ${file}`)
  }
}

export async function verifyChecksums(directory) {
  await distributionFiles(directory)
  if (!(await lstat(path.join(directory, 'checksums.txt'))).isFile()) throw new Error('Distribution checksums must be a regular file.')
  const text = await readFile(path.join(directory, 'checksums.txt'), 'utf8')
  const lines = text.trimEnd().split('\n')
  const records = new Map()
  for (const line of lines) {
    const entry = /^([a-f0-9]{64}) {2}(astral-[a-z0-9.-]+)$/.exec(line)
    if (!entry || !distributionNames.includes(entry[2]) || records.has(entry[2])) throw new Error('Distribution checksums must contain each of the six executable names exactly once.')
    records.set(entry[2], entry[1])
  }
  if (records.size !== distributionNames.length) throw new Error('Distribution checksum inventory is incomplete.')
  for (const name of distributionNames) {
    if (records.get(name) !== await digest(path.join(directory, name))) throw new Error(`Distribution checksum mismatch: ${name}`)
  }
}

export async function verifyBinaries(directory, { configuration = signingEnvironment(), command = macCommand } = {}) {
  await verifyChecksums(directory)
  for (const name of darwinNames) {
    const target = path.join(directory, name)
    const expectedArch = name.endsWith('amd64') ? 'x86_64' : 'arm64'
    const architectures = await architecturesOf(target, command)
    if (architectures.length !== 1 || architectures[0] !== expectedArch) throw new Error(`Unexpected Darwin architecture: ${target}`)
    await verifyCode(target, configuration, { allowJit: false }, command, architectures)
  }
}

export async function signBinaries(directory, { configuration = signingEnvironment(), command = macCommand } = {}) {
  await distributionFiles(directory)
  for (const name of darwinNames) {
    await command('/usr/bin/codesign', ['--force', '--sign', configuration.identity, '--keychain', configuration.keychain, '--options', 'runtime', '--timestamp', path.join(directory, name)])
  }
  // Every consumer receives the exact same already-signed Go bytes. Refresh the
  // complete manifest before Electron seals it into the application resources.
  const sums = []
  for (const name of distributionNames) sums.push(`${await digest(path.join(directory, name))}  ${name}`)
  await writeFile(path.join(directory, 'checksums.txt'), `${sums.join('\n')}\n`)
  await verifyBinaries(directory, { configuration, command })
}

async function isMachO(file) {
  const handle = await open(file, 'r')
  try {
    const buffer = Buffer.alloc(4)
    const { bytesRead } = await handle.read(buffer, 0, 4, 0)
    return bytesRead === 4 && machoMagic.has(buffer.toString('hex'))
  } finally { await handle.close() }
}

async function enclosedMachO(directory) {
  const files = []
  for (const entry of await readdir(directory, { withFileTypes: true })) {
    const target = path.join(directory, entry.name)
    // Framework symlinks refer to already visited files; never follow links out
    // of the signed bundle or count an executable twice.
    if (entry.isSymbolicLink()) continue
    if (entry.isDirectory()) files.push(...await enclosedMachO(target))
    else if (entry.isFile() && await isMachO(target)) files.push(target)
  }
  return files
}

export async function verifyApp(app, { configuration = signingEnvironment(), command = macCommand } = {}) {
  if (!app.endsWith('.app') || !(await lstat(app)).isDirectory()) throw new Error('Pass the unpacked Astral.app bundle.')
  const contents = path.join(app, 'Contents')
  const binaries = path.join(contents, 'Resources/bin')
  await verifyBinaries(binaries, { configuration, command })
  const files = await enclosedMachO(contents)
  const main = path.join(contents, 'MacOS/Astral')
  if (!files.includes(main) || !files.some(file => file.includes('Astral Helper (Renderer).app/Contents/MacOS/'))) {
    throw new Error('The signed bundle is missing the Astral executable or Electron renderer helper.')
  }
  let mainArchitectures
  for (const target of files) {
    const goBinary = darwinNames.some(name => target === path.join(binaries, name))
    const architectures = await architecturesOf(target, command)
    if (target === main) mainArchitectures = architectures
    await verifyCode(target, configuration, { allowJit: !goBinary, requireJit: target === main || target.includes('Astral Helper (Renderer).app/Contents/MacOS/') }, command, architectures)
  }
  await verifyCode(app, configuration, { allowJit: true, requireJit: true }, command, mainArchitectures)
  await command('/usr/bin/codesign', ['--verify', '--all-architectures', '--deep', '--strict', '--verbose=2', app])
  console.log(`Verified Developer ID identity, Hardened Runtime, secure timestamps, entitlements and resource checksums for ${files.length} Mach-O files in ${app}.`)
}

async function main() {
  const [operation, target, ...extra] = process.argv.slice(2)
  if (!target || extra.length) throw new Error('Usage: mac-signing.mjs sign-binaries DIST | verify-binaries DIST | verify-app APP')
  if (operation === 'sign-binaries') await signBinaries(path.resolve(target))
  else if (operation === 'verify-binaries') await verifyBinaries(path.resolve(target))
  else if (operation === 'verify-app') await verifyApp(path.resolve(target))
  else throw new Error('Unknown Mac signing operation.')
}

if (await isDirectInvocation(import.meta.url)) {
  main().catch(error => { console.error(error.message); process.exitCode = 1 })
}

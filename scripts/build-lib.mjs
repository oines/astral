import { cp, mkdir, readFile, readdir, rm } from 'node:fs/promises'
import { spawnSync } from 'node:child_process'
import { fileURLToPath } from 'node:url'
import path from 'node:path'
import { buildNotices } from './build-notices.mjs'
import { readReleaseVersion } from './release-version.mjs'

export const root = fileURLToPath(new URL('../', import.meta.url))
export function run(command, args, options = {}) {
  // pnpm is normally a .cmd shim on Windows. Inside a pnpm script, execute
  // the package manager's JavaScript entry point without involving cmd.exe.
  const packageManager = process.platform === 'win32' && command === 'pnpm' ? process.env.npm_execpath : undefined
  if (process.platform === 'win32' && command === 'pnpm' && !packageManager) throw new Error('Run this build through pnpm (for example, pnpm build:distribution).')
  const result = spawnSync(packageManager ? process.execPath : command, packageManager ? [packageManager, ...args] : args, { cwd: root, stdio: 'inherit', ...options })
  if (result.error) throw result.error
  if (result.status !== 0) throw new Error(`${command} exited with status ${result.status}`)
}
export async function embedWeb() {
  const source = path.join(root, 'apps/web/dist')
  await readFile(path.join(source, 'index.html'))
  await buildNotices(root, path.join(source, 'THIRD_PARTY_NOTICES.txt'))
  await cp(path.join(root, 'LICENSE'), path.join(source, 'LICENSE.txt'))
  await mkdir(path.join(root, 'dist/bin'), { recursive: true })
  await cp(path.join(source, 'THIRD_PARTY_NOTICES.txt'), path.join(root, 'dist/bin/THIRD_PARTY_NOTICES.txt'))
  await cp(path.join(root, 'LICENSE'), path.join(root, 'dist/bin/LICENSE.txt'))
  const target = path.join(root, 'internal/platform/web')
  await mkdir(target, { recursive: true })
  for (const name of await readdir(target)) {
    if (name !== 'README.txt') await rm(path.join(target, name), { recursive: true, force: true })
  }
  await cp(source, target, { recursive: true })
  // A file copied into web/ can still be excluded by go:embed (for example,
  // Vite's _plugin chunks). Check Go's selection before building any binaries.
  const selection = spawnSync('go', ['list', '-find', '-json=EmbedFiles', './internal/platform'], { cwd: root, encoding: 'utf8' })
  if (selection.error) throw selection.error
  if (selection.status !== 0) throw new Error(`Cannot inspect embedded Web assets: ${selection.stderr}`)
  const embedded = new Set(JSON.parse(selection.stdout).EmbedFiles)
  async function checkDirectory(directory, relative = 'web') {
    for (const entry of await readdir(directory, { withFileTypes: true })) {
      const name = `${relative}/${entry.name}`
      if (entry.isDirectory()) await checkDirectory(path.join(directory, entry.name), name)
      else if (entry.isFile() && !embedded.has(name)) throw new Error(`Required Web asset is missing from Go embeds: ${name}`)
    }
  }
  await checkDirectory(source)
}
export async function buildServer(system, arch, { buildRoot = root, environment = process.env } = {}) {
  const version = await readReleaseVersion(buildRoot, environment)
  const output = path.join(buildRoot, 'dist/bin', `astral-${system}-${arch}${system === 'windows' ? '.exe' : ''}`)
  await mkdir(path.dirname(output), { recursive: true })
  run('go', ['build', '-trimpath', '-ldflags', `-s -w -X github.com/oines/astral/internal/cli.Version=${version}`, '-o', output, './cmd/astral'], {
    cwd: buildRoot,
    env: { ...environment, GOWORK: environment.GOWORK || 'auto', CGO_ENABLED: '0', GOOS: system, GOARCH: arch },
  })
  return output
}

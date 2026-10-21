import { cp, mkdir, readFile, readdir, rm } from 'node:fs/promises'
import { spawnSync } from 'node:child_process'
import { fileURLToPath } from 'node:url'
import path from 'node:path'
import { buildNotices } from './build-notices.mjs'

export const root = fileURLToPath(new URL('../', import.meta.url))
export function run(command, args, options = {}) {
  const result = spawnSync(command, args, { cwd: root, stdio: 'inherit', ...options })
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
}
export async function buildServer(system, arch) {
  const manifest = JSON.parse(await readFile(path.join(root, 'package.json'), 'utf8'))
  const output = path.join(root, 'dist/bin', `astral-${system}-${arch}`)
  await mkdir(path.dirname(output), { recursive: true })
  run('go', ['build', '-trimpath', '-ldflags', `-s -w -X github.com/oines/astral/internal/cli.Version=${manifest.version}`, '-o', output, './cmd/astral'], {
    env: { ...process.env, GOWORK: process.env.GOWORK || 'auto', CGO_ENABLED: '0', GOOS: system, GOARCH: arch },
  })
  return output
}

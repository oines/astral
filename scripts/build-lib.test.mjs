import assert from 'node:assert/strict'
import { execFileSync } from 'node:child_process'
import { mkdir, mkdtemp, rm, writeFile } from 'node:fs/promises'
import os from 'node:os'
import path from 'node:path'
import test from 'node:test'
import { buildServer } from './build-lib.mjs'

test('a real native Go build reports the frozen version instead of the source baseline', async t => {
  const buildRoot = await mkdtemp(path.join(os.tmpdir(), 'astral-build-version-'))
  t.after(() => rm(buildRoot, { recursive: true, force: true }))
  for (const name of ['apps/desktop', 'internal/cli', 'cmd/astral']) await mkdir(path.join(buildRoot, name), { recursive: true })
  await writeFile(path.join(buildRoot, 'go.mod'), 'module github.com/oines/astral\n\ngo 1.26.0\n')
  for (const name of ['package.json', 'apps/desktop/package.json']) await writeFile(path.join(buildRoot, name), JSON.stringify({ version: '0.1.13' }))
  await writeFile(path.join(buildRoot, 'internal/cli/version.go'), 'package cli\nvar Version = "development"\n')
  await writeFile(path.join(buildRoot, 'cmd/astral/main.go'), 'package main\nimport("fmt"; "github.com/oines/astral/internal/cli")\nfunc main(){fmt.Print(cli.Version)}\n')
  const system = process.platform === 'win32' ? 'windows' : process.platform
  const arch = process.arch === 'x64' ? 'amd64' : process.arch
  const binary = await buildServer(system, arch, { buildRoot, environment: { ...process.env, GOWORK: 'off', ASTRAL_RELEASE_VERSION: '0.1.48' } })
  assert.equal(execFileSync(binary, { encoding: 'utf8', timeout: 10_000 }), '0.1.48')
})

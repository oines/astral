import { mkdtemp, mkdir, readFile, realpath, rm, symlink, writeFile } from 'node:fs/promises'
import { tmpdir } from 'node:os'
import path from 'node:path'
import { afterEach, describe, expect, it } from 'vitest'
import type { DesktopUpdateState } from '../shared/updates'
import { DesktopUpdateCache } from './update-cache'

const roots: string[] = []
afterEach(async () => { for (const dir of roots.splice(0)) await rm(dir, { recursive: true, force: true }) })

async function fixture() {
  const parent = await realpath(await mkdtemp(path.join(tmpdir(), 'astral-update-cache-')))
  roots.push(parent)
  const directory = path.join(parent, 'astral-updater')
  const pending = path.join(directory, 'pending')
  await mkdir(pending, { recursive: true })
  let state: DesktopUpdateState = { phase: 'idle', currentVersion: '0.10.2' }
  const cache = new DesktopUpdateCache({ directory, state: () => state })
  return { parent, pending, cache, setState(value: DesktopUpdateState) { state = value } }
}

describe('Desktop update packages', () => {
  it('measures and removes all old and current packages while retaining newer packages and unrelated files', async () => {
    const { cache, pending } = await fixture()
    const packageName = (version: string) => `Astral-${version}-mac-arm64.zip`
    const retained = [packageName('0.10.3'), 'Astral-0.1.0-linux-arm64.zip', 'update-info.json', 'unknown.zip']
    for (const name of [...retained, 'Astral-0.1.0-mac-x64.zip', packageName('0.9.10'), packageName('0.10.2')]) await writeFile(path.join(pending, name), 'data')
    expect(await cache.inventory()).toEqual({ total_bytes: 16, cleanable_bytes: 12 })
    expect(await cache.clean()).toEqual({ removed_bytes: 12 })
    expect(await cache.clean()).toEqual({ removed_bytes: 0 })
    for (const name of retained) expect(await readFile(path.join(pending, name), 'utf8')).toBe('data')
  })
  it('skips packages during updating and refuses symlink sources', async () => {
    const { cache, parent, pending, setState } = await fixture()
    const old = path.join(pending, 'Astral-0.1.0-mac-arm64.zip')
    await writeFile(old, 'old')
    setState({ currentVersion: '0.10.2', phase: 'installing' })
    expect(await cache.inventory()).toEqual({ total_bytes: 0, cleanable_bytes: 0 })
    expect(await cache.clean()).toEqual({ removed_bytes: 0 })
    expect(await readFile(old, 'utf8')).toBe('old')
    if (process.platform === 'win32') return
    const outside = path.join(parent, 'user-file')
    await writeFile(outside, 'keep')
    await rm(old)
    await symlink(outside, old)
    setState({ currentVersion: '0.10.2', phase: 'idle' })
    expect(await cache.inventory()).toEqual({ total_bytes: 0, cleanable_bytes: 0 })
    expect(await cache.clean()).toEqual({ removed_bytes: 0 })
    expect(await readFile(outside, 'utf8')).toBe('keep')
  })
})

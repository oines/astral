import { createHash } from 'node:crypto'
import { mkdtemp, mkdir, readFile, realpath, rm, symlink, unlink, writeFile } from 'node:fs/promises'
import { tmpdir } from 'node:os'
import path from 'node:path'
import { afterEach, describe, expect, it, vi } from 'vitest'
import type { DesktopUpdateState } from '../shared/updates'
import { DesktopUpdateCache } from './update-cache'

vi.mock('node:fs/promises', async original => {
  const actual = await original<typeof import('node:fs/promises')>()
  return { ...actual, unlink: vi.fn(actual.unlink) }
})
const cleanups: string[] = []
afterEach(async () => {
  vi.mocked(unlink).mockRestore()
  for (const directory of cleanups.splice(0)) await rm(directory, { recursive: true, force: true })
})
async function fixture(platform: NodeJS.Platform = 'darwin', cachedVersion = '0.1.6') {
  const directory = await realpath(await mkdtemp(path.join(tmpdir(), 'astral-update-cache-')))
  cleanups.push(directory)
  const root = path.join(directory, 'astral-updater')
  const pending = path.join(root, 'pending')
  await mkdir(pending, { recursive: true })
  const configuration = path.join(directory, 'app-update.yml')
  await writeFile(configuration, await readFile(new URL('../../resources/app-update.yml', import.meta.url), 'utf8'))
  const [os, extension] = platform === 'darwin' ? ['mac', 'zip'] : platform === 'win32' ? ['win', 'exe'] : ['linux', 'AppImage']
  const fileName = `Astral-${cachedVersion}-${os}-arm64.${extension}`
  const installer = Buffer.from('verified updater download')
  const metadata = JSON.stringify({ fileName, sha512: createHash('sha512').update(installer).digest('base64'), isAdminRightsRequired: false })
  await writeFile(path.join(pending, fileName), installer)
  await writeFile(path.join(pending, 'update-info.json'), metadata)
  let state: DesktopUpdateState = { phase: 'idle', currentVersion: '0.1.7' }
  let recovery = false
  const config = { directory: root, configuration, journalDirectory: path.join(directory, 'cleanup'), platform, architecture: 'arm64', state: () => state, recoveryBlocked: () => recovery }
  return { cache: new DesktopUpdateCache(config), config, root, pending, fileName, metadata, installer,
    setState: (value: DesktopUpdateState) => { state = value }, setRecovery: () => { recovery = true } }
}

describe('Desktop updater cache ownership and cleanup', () => {
  it.each(['darwin', 'win32', 'linux'] as const)('removes only metadata-backed stale packages on %s and replays the durable result', async platform => {
    const f = await fixture(platform)
    await writeFile(path.join(f.pending, 'unrelated.txt'), 'keep')
    const [item] = await f.cache.inventory()
    expect(item).toMatchObject({ kind: 'update_cache', name: f.fileName, protected: false, bytes: f.installer.length + Buffer.byteLength(f.metadata) })
    expect(item!.token).toMatch(/^desktop:[a-f0-9]{64}$/)
    expect(JSON.stringify(item)).not.toContain(f.root)
    const bytes = await f.cache.clean('cleanup_operation', item!.token)
    expect(bytes).toBe(item!.bytes)
    await expect(readFile(path.join(f.pending, f.fileName))).rejects.toMatchObject({ code: 'ENOENT' })
    await expect(readFile(path.join(f.pending, 'update-info.json'))).rejects.toMatchObject({ code: 'ENOENT' })
    expect(await readFile(path.join(f.pending, 'unrelated.txt'), 'utf8')).toBe('keep')
    await writeFile(path.join(f.pending, f.fileName), 'a replacement file must survive a replay')
    expect(await new DesktopUpdateCache(f.config).clean('cleanup_operation', item!.token)).toBe(bytes)
    expect(await readFile(path.join(f.pending, f.fileName), 'utf8')).toContain('must survive')
    await rm(f.root, { recursive: true })
    expect(await new DesktopUpdateCache(f.config).clean('cleanup_operation', item!.token)).toBe(bytes)
  })

  it('protects a newer pending update, active work and installer recovery dependencies', async () => {
    const newer = await fixture('darwin', '0.1.8')
    expect((await newer.cache.inventory())[0]).toMatchObject({ protected: true, reason: 'update_pending' })
    const f = await fixture()
    const [selected] = await f.cache.inventory()
    f.setState({ phase: 'downloading', currentVersion: '0.1.7' })
    expect((await f.cache.inventory())[0]).toMatchObject({ protected: true, reason: 'update_running' })
    await expect(f.cache.clean('operation', selected!.token)).rejects.toThrow('needed by an update')
    f.setState({ phase: 'error', currentVersion: '0.1.7', operationState: 'failed', operationVersion: '0.1.6' })
    expect((await f.cache.inventory())[0]).toMatchObject({ protected: true, reason: 'update_recovery' })
    f.setState({ phase: 'idle', currentVersion: '0.1.7' })
    f.setRecovery()
    expect((await f.cache.inventory())[0]).toMatchObject({ protected: true, reason: 'update_recovery' })
    expect(await readFile(path.join(f.pending, f.fileName))).toEqual(f.installer)
  })

  it('does not adopt unknown metadata, arbitrary paths, another architecture or a different cache configuration', async () => {
    const f = await fixture()
    for (const fileName of ['../../project.txt', 'other-app.zip', 'Astral-0.1.6-mac-x64.zip']) {
      await writeFile(path.join(f.pending, 'update-info.json'), JSON.stringify({ fileName, sha512: createHash('sha512').update('x').digest('base64') }))
      expect(await f.cache.inventory()).toEqual([])
    }
    await writeFile(path.join(f.pending, 'update-info.json'), f.metadata)
    await writeFile(f.config.configuration, 'provider: generic\nupdaterCacheDirName: another-app\nurl: https://example.com/\n')
    expect(await f.cache.inventory()).toEqual([])
  })

  it('rejects changed selections and symlinks without removing their targets', async () => {
    const f = await fixture()
    const [selected] = await f.cache.inventory()
    await writeFile(path.join(f.pending, f.fileName), 'changed package')
    await expect(f.cache.clean('operation', selected!.token)).rejects.toThrow('changed')
    const outside = path.join(path.dirname(f.root), 'keep.zip')
    await writeFile(outside, 'keep')
    await rm(path.join(f.pending, f.fileName))
    await symlink(outside, path.join(f.pending, f.fileName))
    expect(await f.cache.inventory()).toEqual([])
    await expect(f.cache.clean('operation', selected!.token)).rejects.toThrow()
    expect(await readFile(outside, 'utf8')).toBe('keep')
  })

  it('reconciles an unlink interrupted before its progress record without deleting a replacement', async () => {
    const f = await fixture()
    const [selected] = await f.cache.inventory()
    const actual = await vi.importActual<typeof import('node:fs/promises')>('node:fs/promises')
    vi.mocked(unlink).mockImplementationOnce(async file => {
      await actual.unlink(file)
      throw new Error('interrupted after unlink')
    })
    await expect(f.cache.clean('operation', selected!.token)).rejects.toThrow('interrupted')
    const restarted = new DesktopUpdateCache(f.config)
    expect(await restarted.clean('operation', selected!.token)).toBe(Buffer.byteLength(f.metadata))
    expect(await restarted.inventory()).toEqual([])
    expect(await restarted.clean('operation', selected!.token)).toBe(Buffer.byteLength(f.metadata))
  })
})

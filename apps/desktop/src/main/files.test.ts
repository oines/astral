import { mkdtemp, mkdir, realpath, rm, writeFile } from 'node:fs/promises'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import type { IpcMainInvokeEvent } from 'electron'
import { DesktopFiles, desktopFileTarget, registerDesktopFileHandlers } from './files'

let root = ''
let localDirectory = ''
let report = ''
let snapshot = ''
let computer: Record<string, unknown>
let alive = true
const shell = { showItemInFolder: vi.fn(), openPath: vi.fn(async () => '') }
let request: ReturnType<typeof vi.fn<(path: string, method?: 'GET' | 'POST') => Promise<unknown>>>
let files: DesktopFiles
const target = { computerID: 'managed-local', sessionID: 'session', fileID: 'file-reference' }

beforeEach(async () => {
  vi.clearAllMocks()
  alive = true
  root = await realpath(await mkdtemp(join(tmpdir(), 'astral-native-files-')))
  localDirectory = join(root, 'computers', 'local')
  await mkdir(localDirectory, { recursive: true })
  report = join(root, 'report.md')
  snapshot = join(localDirectory, '.astral', 'versions', 'snapshot.md')
  await mkdir(join(localDirectory, '.astral', 'versions'), { recursive: true })
  await writeFile(report, 'Current file')
  await writeFile(snapshot, 'Preserved snapshot')
  await writeFile(join(localDirectory, 'credentials.json'), JSON.stringify({ computer_id: 'managed-local', server_url: 'http://127.0.0.1:4545', token: 'never-leave-main' }))
  computer = { id: 'managed-local', local: true, kind: 'local', revoked: false, online: true, boot_id: 'local-boot', root: localDirectory }
  request = vi.fn(async (path, method = 'GET') => {
    if (path === '/api/v1/computers') return [computer]
    if (path.startsWith('/api/v1/files/file-reference?')) return { computer_id: 'managed-local', path: snapshot }
    if (path.startsWith('/api/v1/computers/managed-local/files/reference?') && method === 'POST') return { file: { computer_id: 'managed-local', path: report } }
    if (path.startsWith('/api/v1/computers/managed-local/files/directory?')) return { computer_id: 'managed-local', path: root }
    throw new Error('Unexpected source request')
  })
  files = new DesktopFiles({ localDirectory, serverURL: 'http://127.0.0.1:4545', alive: () => alive, request, shell })
})
afterEach(async () => { await rm(root, { recursive: true, force: true }) })

describe('Desktop file locations', () => {
  it('selects the authoritative reference snapshot instead of a renderer path', async () => {
    expect(await files.canReveal({ ...target, path: report })).toBe(true)
    await files.reveal({ ...target, path: report })
    expect(shell.showItemInFolder).toHaveBeenCalledExactlyOnceWith(snapshot)
    expect(shell.openPath).not.toHaveBeenCalled()
    expect(request.mock.calls.some(([path]) => path.includes('session_id=session'))).toBe(true)
  })
  it('opens only a verified directory through the OS directory opener', async () => {
    const directory = { computerID: 'managed-local', sessionID: 'session', path: join(root, 'requested-alias'), directory: true }
    await files.reveal(directory)
    expect(shell.openPath).toHaveBeenCalledExactlyOnceWith(root)
    expect(shell.showItemInFolder).not.toHaveBeenCalled()
    shell.openPath.mockResolvedValueOnce('No file manager is available.')
    await expect(files.reveal(directory)).rejects.toThrow('No file manager')
  })
  it('keeps menu checks read-only and resolves a directory entry before selecting it', async () => {
    const entry = { computerID: 'managed-local', sessionID: 'session', path: report, directory: false }
    expect(await files.canReveal(entry)).toBe(true)
    expect(request.mock.calls.every(([, method]) => method !== 'POST')).toBe(true)
    await files.reveal(entry)
    expect(request.mock.calls.some(([path, method]) => path.includes('/files/reference?') && method === 'POST')).toBe(true)
    expect(shell.showItemInFolder).toHaveBeenCalledExactlyOnceWith(report)
  })
  it('rejects remote and container identities even with a matching OS or hostname', async () => {
    for (const patch of [{ id: 'remote' }, { local: false }, { kind: 'container' }, { container: { name: 'same-host' } }, { root: root }, { revoked: true }, { online: false }]) {
      const original = computer
      computer = { ...computer, os: process.platform, hostname: 'this-machine', ...patch }
      expect(await files.canReveal(target)).toBe(false)
      await expect(files.reveal(target)).rejects.toThrow()
      computer = original
    }
    expect(shell.showItemInFolder).not.toHaveBeenCalled()
    expect(shell.openPath).not.toHaveBeenCalled()
  })
  it('rejects stale registration, cross-source responses and changed native identities', async () => {
    request.mockImplementationOnce(async () => [computer]).mockImplementationOnce(async () => ({ computer_id: 'remote', path: report }))
    expect(await files.canReveal(target)).toBe(false)
    const healthyRequest = request.getMockImplementation()!
    let identityChecks = 0
    request.mockImplementation(async (path, method) => path === '/api/v1/computers' && ++identityChecks > 1 ? [{ ...computer, boot_id: 'replacement-boot' }] : healthyRequest(path, method))
    await expect(files.reveal(target)).rejects.toThrow('changed')
    request.mockImplementation(healthyRequest)
    await writeFile(join(localDirectory, 'credentials.json'), JSON.stringify({ computer_id: 'old-local', server_url: 'http://127.0.0.1:4545' }))
    expect(await files.canReveal(target)).toBe(false)
    expect(shell.showItemInFolder).not.toHaveBeenCalled()
  })
  it('does not invoke native operations after permission rejection or Server exit', async () => {
    request.mockImplementationOnce(async () => [computer]).mockRejectedValueOnce(new Error('Computer access was revoked.'))
    expect(await files.canReveal(target)).toBe(false)
    alive = false
    await expect(files.reveal(target)).rejects.toThrow('not running')
    expect(shell.showItemInFolder).not.toHaveBeenCalled()
  })
  it('rejects a historical file reference when current Bot access to its Computer is forbidden', async () => {
    const healthyRequest = request.getMockImplementation()!
    const denied = Object.assign(new Error('This Bot no longer has access to the Computer.'), { status: 403 })
    request.mockImplementation(async (path, method) => {
      if (path.startsWith('/api/v1/computers/managed-local/files/directory?')) {
        const query = new URL(path, 'http://127.0.0.1:4545').searchParams
        expect(query.get('session_id')).toBe(target.sessionID)
        expect(query.get('path')).toBe(snapshot)
        expect(query.get('limit')).toBe('1')
        throw denied
      }
      return healthyRequest(path, method)
    })
    expect(await files.canReveal(target)).toBe(false)
    await expect(files.reveal(target)).rejects.toBe(denied)
    expect(request.mock.calls.filter(([path]) => path.startsWith('/api/v1/files/file-reference?'))).toHaveLength(2)
    expect(shell.showItemInFolder).not.toHaveBeenCalled()
    expect(shell.openPath).not.toHaveBeenCalled()
  })
  it('validates IPC targets without accepting URLs, relative paths or extra arguments', () => {
    for (const value of [null, [], { ...target, sessionID: '' }, { ...target, executable: 'file-manager' }, { computerID: 'managed-local', sessionID: 'session', path: 'file:///tmp/report', directory: false }, { computerID: 'managed-local', sessionID: 'session', path: 'relative.md', directory: false }, { computerID: 'managed-local', sessionID: 'session', path: report }]) expect(() => desktopFileTarget(value)).toThrow()
  })
  it('checks the native sender before either capability or location handling', async () => {
    const listeners = new Map<string, (event: IpcMainInvokeEvent, target: unknown) => unknown>()
    const sender = vi.fn(() => { throw new Error('Untrusted frame') })
    registerDesktopFileHandlers({ handle: (channel, listener) => { listeners.set(channel, listener) } }, sender, files)
    for (const channel of ['astral:files-can-reveal', 'astral:files-reveal']) expect(() => listeners.get(channel)!({} as IpcMainInvokeEvent, target)).toThrow('Untrusted frame')
    expect(request).not.toHaveBeenCalled()
    expect(shell.showItemInFolder).not.toHaveBeenCalled()
  })
})

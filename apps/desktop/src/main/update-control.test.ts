import { EventEmitter } from 'node:events'
import { request as httpRequest } from 'node:http'
import { mkdtemp, readFile, rm, writeFile } from 'node:fs/promises'
import { tmpdir } from 'node:os'
import path from 'node:path'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { DesktopUpdates } from './updates'
import { DesktopUpdateControl, startDesktopUpdateBridge } from './update-control'
import { DesktopUpdateCache } from './update-cache'
import { managedHandshake } from './service'

class Engine extends EventEmitter {
  autoDownload = true
  autoInstallOnAppQuit = true
  allowPrerelease = false
  allowDowngrade = false
  disableDifferentialDownload = true
  checkForUpdates = vi.fn(async () => ({ isUpdateAvailable: true, updateInfo: { version: '0.1.3' } }))
  downloadUpdate = vi.fn(async () => {})
  quitAndInstall = vi.fn()
}
const cleanups: (() => Promise<unknown>)[] = []
afterEach(async () => { for (const cleanup of cleanups.splice(0).reverse()) await cleanup() })
async function fixture(version = '0.1.2', journal?: string) {
  if (!journal) {
    const directory = await mkdtemp(path.join(tmpdir(), 'astral-update-control-'))
    cleanups.push(() => rm(directory, { recursive: true, force: true }))
    journal = path.join(directory, 'operation.json')
  }
  const engine = new Engine()
  const stop = vi.fn(async () => { await control.beforeInstall() })
  const updates = new DesktopUpdates(engine, version, stop)
  const control = new DesktopUpdateControl(updates, journal)
  await control.initialize()
  return { engine, updates, control, stop, journal }
}
describe('private Desktop update control', () => {
  it('shares a queued operation across HTTP and native calls, persisting the target before shutdown', async () => {
    const f = await fixture()
    let finish!: () => void
    f.engine.downloadUpdate.mockReturnValueOnce(new Promise<void>(resolve => { finish = resolve }))
    const queued = f.control.apply()
    expect(queued.operationId).toBeTruthy()
    expect(queued.operationState).toBe('running')
    expect(f.control.apply().operationId).toBe(queued.operationId)
    const native = f.control.update()
    await vi.waitFor(() => expect(f.control.state().phase).toBe('downloading'))
    expect(f.stop).not.toHaveBeenCalled()
    finish()
    await native
    const record = JSON.parse(await readFile(f.journal, 'utf8'))
    expect(record).toMatchObject({ id: queued.operationId, target: '0.1.3', state: 'running' })
    expect(f.engine.downloadUpdate).toHaveBeenCalledOnce()
    expect(f.engine.quitAndInstall).toHaveBeenCalledOnce()
    expect(f.stop).toHaveBeenCalledOnce()
    const restarted = await fixture('0.1.3', f.journal)
    expect(restarted.control.state()).toMatchObject({ phase: 'idle', currentVersion: '0.1.3', operationId: queued.operationId, operationVersion: '0.1.3', operationState: 'complete' })
    await restarted.control.check()
    expect(restarted.control.state()).toMatchObject({ operationId: queued.operationId, operationVersion: '0.1.3', operationState: 'complete' })
  })
  it('retains a historical completed operation without claiming an unchecked installation is latest', async () => {
    const f = await fixture()
    const id = '12345678-1234-1234-1234-123456789abc'
    await writeFile(f.journal, JSON.stringify({ id, from: '0.1.1', target: '0.1.2', state: 'complete' }))
    const restarted = await fixture('0.1.1', f.journal)
    expect(restarted.control.state()).toMatchObject({ phase: 'idle', currentVersion: '0.1.1', operationId: id, operationVersion: '0.1.2', operationState: 'complete' })
    expect(restarted.engine.checkForUpdates).not.toHaveBeenCalled()
    await restarted.control.check()
    expect(restarted.control.state()).toMatchObject({ phase: 'available', currentVersion: '0.1.1', version: '0.1.3', operationId: id, operationVersion: '0.1.2', operationState: 'complete' })
  })
  it('does not report a changed but unexpected installed version as success or erase it by a timer check', async () => {
    const f = await fixture()
    f.control.apply()
    await f.control.update()
    const restarted = await fixture('0.1.4', f.journal)
    expect(restarted.control.state()).toMatchObject({ phase: 'error', operationVersion: '0.1.3', operationState: 'failed', retry: 'update' })
    await restarted.control.check(true)
    expect(restarted.engine.checkForUpdates).not.toHaveBeenCalled()
  })
  it('retains download failure as the same operation without stopping the Server', async () => {
    const f = await fixture()
    f.engine.downloadUpdate.mockRejectedValueOnce(new Error('checksum mismatch'))
    const queued = f.control.apply()
    await expect(f.control.update()).rejects.toThrow('checksum mismatch')
    expect(f.control.state()).toMatchObject({ phase: 'error', error: 'checksum mismatch', operationId: queued.operationId, operationState: 'failed' })
    expect(f.stop).not.toHaveBeenCalled()
    expect(f.engine.quitAndInstall).not.toHaveBeenCalled()
  })
  it('fails safely on a corrupt operation journal while keeping the application usable', async () => {
    const f = await fixture()
    await writeFile(f.journal, '{broken')
    const restarted = await fixture('0.1.2', f.journal)
    expect(restarted.control.state()).toMatchObject({ phase: 'error', retry: 'check' })
  })
  it('accepts only authenticated empty fixed-route requests and rotates credentials per bridge', async () => {
    const state = { phase: 'available' as const, currentVersion: '0.1.2', version: '0.1.3' }
    const control = { state: vi.fn(() => state), check: vi.fn(async () => state), apply: vi.fn(() => ({ ...state, operationId: 'operation' })) }
    const bridge = await startDesktopUpdateBridge(control)
    const second = await startDesktopUpdateBridge(control)
    cleanups.push(bridge.close, second.close)
    expect(bridge.token).not.toBe(second.token)
    const headers = { Authorization: `Bearer ${bridge.token}` }
    expect((await fetch(`${bridge.url}/state`)).status).toBe(403)
    expect((await fetch(`${bridge.url}/state`, { headers: { ...headers, Origin: 'http://127.0.0.1' } })).status).toBe(403)
    const foreignHost = await new Promise((resolve, reject) => {
      const request = httpRequest(`${bridge.url}/state`, { headers: { ...headers, Host: 'evil.example' } }, response => { response.resume(); response.on('end', () => resolve(response.statusCode)) })
      request.on('error', reject); request.end()
    })
    expect(foreignHost).toBe(403)
    expect((await fetch(`${bridge.url}/state?path=/apply`, { headers })).status).toBe(404)
    expect((await fetch(`${bridge.url}/apply`, { method: 'POST', headers, body: '{}' })).status).toBe(400)
    expect(control.apply).not.toHaveBeenCalled()
    expect(await (await fetch(`${bridge.url}/state`, { headers })).json()).toMatchObject({ manager: 'desktop', current_version: '0.1.2' })
    expect((await fetch(`${bridge.url}/check`, { method: 'POST', headers })).status).toBe(200)
    expect(await (await fetch(`${bridge.url}/apply`, { method: 'POST', headers })).json()).toMatchObject({ operation_id: 'operation' })
    expect(control.apply).toHaveBeenCalledOnce()
  })
  it('sends independent update credentials only inside the private managed stdin record', () => {
    expect(JSON.parse(managedHandshake('a'.repeat(64), { url: 'http://127.0.0.1:12345', token: 'b'.repeat(64) }))).toEqual({ desktop_token: 'a'.repeat(64), desktop_update_url: 'http://127.0.0.1:12345', desktop_update_token: 'b'.repeat(64) })
    expect(JSON.parse(managedHandshake('a'.repeat(64)))).not.toHaveProperty('desktop_update_token')
  })

  it('relays only bounded token-based cleanup requests over the authenticated private bridge', async () => {
    const f = await fixture()
    const cache = { inventory: vi.fn(async () => []), clean: vi.fn(async () => 42) } as unknown as DesktopUpdateCache
    const control = new DesktopUpdateControl(f.updates, f.journal, cache)
    const bridge = await startDesktopUpdateBridge(control)
    cleanups.push(bridge.close)
    const headers = { Authorization: `Bearer ${bridge.token}`, 'Content-Type': 'application/json' }
    expect(await (await fetch(`${bridge.url}/storage/cache`, { headers })).json()).toEqual({ items: [] })
    for (const input of [{ path: '/private/secret', token: 'opaque', operation_id: 'op' }, { token: 'x'.repeat(5000), operation_id: 'op' }]) {
      expect((await fetch(`${bridge.url}/storage/cache/clean`, { method: 'POST', headers, body: JSON.stringify(input) })).status).toBe(400)
    }
    expect(cache.clean).not.toHaveBeenCalled()
    expect((await fetch(`${bridge.url}/storage/cache/clean`, { method: 'POST', body: JSON.stringify({ operation_id: 'op', token: 'opaque' }) })).status).toBe(403)
    expect(await (await fetch(`${bridge.url}/storage/cache/clean`, { method: 'POST', headers, body: JSON.stringify({ operation_id: 'op', token: 'opaque' }) })).json()).toEqual({ removed_bytes: 42 })
    expect(cache.clean).toHaveBeenCalledExactlyOnceWith('op', 'opaque')
  })

  it('waits for durable cache cleanup before starting an update and rejects cleanup admitted after update', async () => {
    const f = await fixture()
    let finish!: () => void
    const cache = { inventory: vi.fn(async () => []), clean: vi.fn(() => new Promise<number>(resolve => { finish = () => resolve(42) })) } as unknown as DesktopUpdateCache
    const control = new DesktopUpdateControl(f.updates, f.journal, cache)
	 f.stop.mockImplementationOnce(async () => { await control.beforeInstall() })
    const cleaning = control.cleanCache('operation', 'token')
    await vi.waitFor(() => expect(cache.clean).toHaveBeenCalledOnce())
    const updating = control.update()
    expect(f.engine.checkForUpdates).not.toHaveBeenCalled()
    await expect(control.cleanCache('another', 'token')).rejects.toThrow('during an update')
    finish()
    expect(await cleaning).toBe(42)
    await updating
    expect(f.engine.downloadUpdate).toHaveBeenCalledOnce()
  })
})

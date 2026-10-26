import { EventEmitter } from 'node:events'
import { describe, expect, it, vi } from 'vitest'
import { DesktopUpdates, updateChannel, updateFeed } from './updates'
class Engine extends EventEmitter {
  autoDownload = false
  autoInstallOnAppQuit = true
  allowPrerelease = true
  allowDowngrade = true
  disableDifferentialDownload = false
  checkForUpdates = vi.fn(async () => null)
  quitAndInstall = vi.fn()
}
describe('Desktop update lifecycle', () => {
  it('uses the public source and separate native architecture channels', () => {
    expect(updateFeed).toBe('https://github.com/oines/astral/releases/latest/download/')
    expect(updateChannel('x64')).toBe('latest-x64')
    expect(updateChannel('arm64')).toBe('latest-arm64')
    expect(() => updateChannel('ia32')).toThrow()
  })
  it('downloads automatically, reports progress, and never installs during a normal quit', async () => {
    const engine = new Engine()
    const stop = vi.fn(async () => {})
    const updates = new DesktopUpdates(engine, '0.1.0', stop)
    expect(engine.autoDownload).toBe(true)
    expect(engine.autoInstallOnAppQuit).toBe(false)
    expect(engine.allowDowngrade).toBe(false)
    expect(engine.disableDifferentialDownload).toBe(true)
    await expect(updates.install()).rejects.toThrow('not finished')
    engine.emit('update-available', { version: '0.1.2' })
    engine.emit('download-progress', { percent: 43.6 })
    expect(updates.state()).toMatchObject({ phase: 'downloading', version: '0.1.2', percent: 43.6 })
    await updates.check()
    expect(engine.checkForUpdates).not.toHaveBeenCalled()
    engine.emit('update-downloaded', { version: '0.1.2' })
    expect(updates.state().phase).toBe('ready')
    expect(stop).not.toHaveBeenCalled()
    expect(engine.quitAndInstall).not.toHaveBeenCalled()
  })
  it('waits for confirmed Server exit and serializes repeated install requests', async () => {
    const engine = new Engine()
    let exit!: () => void
    const stop = vi.fn(() => new Promise<void>(resolve => { exit = resolve }))
    const updates = new DesktopUpdates(engine, '0.1.0', stop)
    engine.emit('update-downloaded', { version: '0.1.1' })
    const first = updates.install()
    const second = updates.install()
    expect(updates.state().phase).toBe('installing')
    expect(engine.quitAndInstall).not.toHaveBeenCalled()
    expect(stop).toHaveBeenCalledTimes(1)
    exit()
    await Promise.all([first, second])
    expect(engine.quitAndInstall).toHaveBeenCalledExactlyOnceWith(false, true)
  })
  it('keeps the downloaded update and supports retry if Server shutdown fails', async () => {
    const engine = new Engine()
    const stop = vi.fn().mockRejectedValueOnce(new Error('Server still running')).mockResolvedValueOnce(undefined)
    const updates = new DesktopUpdates(engine, '0.1.0', stop)
    engine.emit('update-downloaded', { version: '0.1.1' })
    await expect(updates.install()).rejects.toThrow('still running')
    expect(updates.state()).toMatchObject({ phase: 'ready', error: 'Server still running' })
    expect(engine.quitAndInstall).not.toHaveBeenCalled()
    await updates.install()
    expect(engine.quitAndInstall).toHaveBeenCalledTimes(1)
  })
  it('reports background failures and allows a new check', async () => {
    const engine = new Engine()
    engine.checkForUpdates.mockRejectedValueOnce(new Error('offline'))
    const updates = new DesktopUpdates(engine, '0.1.0', async () => {})
    await updates.check()
    expect(updates.state()).toMatchObject({ phase: 'error', error: 'offline' })
    engine.checkForUpdates.mockImplementationOnce(async () => { engine.emit('update-not-available', { version: '0.1.0' }); return null })
    await updates.check()
    expect(updates.state()).toMatchObject({ phase: 'current', error: undefined })
  })
  it('does not contact update servers for development builds or unsupported packages', async () => {
    const engine = new Engine()
    const updates = new DesktopUpdates(engine, '0.1.0', async () => {}, 'package')
    expect(await updates.check()).toMatchObject({ phase: 'unavailable', unavailable: 'package' })
    expect(engine.checkForUpdates).not.toHaveBeenCalled()
  })
  it('reports installer failure after shutdown so main can reopen the current app', async () => {
    const engine = new Engine()
    const updates = new DesktopUpdates(engine, '0.1.0', async () => {})
    updates.onInstallError = vi.fn()
    engine.emit('update-downloaded', { version: '0.1.1' })
    await updates.install()
    engine.emit('error', new Error('Signature validation failed'))
    expect(updates.onInstallError).toHaveBeenCalledOnce()
    expect(updates.state()).toMatchObject({ phase: 'error', error: 'Signature validation failed' })
  })
})

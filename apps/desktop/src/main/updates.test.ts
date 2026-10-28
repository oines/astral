import { EventEmitter } from 'node:events'
import { describe, expect, it, vi } from 'vitest'
import { DesktopUpdates, updateChannel, updateFeed } from './updates'

function release(version: string, isUpdateAvailable = true) {
  return { isUpdateAvailable, updateInfo: { version } }
}
function deferred<T>() {
  let resolve!: (value: T) => void
  let reject!: (error: Error) => void
  const promise = new Promise<T>((yes, no) => { resolve = yes; reject = no })
  return { promise, resolve, reject }
}
class Engine extends EventEmitter {
  autoDownload = true
  autoInstallOnAppQuit = true
  allowPrerelease = true
  allowDowngrade = true
  disableDifferentialDownload = false
  checkForUpdates = vi.fn(async (): Promise<ReturnType<typeof release> | null> => release('0.1.1'))
  downloadUpdate = vi.fn(async (): Promise<unknown> => [])
  quitAndInstall = vi.fn()
}

describe('Desktop update lifecycle', () => {
  it('uses the public source and separate native architecture channels', () => {
    expect(updateFeed).toBe('https://github.com/oines/astral/releases/latest/download/')
    expect(updateChannel('x64')).toBe('latest-x64')
    expect(updateChannel('arm64')).toBe('latest-arm64')
    expect(() => updateChannel('ia32')).toThrow()
  })

  it('checks only metadata and never downloads or stops Server without an explicit update', async () => {
    const engine = new Engine()
    const stop = vi.fn(async () => {})
    const updates = new DesktopUpdates(engine, '0.1.0', stop)
    expect(engine.autoDownload).toBe(false)
    expect(engine.autoInstallOnAppQuit).toBe(false)
    expect(engine.allowPrerelease).toBe(false)
    expect(engine.allowDowngrade).toBe(false)
    expect(engine.disableDifferentialDownload).toBe(true)
    expect(await updates.check(true)).toMatchObject({ phase: 'available', version: '0.1.1' })
    // An unsolicited cached-download event must not authorize installation.
    engine.emit('update-downloaded', { version: '0.1.1' })
    engine.emit('download-progress', { percent: 100 })
    expect(updates.state().phase).toBe('available')
    expect(engine.downloadUpdate).not.toHaveBeenCalled()
    expect(stop).not.toHaveBeenCalled()
    expect(engine.quitAndInstall).not.toHaveBeenCalled()
  })

  it('keeps checking newer releases while an available version is displayed', async () => {
    const engine = new Engine()
    const updates = new DesktopUpdates(engine, '0.1.0', async () => {})
    await updates.check()
    engine.checkForUpdates.mockResolvedValueOnce(release('0.1.3'))
    expect(await updates.check(true)).toMatchObject({ phase: 'available', version: '0.1.3' })
    engine.checkForUpdates.mockResolvedValueOnce(release('0.1.5'))
    expect(await updates.check()).toMatchObject({ phase: 'available', version: '0.1.5' })
    expect(engine.downloadUpdate).not.toHaveBeenCalled()
  })

  it('resolves Latest on click, serializes updates, then waits for download and confirmed Server exit', async () => {
    const engine = new Engine()
    const download = deferred<unknown>()
    const exit = deferred<void>()
    engine.downloadUpdate.mockReturnValueOnce(download.promise)
    const stop = vi.fn(() => exit.promise)
    const updates = new DesktopUpdates(engine, '0.1.0', stop)
    await updates.check()
    engine.checkForUpdates.mockResolvedValueOnce(release('0.1.4'))
    const first = updates.update()
    const second = updates.update()
    await vi.waitFor(() => expect(updates.state()).toMatchObject({ phase: 'downloading', version: '0.1.4', percent: 0 }))
    engine.emit('download-progress', { percent: 43.6 })
    expect(updates.state().percent).toBe(43.6)
    engine.emit('download-progress', { percent: 200 })
    expect(updates.state().percent).toBe(100)
    engine.emit('download-progress', { percent: NaN })
    expect(updates.state().percent).toBe(100)
    expect(await updates.check(true)).toMatchObject({ phase: 'downloading' })
    expect(engine.checkForUpdates).toHaveBeenCalledTimes(2)
    // The library promise, including its verification, must complete before shutdown.
    engine.emit('update-downloaded', { version: '0.1.4' })
    expect(stop).not.toHaveBeenCalled()
    download.resolve([])
    await vi.waitFor(() => expect(stop).toHaveBeenCalledTimes(1))
    expect(updates.state().phase).toBe('installing')
    expect(engine.quitAndInstall).not.toHaveBeenCalled()
    exit.resolve()
    await Promise.all([first, second])
    await expect(updates.update()).rejects.toThrow('restarting')
    expect(engine.downloadUpdate).toHaveBeenCalledTimes(1)
    expect(engine.quitAndInstall).toHaveBeenCalledExactlyOnceWith(false, true)
  })

  it('waits for an earlier metadata check and performs a fresh lookup for the update action', async () => {
    const engine = new Engine()
    const checking = deferred<ReturnType<typeof release>>()
    engine.checkForUpdates.mockReturnValueOnce(checking.promise).mockResolvedValueOnce(release('0.1.6'))
    const updates = new DesktopUpdates(engine, '0.1.0', async () => {})
    const background = updates.check(true)
    const update = updates.update()
    expect(engine.checkForUpdates).toHaveBeenCalledTimes(1)
    checking.resolve(release('0.1.2'))
    await Promise.all([background, update])
    expect(engine.checkForUpdates).toHaveBeenCalledTimes(2)
    expect(updates.state()).toMatchObject({ phase: 'installing', version: '0.1.6' })
  })

  it('does not download or stop Server if the latest lookup finds no newer version', async () => {
    const engine = new Engine()
    const stop = vi.fn(async () => {})
    const updates = new DesktopUpdates(engine, '0.1.0', stop)
    await updates.check()
    engine.checkForUpdates.mockResolvedValueOnce(release('0.0.9', false))
    await updates.update()
    expect(updates.state()).toMatchObject({ phase: 'current', version: undefined })
    expect(engine.downloadUpdate).not.toHaveBeenCalled()
    expect(stop).not.toHaveBeenCalled()
  })

  it('coalesces metadata checks and allows an explicit retry after a check failure', async () => {
    const engine = new Engine()
    const checking = deferred<ReturnType<typeof release>>()
    engine.checkForUpdates.mockReturnValueOnce(checking.promise)
    const updates = new DesktopUpdates(engine, '0.1.0', async () => {})
    const first = updates.check(true)
    const second = updates.check()
    expect(engine.checkForUpdates).toHaveBeenCalledTimes(1)
    checking.reject(new Error('offline'))
    await Promise.all([first, second])
    expect(updates.state()).toMatchObject({ phase: 'error', error: 'offline', retry: 'check' })
    expect(await updates.check()).toMatchObject({ phase: 'available', error: undefined, retry: undefined })
    expect(engine.downloadUpdate).not.toHaveBeenCalled()
  })

  it('does not use a stale offer if the explicit latest lookup fails', async () => {
    const engine = new Engine()
    const stop = vi.fn(async () => {})
    const updates = new DesktopUpdates(engine, '0.1.0', stop)
    await updates.check()
    engine.checkForUpdates.mockRejectedValueOnce(new Error('Latest lookup failed'))
    await expect(updates.update()).rejects.toThrow('Latest lookup failed')
    expect(updates.state()).toMatchObject({ phase: 'error', retry: 'update' })
    expect(engine.downloadUpdate).not.toHaveBeenCalled()
    expect(stop).not.toHaveBeenCalled()
  })

  it('keeps update errors visible to automatic checks; a manual check only checks', async () => {
    const engine = new Engine()
    const stop = vi.fn(async () => {})
    const updates = new DesktopUpdates(engine, '0.1.0', stop)
    engine.downloadUpdate.mockImplementationOnce(async () => {
      engine.emit('error', new Error('checksum mismatch'))
      throw new Error('checksum mismatch')
    })
    await expect(updates.update()).rejects.toThrow('checksum mismatch')
    expect(updates.state()).toMatchObject({ phase: 'error', error: 'checksum mismatch', retry: 'update' })
    await updates.check(true)
    expect(engine.checkForUpdates).toHaveBeenCalledTimes(1)
    expect(updates.state().error).toBe('checksum mismatch')
    expect(await updates.check()).toMatchObject({ phase: 'available', error: undefined, retry: undefined })
    expect(engine.downloadUpdate).toHaveBeenCalledTimes(1)
    expect(stop).not.toHaveBeenCalled()
  })

  it('refreshes Latest when retrying a failed download', async () => {
    const engine = new Engine()
    const stop = vi.fn(async () => {})
    const updates = new DesktopUpdates(engine, '0.1.0', stop)
    engine.downloadUpdate.mockRejectedValueOnce(new Error('connection reset'))
    await expect(updates.update()).rejects.toThrow('connection reset')
    engine.checkForUpdates.mockResolvedValueOnce(release('0.1.7'))
    await updates.update()
    expect(updates.state()).toMatchObject({ phase: 'installing', version: '0.1.7' })
    expect(engine.downloadUpdate).toHaveBeenCalledTimes(2)
    expect(stop).toHaveBeenCalledTimes(1)
  })

  it('leaves the current App running and supports retry if Server shutdown fails', async () => {
    const engine = new Engine()
    const stop = vi.fn().mockRejectedValueOnce(new Error('Server still running')).mockResolvedValueOnce(undefined)
    const updates = new DesktopUpdates(engine, '0.1.0', stop)
    updates.onInstallError = vi.fn()
    await expect(updates.update()).rejects.toThrow('Server still running')
    expect(updates.state()).toMatchObject({ phase: 'error', error: 'Server still running', retry: 'update' })
    expect(engine.quitAndInstall).not.toHaveBeenCalled()
    expect(updates.onInstallError).not.toHaveBeenCalled()
    await updates.update()
    expect(engine.quitAndInstall).toHaveBeenCalledTimes(1)
  })

  it('reports a disabled update engine as a retryable check failure', async () => {
    const engine = new Engine()
    engine.checkForUpdates.mockResolvedValueOnce(null)
    const updates = new DesktopUpdates(engine, '0.1.0', async () => {})
    expect(await updates.check()).toMatchObject({ phase: 'error', retry: 'check', error: 'The update service is unavailable.' })
    expect(engine.downloadUpdate).not.toHaveBeenCalled()
  })

  it('does not contact update servers for development builds or unsupported packages', async () => {
    const engine = new Engine()
    const updates = new DesktopUpdates(engine, '0.1.0', async () => {}, 'package')
    expect(await updates.check()).toMatchObject({ phase: 'unavailable', unavailable: 'package' })
    await expect(updates.update()).rejects.toThrow('unavailable')
    expect(engine.checkForUpdates).not.toHaveBeenCalled()
    expect(engine.downloadUpdate).not.toHaveBeenCalled()
  })

  it('reports asynchronous installer failure after shutdown so main can reopen the current App', async () => {
    const engine = new Engine()
    const updates = new DesktopUpdates(engine, '0.1.0', async () => {})
    updates.onInstallError = vi.fn()
    await updates.update()
    engine.emit('error', new Error('Signature validation failed'))
    engine.emit('error', new Error('duplicate installer failure'))
    expect(updates.onInstallError).toHaveBeenCalledOnce()
    expect(updates.state()).toMatchObject({ phase: 'error', error: 'Signature validation failed', retry: 'update' })
    await updates.check()
    await expect(updates.update()).rejects.toThrow('restarting')
    expect(engine.checkForUpdates).toHaveBeenCalledTimes(1)
    expect(engine.downloadUpdate).toHaveBeenCalledTimes(1)
  })

  it('reports a synchronous installer event and rejection only once', async () => {
    const engine = new Engine()
    const updates = new DesktopUpdates(engine, '0.1.0', async () => {})
    updates.onInstallError = vi.fn()
    engine.quitAndInstall.mockImplementationOnce(() => {
      engine.emit('error', new Error('Installer refused to start'))
      throw new Error('Installer refused to start')
    })
    await expect(updates.update()).rejects.toThrow('Installer refused to start')
    expect(updates.onInstallError).toHaveBeenCalledOnce()
    expect(updates.state()).toMatchObject({ phase: 'error', error: 'Installer refused to start', retry: 'update' })
  })
})

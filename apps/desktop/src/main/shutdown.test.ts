import { describe, expect, it, vi } from 'vitest'
import { DesktopShutdown } from './shutdown'
describe('shared Desktop shutdown', () => {
  it('shares cleanup across concurrent quit paths and marks complete only after exit', async () => {
    let exit!: () => void
    const stop = vi.fn(() => new Promise<void>(resolve => { exit = resolve }))
    const shutdown = new DesktopShutdown(stop)
    const quit = shutdown.prepare()
    const update = shutdown.prepare()
    expect(stop).toHaveBeenCalledTimes(1)
    expect(shutdown.complete).toBe(false)
    exit()
    await Promise.all([quit, update])
    expect(shutdown.complete).toBe(true)
    await shutdown.prepare()
    expect(stop).toHaveBeenCalledTimes(1)
  })
  it('does not permit quit or installation on failure and allows retry', async () => {
    const stop = vi.fn().mockRejectedValueOnce(new Error('pipes still open')).mockResolvedValueOnce(undefined)
    const shutdown = new DesktopShutdown(stop)
    await expect(shutdown.prepare()).rejects.toThrow('pipes still open')
    expect(shutdown.complete).toBe(false)
    await shutdown.prepare()
    expect(shutdown.complete).toBe(true)
  })
})

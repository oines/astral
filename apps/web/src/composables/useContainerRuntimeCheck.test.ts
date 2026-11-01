import { describe, expect, it, vi } from 'vitest'
import type { AstralInternalDomainHostInfo as HostInfo } from '@astral/sdk'
import { useContainerRuntimeCheck } from './useContainerRuntimeCheck'

const host = (patch: Partial<HostInfo> = {}): HostInfo => ({ hostname: 'linux-server', os: 'linux', arch: 'amd64', container_available: false, container_status: 'permission_denied', container_user: 'server-owner', container_error: 'socket permission denied', ...patch })

describe('Server-side container runtime checks', () => {
  it('refreshes an unavailable bootstrap hint after Docker is configured, without restarting the page', async () => {
    const bootstrap = host()
    const refreshed = host({ container_available: true, container_status: 'available', container_runtime: 'existing Docker', container_error: undefined })
    const load = vi.fn(async () => refreshed)
    const check = useContainerRuntimeCheck(() => bootstrap, load)
    expect(check.available.value).toBe(false)
    expect(await check.check()).toBe(true)
    expect(load).toHaveBeenCalledOnce()
    expect(check.host.value).toEqual(refreshed)
    expect(check.available.value).toBe(true)
    expect(bootstrap.container_status).toBe('permission_denied')
  })

  it('deduplicates concurrent clicks and prevents creation until the fresh check finishes', async () => {
    let finish!: (value: HostInfo) => void
    const pending = new Promise<HostInfo>(resolve => { finish = resolve })
    const load = vi.fn(() => pending)
    const check = useContainerRuntimeCheck(() => host({ container_available: true }), load)
    const first = check.check(), second = check.check()
    expect(first).toBe(second)
    expect(check.checking.value).toBe(true)
    expect(check.available.value).toBe(false)
    await Promise.resolve()
    expect(load).toHaveBeenCalledOnce()
    finish(host({ container_available: true, container_status: 'available' }))
    await first
    expect(check.checking.value).toBe(false)
    expect(check.available.value).toBe(true)
  })

  it('keeps a changed runtime unavailable after a failed request and can be checked again', async () => {
    const load = vi.fn<() => Promise<HostInfo>>()
      .mockRejectedValueOnce(new Error('Server connection interrupted'))
      .mockResolvedValueOnce(host({ container_status: 'not_running', container_error: 'connection refused' }))
      .mockResolvedValueOnce(host({ container_available: true, container_status: 'available', container_error: undefined }))
    const check = useContainerRuntimeCheck(() => host({ container_available: true }), load)
    expect(await check.check()).toBe(false)
    expect(check.error.value).toBe('Server connection interrupted')
    expect(check.available.value).toBe(false)
    expect(await check.check()).toBe(false)
    expect(check.error.value).toBe('')
    expect(check.host.value?.container_status).toBe('not_running')
    expect(await check.check()).toBe(true)
    expect(load).toHaveBeenCalledTimes(3)
  })
})

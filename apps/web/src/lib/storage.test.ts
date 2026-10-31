import { beforeEach, describe, expect, it, vi } from 'vitest'
import { cleanStorageChat, resetStorageChat, storageShare, StorageHttpError, storageRequestRejected } from './storage'
import { readStorageTasks, storageTaskRequest, writeStorageTasks } from './storage-tasks'

const http = vi.hoisted(() => ({ post: vi.fn(async () => ({ data: { id: 'operation', state: 'queued' } })), get: vi.fn() }))
vi.mock('@astral/sdk/client', () => ({ client: http }))
vi.mock('./api', () => ({ errorText: (error: unknown) => error && typeof error === 'object' && 'message' in error ? String(error.message) : 'Request failed', request: async (pending: Promise<{ data: unknown }>) => (await pending).data }))
beforeEach(() => vi.clearAllMocks())

describe('storage cleanup requests', () => {
  it('sends a date boundary without changing the retention policy', async () => {
    await cleanStorageChat('chat/a', 'stable-id', '2026-10-01T00:00:00Z')
    expect(http.post).toHaveBeenCalledWith(expect.objectContaining({ url: '/api/v1/storage/conversations/chat%2Fa/cleanup', body: { operation_id: 'stable-id', before: '2026-10-01T00:00:00Z' } }))
  })
  it('preserves zero retention as an explicit policy and omits the date boundary', async () => {
    await cleanStorageChat('chat', 'stable-id', undefined, 0)
    expect(http.post).toHaveBeenCalledWith(expect.objectContaining({ body: { operation_id: 'stable-id', retention_days: 0 } }))
  })
  it('checks the confirmed binding revision when replacing the session', async () => {
    await resetStorageChat('chat', 'stable-id', 42)
    expect(http.post).toHaveBeenCalledWith(expect.objectContaining({ body: { operation_id: 'stable-id', revision: 42 } }))
  })
  it('distinguishes explicit rejection from an unconfirmed network or server result', () => {
    expect(storageRequestRejected(new StorageHttpError(409, { message: 'stale revision' }))).toBe(true)
    expect(storageRequestRejected(new StorageHttpError(400, { message: 'invalid date' }))).toBe(true)
    for (const status of [0, 408, 429, 500, 503]) expect(storageRequestRejected(new StorageHttpError(status, {}))).toBe(false)
  })
  it('keeps display proportions bounded without inventing capacity', () => {
    expect(storageShare(20, 0)).toBe('0%')
    expect(storageShare(200, 100)).toBe('100%')
    expect(storageShare(-20, 100)).toBe('0%')
  })
})

describe('destructive task recovery', () => {
  it('reuses the exact confirmed request after an interrupted response', () => {
    const requests = {}
    const first = storageTaskRequest(requests, 'reset:chat', { kind: 'reset', conversation_id: 'chat', revision: 42 })
    const retry = storageTaskRequest(requests, 'reset:chat', { kind: 'reset', conversation_id: 'chat', revision: 43 })
    expect(retry).toBe(first)
    expect(retry.revision).toBe(42)
    expect(retry.operation_id).toBeTruthy()
  })
  it('restores acknowledged and unconfirmed tasks after a reload', () => {
    let stored = ''
    const storage = { getItem: () => stored, setItem: (_key: string, value: string) => { stored = value } }
    const memory = { requests: { cleanup: { operation_id: 'cleanup-id', kind: 'application' as const, tokens: ['log-token'] } }, conversations: { chat: { id: 'reset-id', kind: 'reset', conversation_id: 'chat', state: 'running' as const } } }
    writeStorageTasks(storage, memory)
    expect(readStorageTasks(storage)).toEqual(memory)
  })
  it('keeps the page usable if browser storage is unavailable or corrupt', () => {
    expect(readStorageTasks({ getItem: () => '{' })).toEqual({ requests: {}, conversations: {} })
    expect(() => writeStorageTasks({ setItem: () => { throw new Error('denied') } }, { requests: {}, conversations: {} })).not.toThrow()
  })
})

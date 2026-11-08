import { beforeEach, describe, expect, it, vi } from 'vitest'
import { deleteWakeEndpoint, listWakeEndpoints, updateWakeEndpoint } from '@astral/sdk'
import { wakeAPI, wakeBlocked, wakeExample, type WakeEndpoint } from './wake-endpoints'

vi.mock('@astral/sdk', () => ({
  listWakeEndpoints: vi.fn(), updateWakeEndpoint: vi.fn(), deleteWakeEndpoint: vi.fn(),
}))

const endpoint: WakeEndpoint = { id: 'wake', agent_id: 'bot', session_id: 'root', source_session_id: 'root', computer_id: 'computer', title: 'Events', enabled: true, trigger_url: 'http://127.0.0.1:57200/hooks/wake', created_at: '', updated_at: '', revision: 1 }

beforeEach(() => vi.clearAllMocks())

describe('generated endpoint API', () => {
  it('keeps Bot scope and optimistic revisions in generated SDK requests', async () => {
    vi.mocked(listWakeEndpoints).mockResolvedValue({ data: [endpoint] } as never)
    vi.mocked(updateWakeEndpoint).mockResolvedValue({ data: { ...endpoint, revision: 2 } } as never)
    vi.mocked(deleteWakeEndpoint).mockResolvedValue({ data: { ok: true } } as never)
    expect(await wakeAPI.list('bot')).toEqual([endpoint])
    expect(listWakeEndpoints).toHaveBeenCalledWith({ query: { agent_id: 'bot' } })
    expect((await wakeAPI.update(endpoint, { notes: '/opt/listener.py', enabled: false })).revision).toBe(2)
    expect(updateWakeEndpoint).toHaveBeenCalledWith({ path: { id: 'wake' }, body: { revision: 1, notes: '/opt/listener.py', enabled: false } })
    await wakeAPI.remove(endpoint)
    expect(deleteWakeEndpoint).toHaveBeenCalledWith({ path: { id: 'wake' }, query: { revision: 1 } })
  })
})

describe('local event delivery example', () => {
  it('quotes both JSON and the delivery URL as shell data without credentials', () => {
    const example = wakeExample(endpoint, 'build-42', "Player's message: $(touch nope) `date`\nnext line")
    expect(example).toContain("'http://127.0.0.1:57200/hooks/wake'")
    expect(example).toContain("Player'\\''s")
    expect(example).toContain('$(touch nope)')
    expect(example).toContain('\\nnext line')
    expect(example).not.toContain('Authorization')
  })

  it('shows traffic protection only before its deadline', () => {
    const blocked = { ...endpoint, blocked_until: '2026-11-08T03:00:00Z' }
    expect(wakeBlocked(blocked, Date.parse('2026-11-08T02:59:59Z'))).toBe(true)
    expect(wakeBlocked(blocked, Date.parse('2026-11-08T03:00:00Z'))).toBe(false)
    expect(wakeBlocked(endpoint, 0)).toBe(false)
  })
})

import { describe, expect, it } from 'vitest'
import { retentionNeedsConfirmation, storageShare, storageSize, storageSources, type StorageBot } from './storage'

describe('storage data sources', () => {
  const bots = [
    { agent_id: 'a', chats: [{ title: 'Team', logical_bytes: 12 }], sessions: [{ title: 'Draft', logical_bytes: 0 }] },
    { agent_id: 'b', chats: [], sessions: [{ title: 'Team notes', logical_bytes: 34 }] },
  ] as StorageBot[]

  it('lists chats and unbound sessions together by descending size, including empty sessions', () => {
    expect(storageSources(bots, '', '').map(row => row.value.title)).toEqual(['Team notes', 'Team', 'Draft'])
    expect(storageSources(bots, 'a', 'TEAm').map(row => row.kind)).toEqual(['chat'])
    expect(storageSources(bots, 'a', 'other')).toEqual([])
  })
  it('distinguishes a completed zero from a missing estimate', () => {
    expect(storageSize(0, 'complete', () => 'estimate')).toBe('0 B')
    expect(storageSize(0, 'failed', () => 'estimate')).toBe('—')
    expect(storageShare(15, 10)).toBe('100%')
    expect(storageShare(0, 0)).toBe('0%')
  })
  it('confirms only retention changes that can expire more messages', () => {
    for (const [from, to] of [[0, 30], [180, 90], [90, 30]] as const) expect(retentionNeedsConfirmation(from, to)).toBe(true)
    for (const [from, to] of [[30, 90], [90, 0], [30, 30]] as const) expect(retentionNeedsConfirmation(from, to)).toBe(false)
  })
})

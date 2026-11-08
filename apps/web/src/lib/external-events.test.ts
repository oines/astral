import { describe, expect, it } from 'vitest'
import { addEntry, durableTranscriptGroups, emptyState, externalMessage, messageText, visibleEntry } from './session-state'
import type { Entry } from './types'

const batch: Entry = { id: 'batch-entry', version: 1, sequence: 1, session_id: 'root', time: '2026-11-08T03:00:00Z', kind: 'context', message: { role: 'user', context_kind: 'external.events', content: [{ type: 'text', text: 'Trusted notice and escaped content' }], metadata: { source: 'external', display_text: '[Minecraft · Receiving host] Player message', external: { title: 'Minecraft', sources: [{ endpoint_id: 'wake', computer_id: 'source-computer', computer_name: 'Receiving host' }] } } } }

describe('external event batches in the transcript', () => {
  it('shows one actual batch with its receiving Computer rather than one input per event', () => {
    const state = emptyState()
    addEntry(state, batch, true)
    const groups = durableTranscriptGroups(state)
    expect(groups).toHaveLength(1)
    expect(groups[0]!.role).toBe('user')
    expect(groups[0]!.entries).toHaveLength(1)
    expect(messageText(batch)).toBe('[Minecraft · Receiving host] Player message')
    expect(externalMessage(batch)).toMatchObject({ title: 'Minecraft', computers: ['Receiving host'] })
    expect(visibleEntry(batch)).toBe(true)
  })

  it('keeps internal wake-up drivers and unrelated passive contexts hidden', () => {
    const driver: Entry = { ...batch, id: 'driver', message: { ...batch.message!, context_kind: 'external.wakeup', metadata: { source: 'external.trigger' } } }
    const untrusted: Entry = { ...batch, id: 'untrusted', message: { ...batch.message!, context_kind: 'other.context' } }
    expect(visibleEntry(driver)).toBe(false)
    expect(visibleEntry(untrusted)).toBe(false)
    expect(externalMessage(driver)).toBeUndefined()
  })
})

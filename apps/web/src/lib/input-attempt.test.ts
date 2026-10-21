import { describe, expect, it, vi } from 'vitest'
import { prepareInputAttempt } from './input-attempt'

describe('draft recovery after a failed send', () => {
  const original = { id: 'delivery-1', text: 'Original message', files: ['astral-file://one'], mode: 'follow_up' }

  it('retries unchanged content with the same delivery identity', async () => {
    const reconcile = vi.fn()
    const retry = await prepareInputAttempt(original, { ...original, files: [...original.files] }, reconcile)
    expect(retry).toBe(original)
    expect(reconcile).not.toHaveBeenCalled()
  })

  it('reconciles the original request before submitting edited text or attachments', async () => {
    let complete!: () => void
    const reconciliation = new Promise<void>(resolve => { complete = resolve })
    const reconcile = vi.fn(() => reconciliation)
    const edited = { text: 'Edited message', files: [], mode: 'follow_up' }
    const prepared = prepareInputAttempt(original, edited, reconcile)
    expect(reconcile).toHaveBeenCalledWith('delivery-1')
    complete()
    const next = await prepared
    expect(next.id).not.toBe(original.id)
    expect(next).toMatchObject(edited)
    expect(original.text).toBe('Original message')
  })

  it('keeps the draft and pending delivery intact if the server is still unavailable', async () => {
    const edited = { text: 'A new draft', files: [], mode: 'steer' }
    await expect(prepareInputAttempt(original, edited, async () => { throw new Error('offline') })).rejects.toThrow('offline')
    expect(edited.text).toBe('A new draft')
    expect(original.id).toBe('delivery-1')
  })
})

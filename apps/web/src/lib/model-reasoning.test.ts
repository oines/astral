import { describe, expect, it } from 'vitest'
import { modelReasoning, reasoningSelection } from './model-reasoning'
import type { Profile } from './types'

const profile = {
  id: 'model', recommendations: { reasoning: { efforts: ['fake-tier'] }, defaults: { reasoning: { effort: 'low' } } },
  reasoning_controls: { format: 'openai-responses', options: [{ id: 'interface-default', value: {} }, { id: 'off', value: { enabled: false } }, { id: 'effort:low', value: { effort: 'low' } }, { id: 'effort:vendor-max', value: { effort: 'vendor-max' } }] },
} as unknown as Profile
const t = (key: string) => key

describe('model reasoning selection', () => {
  it('uses backend controls even when catalog capabilities suggest different levels', () => {
    expect(modelReasoning(profile, undefined, t)).toMatchObject({ value: 'inherit', label: 'low', options: [{ value: 'inherit', label: 'default · low' }, { value: 'interface-default', label: 'interfaceDefault' }, { value: 'off', label: 'off' }, { value: 'effort:low', label: 'low' }, { value: 'effort:vendor-max', label: 'vendor-max' }] })
    expect(reasoningSelection('effort:vendor-max', profile)).toEqual({ effort: 'vendor-max' })
    expect(reasoningSelection('fake-tier', profile)).toBeUndefined()
  })
  it('keeps inheritance, explicit API default, and off as distinct persisted choices', () => {
    expect(reasoningSelection('inherit', profile)).toBeUndefined()
    expect(reasoningSelection('interface-default', profile)).toEqual({})
    expect(reasoningSelection('off', profile)).toEqual({ enabled: false })
    expect(modelReasoning(profile, {}, t)).toMatchObject({ value: 'interface-default', label: 'interfaceDefault' })
    expect(modelReasoning(profile, { enabled: false }, t)).toMatchObject({ value: 'off', label: 'off' })
  })
  it('hides controls without executable backend choices instead of inferring them', () => {
    expect(modelReasoning(undefined, undefined, t).options).toEqual([])
    expect(modelReasoning({ ...profile, reasoning_controls: undefined }, undefined, t).options).toEqual([])
    expect(modelReasoning({ ...profile, recommendations: {}, reasoning_controls: { format: 'openai-responses', options: [{ id: 'interface-default', value: {} }] } }, undefined, t).options).toEqual([])
    expect(modelReasoning({ ...profile, reasoning_controls: { format: 'openai-responses', options: [{ id: 'interface-default', value: {} }] } }, undefined, t).options).toHaveLength(2)
  })
  it('keeps opaque semantic values from the backend without adding toggle fields', () => {
    const effortOnly = { ...profile, reasoning_controls: { format: 'openai-chat', options: [{ id: 'interface-default', value: {} }, { id: 'off', value: { effort: 'vendor-off' } }, { id: 'effort:high', value: { effort: 'high' } }] } }
    expect(reasoningSelection('effort:high', effortOnly)).toEqual({ effort: 'high' })
    expect(reasoningSelection('off', effortOnly)).toEqual({ effort: 'vendor-off' })
    expect(modelReasoning(effortOnly, { effort: 'vendor-off' }, t)).toMatchObject({ value: 'off', label: 'off' })
  })
  it('preserves exact budgets including zero and shows a stale choice honestly', () => {
    const budget = { ...profile, reasoning_controls: { format: 'qwen', options: [{ id: 'interface-default', value: {} }], budget: { min: 0, max: 32000 } } }
    expect(modelReasoning(budget, { budget_tokens: 0 }, t)).toMatchObject({ value: 'budget', label: 'reasoningBudget · 0', budget: { min: 0, max: 32000 } })
    expect(modelReasoning(budget, { budget_tokens: 32001 }, t).value).toBe('unavailable')
    expect(modelReasoning(profile, { effort: 'retired-tier' }, t)).toMatchObject({ value: 'unavailable', label: 'retired-tier · reasoningUnavailable' })
  })
})

import { describe, expect, it } from 'vitest'
import { modelReasoning, reasoningSelection } from './model-reasoning'
import type { Profile } from './types'

const profile = { id: 'model', recommendations: { reasoning: { efforts: ['low', 'vendor-max'], disable: true }, defaults: { reasoning: { effort: 'low' } } } } as unknown as Profile
const t = (key: string) => key

describe('model reasoning selection', () => {
  it('uses the model’s native levels and default without inventing levels', () => {
    expect(modelReasoning(profile, undefined, t)).toEqual({ value: 'default', label: 'low', options: [{ value: 'default', label: 'default · low' }, { value: 'off', label: 'off' }, { value: 'low', label: 'low' }, { value: 'vendor-max', label: 'vendor-max' }] })
    expect(reasoningSelection('vendor-max')).toEqual({ enabled: true, effort: 'vendor-max' })
    expect(reasoningSelection('default')).toBeUndefined()
    expect(reasoningSelection('off')).toEqual({ enabled: false })
  })
  it('honors capability overrides and hides controls when no reasoning is supported', () => {
    const overridden = { ...profile, overrides: { reasoning: { toggle: true, efforts: [] }, defaults: { reasoning: { enabled: false } } } } as unknown as Profile
    expect(modelReasoning(overridden, { enabled: true }, t)).toEqual({ value: 'on', label: 'on', options: [{ value: 'default', label: 'default · off' }, { value: 'on', label: 'on' }] })
    expect(modelReasoning(undefined, undefined, t).options).toEqual([])
    expect(modelReasoning({ ...profile, overrides: { reasoning: { efforts: [] } } } as unknown as Profile, undefined, t).options).toEqual([])
  })
  it('uses native effort fields when the model explicitly has no reasoning toggle', () => {
    const effortOnly = { ...profile, overrides: { reasoning: { toggle: false, disable: true, off_effort: 'none', efforts: ['none', 'low', 'high'] } } } as unknown as Profile
    expect(reasoningSelection('high', effortOnly)).toEqual({ effort: 'high' })
    expect(reasoningSelection('off', effortOnly)).toEqual({ effort: 'none' })
    expect(modelReasoning(effortOnly, { effort: 'none' }, t)).toMatchObject({ value: 'off', label: 'off', options: [{ value: 'default', label: 'default · low' }, { value: 'off', label: 'off' }, { value: 'low', label: 'low' }, { value: 'high', label: 'high' }] })
  })
})

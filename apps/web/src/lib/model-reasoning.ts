import type { AstralCoreReasoningOptions as Reasoning } from '@astral/sdk'
import type { Profile } from './types'
import { object } from './api'

export type ModelSelection = { profile_id: string; reasoning?: Reasoning }

export function reasoningSelection(value: string, profile?: Profile): Reasoning | undefined {
  const caps = object(({ ...profile?.recommendations, ...profile?.overrides }).reasoning)
  if (value === 'default') return undefined
  if (value === 'off') return caps.toggle === false && typeof caps.off_effort === 'string' ? { effort: caps.off_effort } : { enabled: false }
  if (value === 'on') return { enabled: true }
  return caps.toggle === false ? { effort: value } : { enabled: true, effort: value }
}

export function modelReasoning(profile: Profile | undefined, reasoning: Reasoning | undefined, t: (key: string) => string) {
  const values = { ...profile?.recommendations, ...profile?.overrides }
  const caps = object(values.reasoning)
  const defaults = object(object(values.defaults).reasoning)
  const efforts = Array.isArray(caps.efforts) ? caps.efforts.filter((value): value is string => typeof value === 'string' && value !== caps.off_effort) : []
  const canDisable = caps.disable && (caps.toggle !== false || typeof caps.off_effort === 'string')
  const value = reasoning?.enabled === false || (reasoning?.effort && reasoning.effort === caps.off_effort) ? 'off' : reasoning?.effort || (reasoning?.enabled ? 'on' : 'default')
  const defaultLabel = defaults.enabled === false || (defaults.effort && defaults.effort === caps.off_effort) ? t('off') : typeof defaults.effort === 'string' ? t(defaults.effort) : defaults.enabled === true ? t('on') : t('default')
  const options = !efforts.length && !caps.toggle && !canDisable ? [] : [
    { value: 'default', label: defaultLabel === t('default') ? t('default') : `${t('default')} · ${defaultLabel}` },
    ...(canDisable ? [{ value: 'off', label: t('off') }] : []),
    ...(caps.toggle ? [{ value: 'on', label: t('on') }] : []),
    ...efforts.map(value => ({ value, label: t(value) })),
  ]
  return { value, label: value === 'default' ? defaultLabel : t(value), options }
}

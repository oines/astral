import type { AstralCoreReasoningOptions as Reasoning } from '@astral/sdk'
import type { Profile } from './types'
import { object } from './api'

export type ModelSelection = { profile_id: string; reasoning?: Reasoning }

export function reasoningSelection(value: string, profile?: Profile): Reasoning | undefined {
  if (value === 'inherit') return undefined
  const option = profile?.reasoning_controls?.options.find(option => option.id === value)
  return option ? { ...option.value } : undefined
}

function sameSelection(a: Reasoning, b: Reasoning) {
  return a.enabled === b.enabled && a.effort === b.effort && a.budget_tokens === b.budget_tokens
}

function selectionLabel(value: Reasoning, t: (key: string) => string) {
  if (value.enabled === false) return t('off')
  if (value.budget_tokens !== undefined) return `${t('reasoningBudget')} · ${value.budget_tokens.toLocaleString()}`
  if (value.effort) return value.effort
  if (value.enabled === true) return t('on')
  return t('interfaceDefault')
}

export function modelReasoning(profile: Profile | undefined, reasoning: Reasoning | undefined, t: (key: string) => string) {
  const controls = profile?.reasoning_controls
  const defaults = object(object(({ ...profile?.recommendations, ...profile?.overrides }).defaults).reasoning) as Reasoning
  const defaultLabel = selectionLabel(defaults, t)
  const hasDefaults = defaults.enabled !== undefined || !!defaults.effort || defaults.budget_tokens !== undefined
  const available = !!controls && (controls.options.some(option => option.id !== 'interface-default') || !!controls.budget || reasoning !== undefined || hasDefaults)
  const options = available ? [
    { value: 'inherit', label: `${t('default')} · ${defaultLabel}` },
    ...controls.options.map(option => ({ value: option.id, label: option.id.startsWith('effort:') ? option.id.slice(7) : option.id === 'interface-default' ? t('interfaceDefault') : t(option.id) })),
  ] : []
  const selected = reasoning ? controls?.options.find(option => sameSelection(option.value, reasoning)) : undefined
  const validBudget = !!controls?.budget && reasoning?.budget_tokens !== undefined && Number.isInteger(reasoning.budget_tokens) && (controls.budget.min === undefined || reasoning.budget_tokens >= controls.budget.min) && (controls.budget.max === undefined || reasoning.budget_tokens <= controls.budget.max) && reasoning.enabled === undefined && !reasoning.effort
  const value = reasoning === undefined ? 'inherit' : selected?.id || (validBudget ? 'budget' : 'unavailable')
  const label = reasoning === undefined ? defaultLabel : selected ? options.find(option => option.value === selected.id)?.label || selectionLabel(reasoning, t) : validBudget ? selectionLabel(reasoning, t) : `${selectionLabel(reasoning, t)} · ${t('reasoningUnavailable')}`
  return { value, label, options, budget: available ? controls?.budget : undefined }
}

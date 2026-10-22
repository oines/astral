import { reactive } from 'vue'
import { useQueryCache, type EntryKey } from '@pinia/colada'
import { bootstrapQueryKey } from '@astral/sdk/colada'
import type { AstralInternalHttpapiBootstrap as Bootstrap } from '@astral/sdk'
import type { Agent, Profile } from '../lib/types'
import { createSettingsMutations, type SettingsOperation, type SettingsSaveState } from '../lib/settings-mutations'

const states = reactive<Record<string, SettingsSaveState>>({})
const mutations = createSettingsMutations((key, state) => { states[key] = state })
const drafts = reactive<Record<string, string>>({})
const intents = reactive<Record<string, unknown>>({})
const forms = reactive<Record<string, unknown>>({})

export function useSettingsSave() {
  const cache = useQueryCache()
  return {
    confirmAgent: (value: Agent) => {
      const current = cache.getQueryData<Bootstrap>(bootstrapQueryKey())
      if (current) cache.setQueryData(bootstrapQueryKey(), { ...current, agents: current.agents.map(a => a.id === value.id && a.revision <= value.revision ? value : a) })
    },
    confirmModel: (value: Profile) => {
      const current = cache.getQueryData<Bootstrap>(bootstrapQueryKey())
      if (current) cache.setQueryData(bootstrapQueryKey(), { ...current, models: current.models.map(m => m.id === value.id && m.revision <= value.revision ? value : m) })
    },
    confirm: <T extends { id: string; revision: number }>(key: EntryKey, value: T) => {
      cache.setQueryData<T[]>(key, current => current?.some(item => item.id === value.id)
        ? current.map(item => item.id === value.id && item.revision <= value.revision ? { ...item, ...value } : item)
        : [...(current || []), value])
    },
    retry: mutations.retry,
    run: (resource: string, field: string, operation: SettingsOperation, intent?: unknown) => {
      intents[`${resource}/${field}`] = intent
      return mutations.run(resource, field, operation)
    },
    drafts,
    form: <T>(key: string, create: () => T): T => {
      if (!(key in forms)) forms[key] = create()
      return forms[key] as T
    },
    discardForm: (key: string) => { delete forms[key] },
    state: (key: string): SettingsSaveState => states[key] || { status: 'idle' },
    value: <T>(key: string, confirmed: T): T => states[key]?.status === 'saving' && intents[key] !== undefined ? intents[key] as T : confirmed,
    busy: (resource: string) => Object.entries(states).some(([key, state]) => key.startsWith(`${resource}/`) && state.status === 'saving'),
  }
}

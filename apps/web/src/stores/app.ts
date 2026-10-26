import { computed, ref, watch } from 'vue'
import { acceptHMRUpdate, defineStore } from 'pinia'
import { useQuery, useQueryCache } from '@pinia/colada'
import { bootstrapQuery, bootstrapQueryKey, listComputersQuery, listSessionsQuery, listSessionsQueryKey } from '@astral/sdk/colada'
import { getNetworkSettings, getPreferences, saveNetworkSettings, savePreferences, updateSession } from '@astral/sdk'
import { useIntervalFn, usePreferredDark } from '@vueuse/core'
import { i18n } from '../i18n'
import { request } from '../lib/api'
import type { AstralInternalHarnessInputRequest } from '@astral/sdk'
import { useAuthStore } from './auth'
import type { FileRef, SessionView } from '../lib/types'

export const useAppStore = defineStore('app', () => {
  const auth = useAuthStore()
  const authorized = computed(() => auth.authenticated)
  const lastChatPath = ref('/')
  const drafts = ref<Record<string, { text: string; files: Array<{ uri: string; file: FileRef }>; attempt?: AstralInternalHarnessInputRequest; session?: SessionView; selection?: Partial<SessionView> }>>({})
  const query = useQuery({ ...bootstrapQuery(), enabled: () => authorized.value, staleTime: 15_000 })
  const sessionQuery = useQuery({ ...listSessionsQuery(), enabled: () => authorized.value, staleTime: 5000 })
  const computerQuery = useQuery({ ...listComputersQuery(), enabled: () => authorized.value, staleTime: 5000 })
  const cache = useQueryCache()
  const data = computed(() => query.data.value)
  const sessionViews = ref<Record<string, SessionView>>({})
  const sidebar = ref(window.matchMedia('(min-width: 760px)').matches && localStorage.getItem('astral:sidebar') !== 'closed')
  const prefersDark = usePreferredDark()
  const theme = ref(localStorage.getItem('astral:theme') || 'system')
  const dark = computed(() => theme.value === 'dark' || (theme.value === 'system' && prefersDark.value))
  const agents = computed(() => data.value?.agents ?? [])
  const activeAgentID = ref(localStorage.getItem('astral:agent') || '')
  watch(agents, value => {
    if (data.value && !value.some(agent => agent.id === activeAgentID.value)) activeAgentID.value = value[0]?.id || ''
  }, { immediate: true })
  watch(activeAgentID, value => localStorage.setItem('astral:agent', value))
  const computers = computed(() => computerQuery.data.value ?? data.value?.computers ?? [])
  const models = computed(() => data.value?.models ?? [])
  const availableModels = computed(() => models.value.filter(model => model.enabled !== false))
  const sessions = computed(() => (sessionQuery.data.value ?? data.value?.sessions ?? []).map(s => sessionViews.value[s.id] ?? s))
  let refreshing = false
  useIntervalFn(async () => {
    if (!authorized.value || refreshing || document.visibilityState === 'hidden') return
    refreshing = true
    try { await Promise.allSettled([sessionQuery.refetch(), computerQuery.refetch()]) }
    finally { refreshing = false }
  }, 5000)
  watch(sidebar, value => localStorage.setItem('astral:sidebar', value ? 'open' : 'closed'))
  watch(dark, value => {
    document.documentElement.dataset.theme = value ? 'dark' : 'light'
    const desktop = window.astralDesktop
    if (desktop?.platform === 'win32' || desktop?.platform === 'linux') {
      void desktop.appearance(value).catch(error => console.error('The native window appearance could not follow the app theme.', error))
    }
  }, { immediate: true })
  watch(() => data.value?.preferences, p => {
    if (!p) return
    theme.value = p.theme || 'system'
    localStorage.setItem('astral:theme', theme.value)
    if (p.language === 'zh' || p.language === 'en') {
      i18n.global.locale.value = p.language
      localStorage.setItem('astral:language', p.language)
    }
  }, { immediate: true })
  async function refresh() {
    try { await cache.invalidateQueries() }
    catch (error) {
      // Navigation can cancel a transient query while the resource lists refresh.
      if (!(error instanceof Error && error.name === 'AbortError')) throw error
    }
    await query.refetch()
  }
  async function preferences(patch: Partial<NonNullable<typeof data.value>['preferences']>) {
    if (!data.value) return
    const current = await request(getPreferences())
    const saved = await request(savePreferences({ body: { ...current, ...patch } }))
    if (data.value) cache.setQueryData(bootstrapQueryKey(), { ...data.value, preferences: saved })
    if (window.astralDesktop) {
      try { await window.astralDesktop.preferences({ keepRunning: saved.keep_running, loginStart: saved.login_start }) }
      catch (error) {
        await refresh().catch(() => undefined)
        throw { code: 'desktop_preferences', message: error instanceof Error ? error.message : 'The desktop preference could not be applied.' }
      }
    }
    await refresh().catch(() => undefined)
  }
  function confirmNetwork(network: NonNullable<typeof data.value>['network']) {
    if (data.value) cache.setQueryData(bootstrapQueryKey(), { ...data.value, network, public_url: network.public_urls.value[0] || '' })
    return network
  }
  async function refreshNetwork() { return confirmNetwork(await request(getNetworkSettings())) }
  async function networkSettings(patch: { public_urls?: string[]; allow_remote_management?: boolean }) {
    return confirmNetwork(await request(saveNetworkSettings({ body: patch })))
  }
  async function presentation(id: string, patch: { title?: string; pinned?: boolean; archived?: boolean }) {
    await request(updateSession({ path: { id }, body: patch }))
    if (sessionViews.value[id]) Object.assign(sessionViews.value[id], patch)
    cache.setQueryData(listSessionsQueryKey(), (sessionQuery.data.value ?? data.value?.sessions ?? []).map(session => session.id === id ? { ...session, ...patch } : session))
    await refresh().catch(() => undefined)
  }
  function forgetSession(id: string) {
    delete sessionViews.value[id]
    delete drafts.value[id]
    if (lastChatPath.value === `/sessions/${id}`) lastChatPath.value = '/'
    cache.setQueryData(listSessionsQueryKey(), (sessionQuery.data.value ?? []).filter(session => session.id !== id))
    if (data.value) cache.setQueryData(bootstrapQueryKey(), { ...data.value, sessions: data.value.sessions.filter(session => session.id !== id) })
  }
  function forgetAgent(id: string) {
    const owned = [...sessions.value, ...Object.values(sessionViews.value), ...Object.values(drafts.value).flatMap(draft => draft.session ? [draft.session] : [])]
    for (const session of owned) if (session.agent_id === id) forgetSession(session.id)
    delete drafts.value[`new:${id}`]
    if (data.value) cache.setQueryData(bootstrapQueryKey(), { ...data.value, agents: data.value.agents.filter(agent => agent.id !== id) })
  }
  const statuses = computed(() => Object.fromEntries((sessionQuery.data.value ?? []).map(s => [s.id, sessionViews.value[s.id] ?? s])))
  return { authorized, lastChatPath, drafts, data, agents, activeAgentID, computers, models, availableModels, sessions, sessionViews, statuses, sidebar, dark, query, refresh, preferences, refreshNetwork, networkSettings, presentation, forgetSession, forgetAgent }
})

if (import.meta.hot) import.meta.hot.accept(acceptHMRUpdate(useAppStore, import.meta.hot))

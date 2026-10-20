import { computed, ref, watch } from 'vue'
import { defineStore } from 'pinia'
import { useQuery, useQueryCache } from '@pinia/colada'
import { bootstrapQuery, listComputersQuery, listSessionsQuery } from '@astral/sdk/colada'
import { savePreferences } from '@astral/sdk'
import { useIntervalFn, usePreferredDark } from '@vueuse/core'
import { i18n } from '../i18n'
import { request } from '../lib/api'
import type { SessionView } from '../lib/types'

export const useAppStore = defineStore('app', () => {
  const authorized = ref(true)
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
  const computers = computed(() => computerQuery.data.value ?? data.value?.computers ?? [])
  const models = computed(() => data.value?.models ?? [])
  const sessions = computed(() => (sessionQuery.data.value ?? data.value?.sessions ?? []).map(s => sessionViews.value[s.id] ?? s))
  let refreshing = false
  useIntervalFn(async () => {
    if (!authorized.value || refreshing || document.visibilityState === 'hidden') return
    refreshing = true
    try { await Promise.allSettled([sessionQuery.refetch(), computerQuery.refetch()]) }
    finally { refreshing = false }
  }, 5000)
  watch(sidebar, value => localStorage.setItem('astral:sidebar', value ? 'open' : 'closed'))
  watch(dark, value => document.documentElement.dataset.theme = value ? 'dark' : 'light', { immediate: true })
  watch(() => data.value?.preferences, p => {
    if (!p) return
    theme.value = p.theme || 'system'
    localStorage.setItem('astral:theme', theme.value)
    if (p.language === 'zh' || p.language === 'en') {
      i18n.global.locale.value = p.language
      localStorage.setItem('astral:language', p.language)
    }
  }, { immediate: true })
  window.addEventListener('astral:unauthorized', () => { authorized.value = false })
  async function refresh() { await cache.invalidateQueries(); await query.refetch() }
  async function preferences(patch: Partial<NonNullable<typeof data.value>['preferences']>) {
    if (!data.value) return
    const saved = await request(savePreferences({ body: { ...data.value.preferences, ...patch } }))
    if (window.astralDesktop) await window.astralDesktop.preferences({ keepRunning: saved.keep_running, loginStart: saved.login_start })
    await refresh()
  }
  const statuses = computed(() => Object.fromEntries((sessionQuery.data.value ?? []).map(s => [s.id, sessionViews.value[s.id] ?? s])))
  return { authorized, data, agents, computers, models, sessions, sessionViews, statuses, sidebar, dark, query, refresh, preferences }
})

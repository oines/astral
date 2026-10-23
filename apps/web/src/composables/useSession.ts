import { onScopeDispose, ref, watch, type Ref } from 'vue'
import { sessionHistory } from '@astral/sdk'
import { addEntry, applyEvent, emptyState, fromSnapshot } from '../lib/session-state'
import { errorText, request } from '../lib/api'
import type { SessionEvent, Snapshot } from '../lib/types'
import { useAppStore } from '../stores/app'

export function useSession(sessionID: Ref<string>) {
  const state = ref(emptyState())
  const connection = ref('connecting')
  const error = ref('')
  const app = useAppStore()
  let source: EventSource | undefined
  let timer: ReturnType<typeof setTimeout> | undefined
  let generation = 0
  function close() {
    const closing = source
    source = undefined
    closing?.close()
    clearTimeout(timer)
  }
  function connect() {
    close()
    if (!sessionID.value) return
    const current = generation
    const transport = new EventSource(`/api/v1/sessions/${encodeURIComponent(sessionID.value)}/events`)
    source = transport
    const active = () => current === generation && source === transport
    source.addEventListener('snapshot', event => {
      if (!active()) return
      try {
        state.value = fromSnapshot(JSON.parse((event as MessageEvent<string>).data) as Snapshot)
        connection.value = 'connected'
        error.value = ''
        if (state.value.view) app.sessionViews[state.value.view.id] = state.value.view
      } catch (e) { error.value = errorText(e); reconnect() }
    })
    const receive = (event: Event) => {
      if (!active()) return
      try {
        if (!applyEvent(state.value, JSON.parse((event as MessageEvent<string>).data) as SessionEvent)) { reconnect(); return }
        if (state.value.view) app.sessionViews[state.value.view.id] = state.value.view
      } catch (e) { error.value = errorText(e); reconnect() }
    }
    source.addEventListener('event', receive)
    source.addEventListener('state', receive)
    source.addEventListener('resync', () => { if (active()) reconnect() })
    source.onerror = () => {
      // A proxy can return 502 during a restart, after which native EventSource
      // may stop retrying. Every recovery starts with a fresh server snapshot.
      if (active()) reconnect()
    }
  }
  function reconnect() {
    close()
    state.value.previews = {}
    connection.value = 'reconnecting'
    timer = setTimeout(connect, 1000)
  }
  const loadingHistory = ref(false)
  async function older() {
    if (!state.value.historyCursor || loadingHistory.value) return false
    loadingHistory.value = true
    const current = generation
    try {
      const page = await request(sessionHistory({ path: { id: sessionID.value }, query: { cursor: state.value.historyCursor, limit: 50 } }))
      if (current !== generation) return false
      for (const entry of page.entries ?? []) addEntry(state.value, entry, true)
      state.value.historyCursor = page.next_cursor
      return true
    } catch (e) { if (current === generation) error.value = errorText(e); return false }
    finally { if (current === generation) loadingHistory.value = false }
  }
  async function moreTrace() {
    if (!state.value.traceCursor) return
    const current = generation
    const page = await request(sessionHistory({ path: { id: sessionID.value }, query: { cursor: state.value.traceCursor, limit: 100, trace: true } }))
    if (current !== generation) return
    for (const entry of page.entries ?? []) addEntry(state.value, entry)
    state.value.traceCursor = page.next_cursor
  }
  watch(sessionID, (_, previous) => {
    if (previous) delete app.sessionViews[previous]
    generation++
    loadingHistory.value = false
    state.value = emptyState()
    error.value = ''
    connection.value = 'connecting'
    connect()
  }, { immediate: true })
  onScopeDispose(() => { close(); if (sessionID.value) delete app.sessionViews[sessionID.value] })
  return { state, connection, error, older, moreTrace, loadingHistory, reconnect }
}

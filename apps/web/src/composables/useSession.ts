import { effectScope, onScopeDispose, ref, watch, type Ref } from 'vue'
import { client } from '@astral/sdk/client'
import type { SessionHistoryResponses, SessionHistoryErrors } from '@astral/sdk'
import { addEntry, applyEvent, emptyState, interruptPreviews, reconcileSnapshot } from '../lib/session-state'
import { errorText, request } from '../lib/api'
import type { SessionEvent, Snapshot } from '../lib/types'
import { useAppStore } from '../stores/app'
import { useAuthStore } from '../stores/auth'
import { createSessionEventBatch } from '../lib/session-event-batch'
import { createSessionCache, type HistoryBoundary } from '../lib/session-cache'

const caches = new WeakMap<ReturnType<typeof useAppStore>, ReturnType<typeof createSessionCache>>()
function accountIdentity(app: ReturnType<typeof useAppStore>, auth: ReturnType<typeof useAuthStore>) {
  return app.authorized ? `${auth.status?.desktop ? 'desktop' : 'web'}:${auth.status?.username ?? ''}` : ''
}
function sessionCache(app: ReturnType<typeof useAppStore>, auth: ReturnType<typeof useAuthStore>) {
  let cache = caches.get(app)
  if (cache) return cache
  cache = createSessionCache()
  caches.set(app, cache)
  const owned = cache
  // The app/account outlive ChatPage. Invalidate even if logout happens from a
  // settings page after the last session component has been disposed.
  effectScope(true).run(() => watch(() => accountIdentity(app, auth), () => owned.clear(), { flush: 'sync' }))
  app.$onAction?.(({ name, args, after }) => after(() => {
    if (name === 'forgetSession') owned.remove(args[0] as string)
    if (name === 'forgetAgent') owned.removeAgent(args[0] as string)
  }), true)
  return cache
}

export function useSession(sessionID: Ref<string>) {
  const state = ref(emptyState())
  const connection = ref('connecting')
  const error = ref('')
  const app = useAppStore()
  const auth = useAuthStore()
  const cache = sessionCache(app, auth)
  let source: EventSource | undefined
  let timer: ReturnType<typeof setTimeout> | undefined
  let generation = 0
  let historyEpoch = 0
  let historyRequest: AbortController | undefined
  let olderPending: Promise<boolean> | undefined
  let historyBoundary: HistoryBoundary | undefined
  const events = createSessionEventBatch(batch => {
    try {
      for (const event of batch) if (!applyEvent(state.value, event)) { reconnect(); return }
      if (state.value.view) app.sessionViews[state.value.view.id] = state.value.view
    } catch (e) { error.value = errorText(e); reconnect() }
  })
  function resetHistoryRequest() { historyEpoch++; historyRequest?.abort(); historyRequest = undefined; olderPending = undefined; loadingHistory.value = false }
  function history(cursor: string, trace: boolean, signal?: AbortSignal) {
    return request(client.get<SessionHistoryResponses, SessionHistoryErrors>({ url: `/api/v1/sessions/${encodeURIComponent(sessionID.value)}/timeline`, query: { cursor, limit: trace ? 100 : 60, trace }, signal }))
  }
  function close() {
    const closing = source
    source = undefined
    closing?.close()
    clearTimeout(timer)
  }
  function connect() {
    close()
    if (!sessionID.value || !app.authorized || !retainCurrent) return
    const current = generation
    const transport = new EventSource(`/api/v1/sessions/${encodeURIComponent(sessionID.value)}/events`)
    source = transport
    const active = () => current === generation && source === transport
    source.addEventListener('snapshot', event => {
      if (!active()) return
      try {
        events.flush()
        if (!active()) return
        const snapshot = JSON.parse((event as MessageEvent<string>).data) as Snapshot
        const previous = state.value
        const known = Object.values(previous.entries)
        const overlap = (snapshot.history.entries ?? []).some(entry => previous.entries[entry.id])
        if (known.length && !overlap && snapshot.history.next_cursor) {
          historyBoundary ??= { sequence: known.reduce((latest, entry) => Math.max(latest, entry.sequence), 0), cursor: previous.historyCursor }
        }
        resetHistoryRequest()
        state.value = reconcileSnapshot(previous, snapshot)
        // A long outage may exceed the snapshot tail. Fill that gap in the
        // background while keeping previously read pages and live output on screen.
        if (historyBoundary) void fillGap(historyEpoch)
        connection.value = 'connected'
        error.value = ''
        if (state.value.view) app.sessionViews[state.value.view.id] = state.value.view
      } catch (e) { error.value = errorText(e); reconnect() }
    })
    const receive = (event: Event) => {
      if (!active()) return
      try {
        events.push(JSON.parse((event as MessageEvent<string>).data) as SessionEvent)
      } catch (e) { error.value = errorText(e); reconnect() }
    }
    source.addEventListener('event', receive)
    source.addEventListener('state', receive)
    source.addEventListener('resync', () => { if (active()) reconnect() })
    source.onerror = () => {
      // A proxy can return 502 during a restart, after which native EventSource
      // may stop retrying. Every recovery starts with a fresh server snapshot.
      if (active()) { void auth.refresh(); reconnect() }
    }
  }
  function reconnect() {
    events.flush()
    interruptPreviews(state.value)
    close()
    if (!sessionID.value || !app.authorized || !retainCurrent) return
    connection.value = 'reconnecting'
    timer = setTimeout(connect, 1000)
  }
  const loadingHistory = ref(false)
  function older(): Promise<boolean> {
    // The scroll prefetch and reconnect gap recovery share one request rather
    // than treating the other's in-flight work as a failed history load.
    if (olderPending) return olderPending
    const cursor = state.value.historyCursor
    if (!cursor) return Promise.resolve(false)
    const pending = loadOlderPage(cursor)
    olderPending = pending
    void pending.finally(() => { if (olderPending === pending) olderPending = undefined })
    return pending
  }
  async function loadOlderPage(cursor: string) {
    loadingHistory.value = true
    const current = generation
    const epoch = historyEpoch
    const controller = new AbortController()
    historyRequest = controller
    try {
      const page = await history(cursor, false, controller.signal)
      if (current !== generation || epoch !== historyEpoch || cursor !== state.value.historyCursor) return true
      for (const entry of page.entries ?? []) addEntry(state.value, entry, true)
      const crossed = historyBoundary && (page.entries ?? []).some(entry => entry.sequence <= historyBoundary!.sequence)
      state.value.historyCursor = crossed ? historyBoundary!.cursor : page.next_cursor
      if (crossed || !page.next_cursor) historyBoundary = undefined
      return true
    } catch (e) {
      if (current !== generation || epoch !== historyEpoch || controller.signal.aborted) return true
      error.value = errorText(e)
      return false
    }
    finally { if (current === generation && epoch === historyEpoch) { loadingHistory.value = false; historyRequest = undefined } }
  }
  async function fillGap(epoch: number) {
    while (epoch === historyEpoch && historyBoundary && state.value.historyCursor) {
      if (!await older()) return
    }
  }
  async function moreTrace() {
    if (!state.value.traceCursor) return
    const current = generation
    const epoch = historyEpoch
    const page = await history(state.value.traceCursor, true)
    if (current !== generation || epoch !== historyEpoch) return
    for (const entry of page.entries ?? []) addEntry(state.value, entry)
    state.value.traceCursor = page.next_cursor
  }
  let activeID = ''
  let owner = accountIdentity(app, auth)
  let retainCurrent = true
  app.$onAction?.(({ name, args, after }) => after(() => {
    if ((name === 'forgetSession' && args[0] === activeID) || (name === 'forgetAgent' && args[0] === state.value.view?.agent_id)) {
      retainCurrent = false
      leave(false)
      historyBoundary = undefined
      state.value = emptyState()
      connection.value = 'disconnected'
      error.value = ''
    }
  }))
  function leave(retain: boolean) {
    if (retain) events.flush()
    else events.clear()
    interruptPreviews(state.value)
    if (retain && retainCurrent && activeID) cache.put(activeID, { state: state.value, historyBoundary })
    if (activeID) delete app.sessionViews[activeID]
    generation++
    resetHistoryRequest()
    close()
  }
  watch([sessionID, () => accountIdentity(app, auth)], ([id, nextOwner]) => {
    leave(!!owner && owner === nextOwner)
    owner = nextOwner
    activeID = id
    retainCurrent = true
    historyBoundary = undefined
    const restored = owner && id ? cache.take(id) : undefined
    state.value = restored?.state ?? emptyState()
    historyBoundary = restored?.historyBoundary
    error.value = ''
    connection.value = 'connecting'
    if (state.value.view) app.sessionViews[id] = state.value.view
    connect()
  }, { immediate: true, flush: 'sync' })
  const visibility = () => { if (document.visibilityState !== 'hidden') events.flush() }
  if (typeof document !== 'undefined') document.addEventListener('visibilitychange', visibility)
  onScopeDispose(() => {
    leave(!!owner && owner === accountIdentity(app, auth))
    if (typeof document !== 'undefined') document.removeEventListener('visibilitychange', visibility)
  })
  return { state, connection, error, older, moreTrace, loadingHistory, reconnect }
}

import { computed, onScopeDispose, ref, watch } from 'vue'
import { defineStore } from 'pinia'
import { useAuthStore } from './auth'
import { errorText } from '../lib/api'
import { wakeAPI, type WakeEndpoint, type WakePatch } from '../lib/wake-endpoints'

// One Owner-scoped directory feeds both sidebar markers and endpoint editing.
// SSE starts with a complete snapshot; opening another view adds no GET.
export const useWakeEndpointStore = defineStore('wake-endpoints', () => {
  const auth = useAuthStore()
  const identity = computed(() => auth.authenticated ? `${auth.status?.desktop ? 'desktop' : 'web'}:${auth.status?.username || ''}` : '')
  const items = ref<WakeEndpoint[]>([])
  const ready = ref(false)
  const error = ref('')
  const statusObservers = ref(0)
  const deletedComputerIDs = new Set<string>()
  const visibleItems = (rows: WakeEndpoint[]) => rows.filter(item => !deletedComputerIDs.has(item.computer_id))
  let generation = 0
  let requestID = 0
  let stream: EventSource | undefined
  let retry: ReturnType<typeof setTimeout> | undefined
  function disconnect() { stream?.close(); stream = undefined; clearTimeout(retry) }
  async function refresh() {
    const scope = generation, request = ++requestID
    if (!identity.value) return
    try {
      const result = await wakeAPI.list()
      if (scope === generation && request === requestID) { items.value = visibleItems(result); ready.value = true; error.value = '' }
    } catch (e) {
      if (scope === generation && request === requestID) error.value = errorText(e)
      throw e
    }
  }
  function connect() {
    disconnect()
    if (!identity.value) return
    const scope = generation
    const current = new EventSource(`/api/v1/wake-endpoints/events${statusObservers.value ? '' : '?configuration_only=true'}`)
    stream = current
    current.addEventListener('wake-endpoints', event => {
      if (scope !== generation || stream !== current) return
      try { requestID++; items.value = visibleItems(JSON.parse((event as MessageEvent<string>).data) as WakeEndpoint[]); ready.value = true; error.value = '' }
      catch { void refresh().catch(() => undefined) }
    })
    current.onerror = () => {
      if (scope !== generation || stream !== current) return
      disconnect()
      void refresh().catch(() => undefined)
      retry = setTimeout(connect, 3000)
    }
  }
  watch(identity, () => {
    generation++; requestID++; disconnect(); items.value = []; ready.value = false; error.value = ''
    deletedComputerIDs.clear()
    connect()
  }, { immediate: true, flush: 'sync' })
  watch(() => statusObservers.value > 0, connect, { flush: 'sync' })
  onScopeDispose(() => { generation++; requestID++; disconnect() })
  async function update(endpoint: WakeEndpoint, patch: WakePatch) {
    const scope = generation
    const saved = await wakeAPI.update(endpoint, patch)
    if (scope === generation && identity.value && !deletedComputerIDs.has(saved.computer_id)) {
      const latest = items.value.find(item => item.id === saved.id)
      if (!latest || latest.revision < saved.revision) items.value = [...items.value.filter(item => item.id !== saved.id), saved]
    }
    return saved
  }
  async function remove(endpoint: WakeEndpoint) {
    const scope = generation
    await wakeAPI.remove(endpoint)
    if (scope === generation && identity.value) items.value = items.value.filter(item => item.id !== endpoint.id)
  }
  function forgetSessions(ids: Set<string>) { items.value = items.value.filter(item => !ids.has(item.session_id)) }
  function forgetAgent(id: string) { items.value = items.value.filter(item => item.agent_id !== id) }
  function forgetComputer(id: string) { deletedComputerIDs.add(id); requestID++; items.value = visibleItems(items.value) }
  function observeStatus() {
    statusObservers.value++
    let released = false
    return () => { if (!released) { released = true; statusObservers.value-- } }
  }
  return { items, ready, error, refresh, update, remove, forgetSessions, forgetAgent, forgetComputer, observeStatus }
})

import { onActivated, onDeactivated, onMounted, onScopeDispose, ref, watch, type Ref } from 'vue'
import { errorText } from '../lib/api'
import { wakeAPI, type WakeEndpoint, type WakePatch } from '../lib/wake-endpoints'

// Page-scoped observation: leaving the panel releases its connection and
// switching Bots cannot let a late response replace the current directory.
export function useWakeEndpoints(agentID: Ref<string>, authorized: Ref<boolean>) {
  const items = ref<WakeEndpoint[]>([])
  const ready = ref(false)
  const error = ref('')
  let active = false
  let generation = 0
  let requestID = 0
  let stream: EventSource | undefined
  let retry: ReturnType<typeof setTimeout> | undefined
  function disconnect() { stream?.close(); stream = undefined; clearTimeout(retry) }
  async function refresh() {
    const scope = generation, request = ++requestID, id = agentID.value
    if (!id || !authorized.value) return
    try {
      const result = await wakeAPI.list(id)
      if (scope === generation && request === requestID && active) { items.value = result; ready.value = true; error.value = '' }
    } catch (e) {
      if (scope === generation && request === requestID && active) error.value = errorText(e)
      throw e
    }
  }
  function connect() {
    disconnect()
    if (!active || !authorized.value || !agentID.value) return
    const scope = generation
    const current = new EventSource(`/api/v1/wake-endpoints/events?agent_id=${encodeURIComponent(agentID.value)}`)
    stream = current
    current.addEventListener('wake-endpoints', event => {
      if (scope !== generation || stream !== current) return
      try { requestID++; items.value = JSON.parse((event as MessageEvent<string>).data) as WakeEndpoint[]; ready.value = true; error.value = '' }
      catch { void refresh().catch(() => undefined) }
    })
    current.onerror = () => {
      if (scope !== generation || stream !== current) return
      disconnect()
      void refresh().catch(() => undefined)
      retry = setTimeout(connect, 3000)
    }
  }
  function restart() {
    generation++; requestID++; disconnect(); items.value = []; ready.value = false; error.value = ''
    // The event stream starts with a full snapshot; avoid fetching the same
    // directory twice every time the page is opened or a Bot is selected.
    if (active && authorized.value && agentID.value) connect()
  }
  function start() { if (!active) { active = true; restart() } }
  function stop() { active = false; generation++; requestID++; disconnect() }
  watch([agentID, authorized], restart)
  onMounted(start)
  onActivated(start)
  onDeactivated(stop)
  onScopeDispose(stop)
  async function update(endpoint: WakeEndpoint, patch: WakePatch) {
    const scope = generation
    const saved = await wakeAPI.update(endpoint, patch)
    if (scope === generation && active) {
      const latest = items.value.find(item => item.id === saved.id)
      if (!latest || latest.revision < saved.revision) items.value = [...items.value.filter(item => item.id !== saved.id), saved]
    }
    return saved
  }
  async function remove(endpoint: WakeEndpoint) {
    const scope = generation
    await wakeAPI.remove(endpoint)
    if (scope === generation && active) items.value = items.value.filter(item => item.id !== endpoint.id)
  }
  return { items, ready, error, refresh, update, remove }
}

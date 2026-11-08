import { computed, onActivated, onDeactivated, onMounted, onScopeDispose, type Ref } from 'vue'
import { useWakeEndpointStore } from '../stores/wake-endpoints'

// A view of the shared Owner directory; selecting a Bot never reconnects SSE.
export function useWakeEndpoints(agentID: Ref<string>, authorized: Ref<boolean>) {
  const directory = useWakeEndpointStore()
  let release: (() => void) | undefined
  function start() { release ??= directory.observeStatus() }
  function stop() { release?.(); release = undefined }
  onMounted(start)
  onActivated(start)
  onDeactivated(stop)
  onScopeDispose(stop)
  return {
    items: computed(() => authorized.value ? directory.items.filter(item => item.agent_id === agentID.value) : []),
    ready: computed(() => authorized.value && directory.ready),
    error: computed(() => directory.error),
    refresh: directory.refresh, update: directory.update, remove: directory.remove,
  }
}

import { computed, ref, shallowRef } from 'vue'
import { computerHost, type AstralInternalDomainHostInfo as HostInfo } from '@astral/sdk'
import { errorText, request } from '../lib/api'

// This asks the running Server to use its own Docker CLI, account and context.
// The bootstrap result is only the initial hint; every explicit check is fresh.
export function useContainerRuntimeCheck(initial: () => HostInfo | undefined, load = () => request(computerHost({ signal: AbortSignal.timeout(20_000) }))) {
  const result = shallowRef<HostInfo>()
  const checking = ref(false)
  const error = ref('')
  const host = computed(() => result.value ?? initial())
  const available = computed(() => !checking.value && !error.value && host.value?.container_available === true)
  let pending: Promise<boolean> | undefined
  function check(): Promise<boolean> {
    if (pending) return pending
    checking.value = true
    error.value = ''
    pending = Promise.resolve().then(load).then(value => {
      result.value = value
      return value.container_available
    }).catch(cause => {
      error.value = errorText(cause)
      return false
    }).finally(() => { checking.value = false; pending = undefined })
    return pending
  }
  return { host, checking, error, available, check }
}

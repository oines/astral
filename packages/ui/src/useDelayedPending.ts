import { readonly, ref, toValue, watch, type MaybeRefOrGetter } from 'vue'

// A pending key can identify a particular request or destination. Changing it
// starts a fresh delay, so a previous slow request cannot flash the next one.
export function useDelayedPending(pending: MaybeRefOrGetter<boolean | string>, delay = 300) {
  const visible = ref(false)
  watch(() => toValue(pending), (value, _, onCleanup) => {
    visible.value = false
    if (!value) return
    const timer = setTimeout(() => { visible.value = true }, delay)
    onCleanup(() => clearTimeout(timer))
  }, { immediate: true, flush: 'sync' })
  return readonly(visible)
}

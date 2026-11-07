import { computed, ref, toValue, watch, type MaybeRefOrGetter } from 'vue'
import { errorText } from '../lib/api'

export interface FileRevealTarget { computerID: string; sessionID: string; fileID?: string; path?: string; directory?: boolean }

// Capability comes from the Desktop main process, which verifies that the
// source Computer is its managed local Computer. IDs/hostnames alone are hints.
export function useFileReveal(target: MaybeRefOrGetter<FileRevealTarget | undefined>) {
  const available = ref(false)
  const busy = ref(false)
  const error = ref('')
  const platform = typeof window === 'undefined' ? '' : window.astralDesktop?.platform || ''
  const labelKey = computed(() => {
    const directory = toValue(target)?.directory
    if (platform === 'darwin') return directory ? 'filesOpenFinder' : 'filesRevealFinder'
    if (platform === 'win32') return directory ? 'filesOpenExplorer' : 'filesRevealExplorer'
    return directory ? 'filesOpenFileManager' : 'filesRevealFileManager'
  })
  let generation = 0
  watch(() => toValue(target), async (value, _, onCleanup) => {
    let current = true
    onCleanup(() => { current = false })
    available.value = false
    // Closing a context menu must retain an error from its requested native
    // action. A different source, however, starts an independent operation.
    if (!value) return
    generation++
    error.value = ''
    busy.value = false
    const bridge = typeof window === 'undefined' ? undefined : window.astralDesktop?.files
    if (!bridge) return
    try {
      const result = await bridge.canReveal(value)
      if (current) available.value = result
    } catch { /* Optional native capability is absent for remote/unavailable sources. */ }
  }, { immediate: true })
  async function reveal() {
    const value = toValue(target)
    const bridge = typeof window === 'undefined' ? undefined : window.astralDesktop?.files
    if (!value || !bridge || busy.value) return
    const current = generation
    busy.value = true
    error.value = ''
    try { await bridge.reveal(value) }
    catch (cause) { if (current === generation) error.value = errorText(cause) }
    finally { if (current === generation) busy.value = false }
  }
  return { available, busy, error, labelKey, reveal }
}

import { onScopeDispose, ref } from 'vue'
import { copyToClipboard } from '../lib/browser'

// A shared dialog keeps the requested text available when the browser blocks
// both clipboard APIs. It never reports success for a rejected copy.
export const manualCopyText = ref<string>()

export function useClipboard() {
  const copied = ref(false)
  let timer: ReturnType<typeof setTimeout> | undefined
  onScopeDispose(() => clearTimeout(timer))
  async function copy(text: string) {
    clearTimeout(timer)
    copied.value = false
    const success = await copyToClipboard(text)
    if (success) {
      copied.value = true
      timer = setTimeout(() => { copied.value = false }, 2000)
    } else manualCopyText.value = text
    return success
  }
  return { copy, copied }
}

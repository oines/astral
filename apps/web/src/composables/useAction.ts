import { ref } from 'vue'
import { errorText } from '../lib/api'
export function useAction() {
  const busy = ref(false)
  const error = ref('')
  const success = ref('')
  async function run<T>(fn: () => Promise<T>, message = ''): Promise<T | undefined> {
    busy.value = true
    error.value = ''
    success.value = ''
    try {
      const value = await fn()
      if (message) success.value = message
      return value
    } catch (e) { success.value = ''; error.value = errorText(e); return undefined }
    finally { busy.value = false }
  }
  return { busy, error, success, run }
}

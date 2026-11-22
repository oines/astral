import { computed, reactive, ref } from 'vue'
import { errorText } from '../lib/api'

export interface StorageActionScope {
  ready: boolean
  reload: () => Promise<void>
}

export function useStorageAction(scope: StorageActionScope) {
  const busy = ref('')
  const confirming = ref('')
  const errors = reactive<Record<string, string>>({})
  const locked = computed(() => !!busy.value || !scope.ready)
  function confirm(key: string, open: boolean) {
    if (open && !locked.value) {
      delete errors[key]
      confirming.value = key
    } else if (!open && !busy.value && confirming.value === key) confirming.value = ''
  }
  async function run(key: string, action: () => Promise<void>) {
    if (locked.value) return
    busy.value = key
    delete errors[key]
    try {
      await action()
      if (confirming.value === key) confirming.value = ''
    } catch (error) {
      // The Server may already have completed a destructive request. Read
      // its current state before allowing a newly confirmed action.
      confirming.value = ''
      errors[key] = errorText(error)
      await scope.reload()
    } finally { busy.value = '' }
  }
  return { busy, locked, confirming, errors, confirm, run }
}

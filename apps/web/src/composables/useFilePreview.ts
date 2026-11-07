import { onScopeDispose, ref, shallowRef, toValue, watch, type MaybeRefOrGetter } from 'vue'
import { errorText } from '../lib/api'
import type { FileRef } from '../lib/types'
import { fileService, type FilePreviewResource, type FileService } from './useFileService'

export function useFilePreview(file: MaybeRefOrGetter<FileRef>, sessionID: MaybeRefOrGetter<string>, service: Pick<FileService, 'preview'> = fileService) {
  const preview = shallowRef<FilePreviewResource>()
  const busy = ref(false)
  const error = ref('')
  let generation = 0
  let controller: AbortController | undefined
  async function load(preserve = false) {
    const current = ++generation
    controller?.abort()
    const requestController = new AbortController()
    controller = requestController
    if (!preserve) preview.value = undefined
    error.value = ''
    busy.value = true
    try {
      const result = await service.preview(toValue(file), toValue(sessionID), requestController.signal)
      if (current === generation) { preview.value = result; return result }
    } catch (cause) {
      if (current === generation && !requestController.signal.aborted) error.value = errorText(cause)
    } finally {
      if (current === generation) busy.value = false
    }
  }
  watch(() => {
    const value = toValue(file)
    return JSON.stringify([toValue(sessionID), value.id, value.revision, value.version, value.path])
  }, () => load(), { immediate: true, flush: 'sync' })
  onScopeDispose(() => { generation++; controller?.abort() })
  return { preview, busy, error, reload: () => load(true) }
}

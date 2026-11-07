<script setup lang="ts">
import { computed, onActivated, onBeforeUnmount, onDeactivated, onErrorCaptured, onMounted, ref, shallowRef, watch, type Component } from 'vue'
import { useI18n } from 'vue-i18n'
import { Button, useDelayedPending } from '@astral/ui'
import { Check, Copy, Download, ExternalLink, RefreshCw } from '@astral/icons'
import type { FileRef } from '../../lib/types'
import { errorText, fileURL } from '../../lib/api'
import { useFilePreview } from '../../composables/useFilePreview'
import { useFileReveal } from '../../composables/useFileReveal'
import { useClipboard } from '../../composables/useClipboard'
import { selectFilePreview } from './preview-registry'
import { createPreviewScrollState } from './preview-scroll'
import DownloadFilePreview from './renderers/DownloadFilePreview.vue'

const props = withDefaults(defineProps<{ file: FileRef; sessionID: string; computerName?: string }>(), { computerName: '' })
const { t } = useI18n()
const scrollHost = ref<HTMLElement>()
const scroll = createPreviewScrollState(() => scrollHost.value)
onMounted(scroll.activate)
onActivated(scroll.activate)
onDeactivated(scroll.deactivate)
onBeforeUnmount(scroll.deactivate)
watch([() => props.sessionID, () => props.file.computer_id, () => props.file.path], scroll.reset, { flush: 'sync' })
const { preview, busy, error, reload } = useFilePreview(() => props.file, () => props.sessionID)
const { copy, copied } = useClipboard()
const copying = ref(false)
const copyConfirmed = ref(false)
const copyError = ref('')
let copyGeneration = 0
const copyLabel = computed(() => preview.value?.partial ? 'filesCopyPreview' : 'filesCopyContent')
function resetCopy() {
  copyGeneration++
  copied.value = false
  copying.value = false
  copyConfirmed.value = false
  copyError.value = ''
}
// The shared clipboard's timer expires this feedback. A late copy from an old
// file may update its ref, but can never confirm the current file's action.
watch(copied, value => { if (!value) copyConfirmed.value = false }, { flush: 'sync' })
watch([() => props.sessionID, () => props.file.id, () => props.file.revision, () => props.file.version, () => props.file.path], resetCopy, { flush: 'sync' })
onDeactivated(resetCopy)
onBeforeUnmount(resetCopy)
async function copyContent() {
  const content = preview.value?.content
  if (content === undefined || busy.value || copying.value) return
  const generation = ++copyGeneration
  copying.value = true
  copyError.value = ''
  try {
    const success = await copy(content)
    if (generation === copyGeneration) copyConfirmed.value = success
  } catch (cause) { if (generation === copyGeneration) copyError.value = errorText(cause) }
  finally { if (generation === copyGeneration) copying.value = false }
}
const rendererError = ref('')
const rendererGeneration = ref(0)
watch([busy, preview], ([loading, resource]) => {
  if (loading || !resource) scroll.suspend()
  else void scroll.resume()
}, { immediate: true, flush: 'sync' })
watch([() => props.sessionID, () => props.file.id], () => { rendererError.value = ''; rendererGeneration.value++ }, { flush: 'sync' })
onErrorCaptured(cause => {
  rendererError.value = errorText(cause)
  scroll.suspend()
  return false
})
async function refresh() {
  if (busy.value) return
  resetCopy()
  scroll.record()
  const identity = `${props.sessionID}\u0000${props.file.id}`
  const retryingRenderer = Boolean(rendererError.value)
  const loaded = await reload()
  if (!loaded || identity !== `${props.sessionID}\u0000${props.file.id}`) return
  rendererError.value = ''
  if (retryingRenderer || renderer.value?.refreshRemount) rendererGeneration.value++
}
const waiting = useDelayedPending(() => busy.value ? `${props.sessionID}:${props.file.id}` : false)
const renderer = computed(() => preview.value && selectFilePreview(preview.value))
const rendererComponent = shallowRef<Component>()
watch([renderer, rendererGeneration], ([next]) => {
  if (!next) { rendererComponent.value = undefined; return }
  try { rendererComponent.value = next.createComponent?.() || next.component }
  catch (cause) { rendererError.value = errorText(cause) }
}, { immediate: true })
const format = computed(() => {
  if (!preview.value || !renderer.value) return ''
  return renderer.value.format?.(preview.value) || t(renderer.value.labelKey)
})
const native = useFileReveal(() => ({ computerID: props.file.computer_id, sessionID: props.sessionID, fileID: props.file.id }))
</script>
<template>
  <section
    ref="scrollHost"
    class="file-preview"
    :aria-busy="busy || undefined"
    @scroll.passive="scroll.record"
  >
    <div class="file-preview-toolbar">
      <span class="file-preview-source small muted">{{ computerName || file.computer_id }}<span v-if="format"> · {{ format }}</span></span>
      <div class="row">
        <Button
          variant="ghost"
          size="icon"
          :busy="busy"
          :aria-label="t('filesRefreshPreview')"
          @click="refresh"
        >
          <RefreshCw />
        </Button>
        <Button
          v-if="preview?.content !== undefined"
          variant="ghost"
          size="icon"
          :busy="copying"
          :disabled="busy"
          :aria-label="t(copyLabel)"
          :title="t(copyLabel)"
          @click="copyContent"
        >
          <Check v-if="copied && copyConfirmed" /><Copy v-else />
        </Button>
        <Button
          v-if="native.available.value"
          variant="ghost"
          size="icon"
          :busy="native.busy.value"
          :aria-label="t(native.labelKey.value)"
          @click="native.reveal"
        >
          <ExternalLink />
        </Button>
        <a
          :href="fileURL(file.id, sessionID, true)"
          class="button button-ghost button-icon"
          :aria-label="t('download')"
        ><Download /></a>
      </div>
    </div>
    <p
      class="file-preview-path small muted mono"
      tabindex="0"
    >
      {{ file.path }}
    </p>
    <div
      v-if="error || rendererError || copyError || native.error.value"
      class="file-preview-error"
      role="alert"
    >
      <p class="error-box">
        {{ error || (rendererError ? t('filesPreviewFailed') : copyError || native.error.value) }}
      </p>
      <Button
        v-if="error || rendererError"
        :busy="busy"
        size="small"
        @click="refresh"
      >
        {{ t('retry') }}
      </Button>
    </div>
    <div
      v-if="waiting"
      class="file-preview-loading"
      role="status"
      :aria-label="t('loading')"
    >
      <span
        class="spinner"
        aria-hidden="true"
      />
    </div>
    <p
      v-if="preview?.partial && preview.content !== undefined"
      class="notice"
    >
      {{ t('previewPartial') }}
    </p>
    <DownloadFilePreview
      v-if="rendererError"
      :message="t('filesPreviewDownload')"
    />
    <component
      :is="rendererComponent"
      v-else-if="preview && renderer"
      :key="`${file.id}:${rendererGeneration}`"
      :preview="preview"
      :session-i-d="sessionID"
    />
  </section>
</template>
<style scoped>
.file-preview { display: flex; flex-direction: column; gap: 14px; min-width: 0; min-height: 0; padding: 12px 20px 24px; overflow: auto; scrollbar-width: thin; height: 100%; }
.file-preview-toolbar { position: sticky; top: -12px; z-index: 1; background: var(--bg); display: flex; align-items: center; justify-content: space-between; min-height: 34px; gap: 12px; }
.file-preview-toolbar .row { gap: 2px; }
.file-preview-source { min-width: 0; overflow-wrap: anywhere; }
.file-preview-path { min-width: 0; flex-shrink: 0; white-space: nowrap; overflow-x: auto; overflow-y: hidden; scrollbar-width: thin; margin: 0; }
.file-preview-error { display: flex; align-items: flex-start; gap: 12px; }
.file-preview-error p { flex: 1; min-width: 0; }
.file-preview-loading { display: grid; place-items: center; min-height: 80px; }
.file-preview :deep(video), .file-preview :deep(audio) { width: 100%; }
.file-preview :deep(.code-viewer) { flex: 1; min-height: 240px; }
</style>

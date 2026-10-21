<script setup lang="ts">
import { computed, defineAsyncComponent, ref, watch } from 'vue'
import { useWindowSize } from '@vueuse/core'
import { useAppStore } from '../stores/app'
import { useI18n } from 'vue-i18n'
import { fileContent } from '@astral/sdk'
import { Button } from '@astral/ui'
import { Download, X } from '@astral/icons'
import { errorText, fileURL } from '../lib/api'
import { useSurfaceStore } from '../stores/surface'
import Markdown from './Markdown.vue'
const CodeViewer = defineAsyncComponent(() => import('./CodeViewer.vue'))
const surface = useSurfaceStore()
const app = useAppStore()
const { width: windowWidth } = useWindowSize()
const maximumWidth = computed(() => windowWidth.value <= 1200 ? windowWidth.value - 16 : Math.max(320, windowWidth.value - (app.sidebar ? 256 : 0) - 456))
const panelWidth = computed(() => Math.min(surface.width, maximumWidth.value))
const { t } = useI18n()
const busy = ref(false)
const error = ref('')
const content = ref('')
const mediaType = ref('')
const fileName = ref('')
const partial = ref(false)
const title = computed(() => surface.current?.ref?.display_name || fileName.value || t('source'))
const format = computed(() => title.value.endsWith('.md') || mediaType.value.includes('markdown') ? 'Markdown' : title.value.includes('.') ? title.value.split('.').at(-1)?.toUpperCase() : mediaType.value.split('/')[0])
let generation = 0
let controller: AbortController | undefined
let returnFocus: HTMLElement | undefined
function panelOpened(element: Element) {
  if (returnFocus === document.activeElement && returnFocus?.matches(':focus-visible')) element.querySelector<HTMLElement>('h2')?.focus({ preventScroll: true })
}
function panelClosed() {
  if (returnFocus?.isConnected && document.activeElement === document.body) returnFocus.focus({ preventScroll: true })
  returnFocus = undefined
}
watch(() => surface.current, async (s, previous) => {
  if (s && !previous) returnFocus = document.activeElement instanceof HTMLElement ? document.activeElement : undefined
  generation++
  const current = generation
  controller?.abort()
  controller = new AbortController()
  error.value = ''
  content.value = ''
  mediaType.value = ''
  fileName.value = ''
  partial.value = false
  busy.value = false
  if (s?.kind !== 'file') return
  busy.value = true
  try {
    const response = await fileContent({ path: { id: s.id }, query: { session_id: s.sessionID }, headers: { Range: 'bytes=0-262143' }, parseAs: 'blob', signal: controller.signal, throwOnError: true })
    if (current !== generation) return
    mediaType.value = response.response.headers.get('Content-Type') || ''
    const disposition = response.response.headers.get('Content-Disposition') || ''
    const encodedName = /filename\*=UTF-8''([^;]+)/i.exec(disposition)?.[1]
    const plainName = /filename=(?:"([^"]+)"|([^;]+))/i.exec(disposition)
    fileName.value = encodedName ? decodeURIComponent(encodedName) : plainName?.[1] || plainName?.[2]?.trim() || s.id
    const range = response.response.headers.get('Content-Range') || ''
    const match = /bytes (\d+)-(\d+)\/(\d+)/.exec(range)
    partial.value = Boolean(match && Number(match[2]) + 1 < Number(match[3]))
    if (response.data instanceof Blob && !/^(image|audio|video)\//.test(mediaType.value) && mediaType.value !== 'application/pdf') {
      try {
        const preview = new TextDecoder('utf-8', { fatal: true }).decode(await response.data.arrayBuffer())
        if (!preview.includes('\u0000')) content.value = preview
      } catch { /* Binary files are offered as downloads. */ }
    }
  } catch (e) { if (current === generation && !controller.signal.aborted) error.value = errorText(e) }
  finally { if (current === generation) busy.value = false }
}, { immediate: true })
function resize(event: PointerEvent) {
  const target = event.target as HTMLElement
  target.setPointerCapture(event.pointerId)
  const start = event.clientX
  const width = panelWidth.value
  function move(e: PointerEvent) { surface.width = Math.max(320, Math.min(maximumWidth.value, width + start - e.clientX)) }
  function end() { target.removeEventListener('pointermove', move); target.removeEventListener('pointerup', end); target.removeEventListener('pointercancel', end) }
  target.addEventListener('pointermove', move)
  target.addEventListener('pointerup', end, { once: true })
  target.addEventListener('pointercancel', end, { once: true })
}
</script>
<template>
  <Transition
    name="context-panel"
    @after-enter="panelOpened"
    @after-leave="panelClosed"
  >
    <aside
      v-if="surface.current"
      class="context-surface"
      :style="{ width: `${panelWidth}px`, '--panel-width': `${panelWidth}px` }"
      :aria-label="title"
    >
      <div
        class="surface-resize"
        role="separator"
        tabindex="0"
        aria-orientation="vertical"
        :aria-label="t('resizeDetails')"
        @pointerdown="resize"
        @keydown.left.prevent="surface.width = Math.min(maximumWidth, panelWidth + 30)"
        @keydown.right.prevent="surface.width = Math.max(320, panelWidth - 30)"
      />
      <div class="surface-heading row between">
        <div class="row">
          <h2 tabindex="-1">
            {{ title }}
          </h2>
        </div><Button
          size="icon"
          variant="ghost"
          :aria-label="t('close')"
          @click="surface.close"
        >
          <X />
        </Button>
      </div>
      <div class="surface-body stack">
        <p
          v-if="error"
          class="error-box"
        >
          {{ error }}
        </p><p
          v-if="busy"
          class="muted"
        >
          {{ t('loading') }}
        </p>
        <template v-if="surface.current.kind === 'file'">
          <div class="row between">
            <span class="muted small">{{ format }}</span><a
              :href="fileURL(surface.current.id, surface.current.sessionID, true)"
              class="button button-secondary button-small"
            ><Download />{{ t('download') }}</a>
          </div>
          <div
            v-if="surface.current.ref"
            class="muted small mono"
          >
            {{ surface.current.ref.path }}<br>{{ surface.current.ref.computer_id }}
          </div>
          <img
            v-if="mediaType.startsWith('image/')"
            :src="fileURL(surface.current.id, surface.current.sessionID)"
            class="file-preview-image"
            :alt="title"
          >
          <video
            v-else-if="mediaType.startsWith('video/')"
            :src="fileURL(surface.current.id, surface.current.sessionID)"
            controls
          />
          <audio
            v-else-if="mediaType.startsWith('audio/')"
            :src="fileURL(surface.current.id, surface.current.sessionID)"
            controls
          />
          <iframe
            v-else-if="mediaType === 'application/pdf'"
            :src="fileURL(surface.current.id, surface.current.sessionID)"
            class="pdf-preview"
            :title="title"
          />
          <template v-else-if="content">
            <p
              v-if="partial"
              class="notice"
            >
              {{ t('previewPartial') }}
            </p>
            <Markdown
              v-if="/markdown/.test(mediaType) || title.endsWith('.md')"
              :content="content"
              :session-i-d="surface.current.sessionID"
            />
            <CodeViewer
              v-else
              :content="content"
              :language="title.endsWith('.py') ? 'python' : /\\.([jt]s|tsx|jsx)$/.test(title) ? 'javascript' : 'plaintext'"
            />
          </template>
          <p
            v-else-if="!busy && !error"
            class="muted"
          >
            {{ t('previewUnavailable') }}
          </p>
        </template>
      </div>
    </aside>
  </Transition>
</template>

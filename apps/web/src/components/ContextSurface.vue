<script setup lang="ts">
import { computed, defineAsyncComponent, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { fileContent, sessionEntry } from '@astral/sdk'
import { Button } from '@astral/ui'
import { ArrowLeft, Download, X, Terminal, FileCode, CircleAlert } from '@astral/icons'
import type { Entry } from '../lib/types'
import type { SessionState } from '../lib/session-state'
import { sortedEntries } from '../lib/session-state'
import { errorText, fileURL, object, request } from '../lib/api'
import { useSurfaceStore } from '../stores/surface'
import Markdown from './Markdown.vue'
const CodeViewer = defineAsyncComponent(() => import('./CodeViewer.vue'))
const TerminalViewer = defineAsyncComponent(() => import('./TerminalViewer.vue'))
const props = defineProps<{ state: SessionState; moreTrace: () => Promise<void> }>()
const surface = useSurfaceStore()
const { t } = useI18n()
const busy = ref(false)
const error = ref('')
const content = ref('')
const mediaType = ref('')
const fileName = ref('')
const partial = ref(false)
const selected = ref<Entry>()
const viewMode = ref<'result' | 'json'>('result')
const title = computed(() => {
  const s = surface.current
  if (!s) return ''
  if (s.kind === 'file') return s.ref?.display_name || fileName.value || t('source')
  if (s.kind === 'json') return s.title
  if (s.kind === 'entry') return s.entry.tool?.invocation.call.name || s.entry.kind
  return t('details')
})
const trace = computed(() => {
  const s = surface.current
  if (s?.kind !== 'trace') return []
  return sortedEntries(props.state.trace).filter(e => e.run_id === s.runID || e.run?.id === s.runID)
})
const steps = computed(() => {
  const tools = new Map<string, Entry>()
  for (const entry of trace.value) if (entry.tool) tools.set(entry.tool.invocation.execution_id, entry)
  return [...tools.values()]
})
const entry = computed(() => selected.value || (surface.current?.kind === 'entry' ? surface.current.entry : undefined))
const result = computed(() => entry.value?.tool?.result)
const details = computed(() => object(result.value?.details))
const args = computed(() => object(entry.value?.tool?.invocation.call.arguments))
const terminalOutput = computed(() => {
  const e = entry.value
  if (!e?.tool) return undefined
  const output = details.value.output
  const progress = props.state.tools[e.tool.invocation.execution_id]?.text ?? ''
  return typeof output === 'string' ? output : progress || undefined
})
const patch = computed(() => {
  const v = details.value.patch || details.value.diff
  return typeof v === 'string' ? v : undefined
})
const sourceResults = computed(() => Array.isArray(details.value.results) ? details.value.results.map(object) : [])
let generation = 0
let controller: AbortController | undefined
watch(() => surface.current, async s => {
  generation++
  const current = generation
  controller?.abort()
  controller = new AbortController()
  selected.value = undefined
  error.value = ''
  content.value = ''
  mediaType.value = ''
  fileName.value = ''
  partial.value = false
  viewMode.value = 'result'
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
async function inspect(e: Entry) {
  selected.value = e
  const s = surface.current
  if (!s || !('sessionID' in s)) return
  const current = generation
  try {
    const complete = await request(sessionEntry({ path: { id: s.sessionID, entry: e.id } }))
    if (current === generation && selected.value?.id === e.id) selected.value = complete
  } catch (e) { error.value = errorText(e) }
}
function resize(event: PointerEvent) {
  const target = event.target as HTMLElement
  target.setPointerCapture(event.pointerId)
  const start = event.clientX
  const width = surface.width
  function move(e: PointerEvent) { surface.width = Math.max(320, Math.min(window.innerWidth - 300, width + start - e.clientX)) }
  function end() { target.removeEventListener('pointermove', move); target.removeEventListener('pointerup', end) }
  target.addEventListener('pointermove', move)
  target.addEventListener('pointerup', end, { once: true })
}
async function more() {
  busy.value = true
  try { await props.moreTrace() } catch (e) { error.value = errorText(e) }
  finally { busy.value = false }
}
</script>
<template>
  <aside
    v-if="surface.current"
    class="context-surface"
    :style="{ width: `${surface.width}px` }"
    :aria-label="title"
  >
    <div
      class="surface-resize"
      role="separator"
      tabindex="0"
      aria-orientation="vertical"
      :aria-label="t('resizeDetails')"
      @pointerdown="resize"
      @keydown.left.prevent="surface.width += 30"
      @keydown.right.prevent="surface.width = Math.max(320, surface.width - 30)"
    />
    <div class="surface-heading row between">
      <div class="row">
        <Button
          v-if="selected"
          size="icon"
          variant="ghost"
          :aria-label="t('back')"
          @click="selected = undefined"
        >
          <ArrowLeft />
        </Button><h2>{{ title }}</h2>
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
          <span class="muted small">{{ mediaType }}</span><a
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
      <template v-else-if="surface.current.kind === 'json'">
        <pre class="raw-detail">{{ JSON.stringify(surface.current.data, null, 2) }}</pre>
      </template>
      <template v-else-if="entry">
        <div class="row wrap">
          <span
            class="badge"
            :class="{ bad: result?.outcome === 'unknown' || result?.is_error, good: result?.outcome === 'succeeded' }"
          >{{ result?.outcome || entry.kind }}</span><span class="muted small">{{ new Date(entry.time).toLocaleString() }}</span><Button
            size="small"
            variant="ghost"
            @click="viewMode = viewMode === 'result' ? 'json' : 'result'"
          >
            {{ viewMode === 'result' ? 'Raw data' : t('result') }}
          </Button>
        </div>
        <template v-if="viewMode === 'result'">
          <pre
            v-if="entry.tool"
            class="command-detail"
          >{{ JSON.stringify(args, null, 2) }}</pre>
          <div
            v-if="terminalOutput !== undefined"
            class="stack"
          >
            <h3 class="row">
              <Terminal />{{ t('terminal') }}<span
                v-if="details.exit_code !== undefined"
                class="badge"
              >exit {{ details.exit_code }}</span>
            </h3><TerminalViewer :content="terminalOutput" />
          </div>
          <div
            v-if="patch"
            class="stack"
          >
            <h3 class="row">
              <FileCode />Diff
            </h3><CodeViewer
              :content="patch"
              language="diff"
            />
          </div>
          <div
            v-if="sourceResults.length"
            class="stack"
          >
            <a
              v-for="(source, i) in sourceResults"
              :key="i"
              :href="String(source.url)"
              target="_blank"
              rel="noopener noreferrer"
            ><strong>{{ source.title }}</strong><p class="muted">{{ source.summary }}</p></a>
          </div>
          <Markdown
            v-if="result?.content?.length && terminalOutput === undefined"
            :content="result.content.filter(p => p.type === 'text').map(p => p.text).join('\n')"
            :session-i-d="surface.current.sessionID"
          />
          <pre
            v-if="!entry.tool"
            class="raw-detail"
          >{{ JSON.stringify(entry, null, 2) }}</pre>
        </template>
        <pre
          v-else
          class="raw-detail"
        >{{ JSON.stringify(entry, null, 2) }}</pre>
      </template>
      <template v-else>
        <p class="muted small">
          {{ trace.length }} recorded events
        </p>
        <button
          v-for="step in steps"
          :key="step.tool?.invocation.execution_id"
          class="trace-step"
          @click="inspect(step)"
        >
          <span class="row"><CircleAlert v-if="step.tool?.needs_resolution" /><FileCode v-else /><strong>{{ step.tool?.invocation.call.name }}</strong></span><span
            class="badge"
            :class="{ bad: step.tool?.needs_resolution || step.tool?.result?.is_error }"
          >{{ step.tool?.result?.outcome || step.kind }}</span>
        </button>
        <details class="trace-raw">
          <summary>{{ t('allEvents') }}</summary><button
            v-for="e in trace"
            :key="e.id"
            class="trace-event"
            @click="inspect(e)"
          >
            <span>{{ e.kind }}</span><time>{{ new Date(e.time).toLocaleTimeString() }}</time>
          </button>
        </details>
        <Button
          v-if="state.traceCursor"
          :busy="busy"
          @click="more"
        >
          {{ t('older') }}
        </Button>
      </template>
    </div>
  </aside>
</template>

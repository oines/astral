<script setup lang="ts">
import { computed, defineAsyncComponent, nextTick, onMounted, ref, watch } from 'vue'
import { useResizeObserver } from '@vueuse/core'
import { useI18n } from 'vue-i18n'
import { Check, ChevronRight, Copy, File } from '@astral/icons'
import { Collapse, useDelayedPending } from '@astral/ui'
import type { Entry, ProcessStatus } from '../lib/types'
import type { LiveTool } from '../lib/session-state'
import { errorText, object } from '../lib/api'
import { fileToolTarget, sourceHost, toolInteractionMode, toolPresentation, toolResultText } from '../lib/tool-presentation'
import { useAppStore } from '../stores/app'
import { useSurfaceStore } from '../stores/surface'
import { fileService } from '../composables/useFileService'
import { useClipboard } from '../composables/useClipboard'
import { useProcessOutput } from '../composables/useProcessOutput'
import Markdown from './Markdown.vue'
import InlineDiff from './InlineDiff.vue'
import ToolResultImages from './ToolResultImages.vue'
import ToolAgentControlDetails from './ToolAgentControlDetails.vue'
import ToolMessageDetails from './ToolMessageDetails.vue'
import ToolSpawnAgentDetails from './ToolSpawnAgentDetails.vue'
import '../tool-details.css'

const TerminalViewer = defineAsyncComponent(() => import('./TerminalViewer.vue'))

const props = defineProps<{ entry: Entry; sessionID: string; live?: LiveTool; process?: ProcessStatus }>()
const emit = defineEmits<{ expand: [] }>()
const { t } = useI18n()
const app = useAppStore()
const surface = useSurfaceStore()
const open = ref(false)
const targetEl = ref<HTMLElement>()
const targetTruncated = ref(false)
const openingFile = ref(false)
const waitingFile = useDelayedPending(openingFile)
const fileError = ref('')
const { copy: copyCmd, copied: copiedCmd } = useClipboard()
const { copy: copyOut, copied: copiedOut } = useClipboard()

const tool = computed(() => props.entry.tool)
const executionID = computed(() => tool.value?.invocation.execution_id ?? '')
const result = computed(() => tool.value?.result)
const details = computed(() => object(result.value?.details))
const presentation = computed(() => toolPresentation(props.entry, app.computers))
const label = computed(() => presentation.value.label ? t(presentation.value.label) : presentation.value.name)
const fileTarget = computed(() => fileToolTarget(props.entry))

const syncActive = computed(() => Boolean(props.live?.started && !props.live?.ended))
const subscribeProcess = computed(() => open.value && Boolean(props.process) && !syncActive.value)
const proc = useProcessOutput({
  sessionID: computed(() => props.sessionID),
  executionID,
  enabled: subscribeProcess,
})

const asyncRunning = computed(() => Boolean(props.process?.running) && proc.running.value !== false)
const active = computed(() => syncActive.value || asyncRunning.value)

const output = computed(() => {
  if (proc.output.value !== undefined) return proc.output.value
  return typeof details.value.output === 'string' ? details.value.output : props.live?.text || ''
})
const exitCode = computed(() => {
  if (proc.exitCode.value !== undefined) return proc.exitCode.value
  if (props.process?.exit_code !== undefined) return props.process.exit_code
  return typeof details.value.exit_code === 'number' ? details.value.exit_code : undefined
})
const processError = computed(() => proc.error.value || props.process?.error || '')
const outputTruncated = computed(() => proc.truncated.value || Boolean(props.process?.truncated))

const patch = computed(() => typeof (details.value.patch || details.value.diff) === 'string' ? String(details.value.patch || details.value.diff) : '')
const sources = computed(() => Array.isArray(details.value.results) ? details.value.results.map(object) : [])
const text = computed(() => toolResultText(props.entry))
const isPlainTextTool = computed(() => ['read', 'read_file', 'write', 'write_file'].includes(presentation.value.name))

const mode = computed(() => toolInteractionMode(props.entry, {
  active: syncActive.value,
  process: props.process ? { ...props.process, running: asyncRunning.value } : undefined,
  liveText: props.live?.text,
  targetTruncated: targetTruncated.value,
}))

function measureTarget() {
  const el = targetEl.value
  targetTruncated.value = Boolean(el && el.scrollWidth > el.clientWidth + 1)
}
useResizeObserver(targetEl, measureTarget)
onMounted(measureTarget)
watch(() => presentation.value.target, () => { void nextTick(measureTarget) })

function toggle() {
  if (mode.value !== 'expand') return
  emit('expand')
  open.value = !open.value
}

async function openFile() {
  if (!fileTarget.value || openingFile.value) return
  fileError.value = ''
  openingFile.value = true
  try {
    const refFile = await fileService.referencePath({ computer_id: fileTarget.value.computerID }, props.sessionID, fileTarget.value.path)
    surface.open({ kind: 'file', id: refFile.id, sessionID: props.sessionID, ref: refFile })
  } catch (err) {
    fileError.value = errorText(err)
  } finally {
    openingFile.value = false
  }
}

function openExternal(event: MouseEvent, url: string) {
  if (window.astralDesktop && /^https?:/i.test(url)) {
    event.preventDefault()
    void window.astralDesktop.openExternal(url)
  }
}

const showFallbackText = computed(() => {
  if (!text.value) return false
  if (output.value || typeof details.value.output === 'string') return false
  if (patch.value || sources.value.length) return false
  return true
})
</script>
<template>
  <div class="tool-call">
    <div
      v-if="mode === 'static'"
      class="tool-call-heading tool-call-static"
    >
      <span class="tool-name">{{ label }}</span><span
        v-if="presentation.target"
        ref="targetEl"
        class="tool-target"
        :title="presentation.target"
      >{{ presentation.target }}</span>
    </div>
    <button
      v-else-if="mode === 'file'"
      type="button"
      class="tool-call-heading tool-call-file"
      :title="t('openFilePreview')"
      @click="openFile"
    >
      <span
        v-if="waitingFile"
        class="spinner"
        aria-hidden="true"
      />
      <span class="tool-name">{{ label }}</span><span
        v-if="presentation.target"
        ref="targetEl"
        class="tool-target"
        :title="presentation.target"
      >{{ presentation.target }}</span>
    </button>
    <template v-else>
      <button
        type="button"
        class="tool-call-heading"
        :aria-expanded="open"
        @click="toggle"
      >
        <span
          v-if="active"
          class="spinner"
          aria-hidden="true"
        />
        <span class="tool-name">{{ label }}</span><span
          v-if="presentation.target"
          ref="targetEl"
          class="tool-target"
          :title="presentation.target"
        >{{ presentation.target }}</span>
        <ChevronRight
          class="disclosure-chevron"
          :class="{ expanded: open }"
        />
      </button>
      <Collapse :open="open">
        <div class="disclosure-body">
          <button
            type="button"
            class="disclosure-rail"
            :aria-label="t('collapseDetails')"
            @click="toggle"
          />
          <div class="tool-call-detail">
            <ToolAgentControlDetails
              v-if="presentation.name === 'agent_control'"
              :entry="entry"
            />
            <ToolMessageDetails
              v-else-if="presentation.name === 'send_message'"
              :entry="entry"
            />
            <ToolSpawnAgentDetails
              v-else-if="presentation.name === 'spawn_agent'"
              :entry="entry"
              :session-i-d="sessionID"
            />
            <template v-else>
              <div
                v-if="presentation.command"
                class="tool-command-row"
              >
                <pre class="tool-command">$ {{ presentation.command }}</pre>
                <button
                  type="button"
                  class="tool-copy-btn"
                  :title="t(copiedCmd ? 'copied' : 'copyCommand')"
                  :aria-label="t(copiedCmd ? 'copied' : 'copyCommand')"
                  @click="copyCmd(presentation.command)"
                >
                  <Check v-if="copiedCmd" /><Copy v-else />
                </button>
              </div>
              <div
                v-if="proc.waiting.value && !output"
                class="tool-detail-loading"
                role="status"
              >
                <span
                  class="spinner"
                  aria-hidden="true"
                />{{ t('loading') }}
              </div>
              <p
                v-if="outputTruncated"
                class="tool-detail-note"
              >
                {{ t('outputTruncated') }}
              </p>
              <div
                v-if="output"
                class="tool-output"
              >
                <div class="tool-output-actions">
                  <button
                    type="button"
                    class="tool-copy-btn"
                    :title="t(copiedOut ? 'copied' : 'copyOutput')"
                    :aria-label="t(copiedOut ? 'copied' : 'copyOutput')"
                    @click="copyOut(output)"
                  >
                    <Check v-if="copiedOut" /><Copy v-else />
                  </button>
                </div>
                <TerminalViewer
                  v-if="output.includes('\u001b')"
                  :content="output"
                  :follow="active"
                  inline
                />
                <pre
                  v-else
                  class="tool-plain-output"
                >{{ output.trimEnd() }}</pre>
              </div>
              <span
                v-if="exitCode !== undefined && exitCode !== 0"
                class="tool-exit"
              >exit {{ exitCode }}</span>
              <p
                v-if="processError"
                class="tool-detail-text text-danger"
              >
                {{ processError }}
              </p>
              <InlineDiff
                v-if="patch"
                :patch="patch"
              />
              <button
                v-if="patch && fileTarget"
                type="button"
                class="tool-file-link"
                @click="openFile"
              >
                <span
                  v-if="waitingFile"
                  class="spinner"
                  aria-hidden="true"
                /><File v-else />
                <span>{{ t('openFilePreview') }}</span>
              </button>
              <div
                v-if="sources.length"
                class="tool-sources"
              >
                <a
                  v-for="(source, i) in sources"
                  :key="i"
                  class="tool-source-item"
                  :href="String(source.url)"
                  target="_blank"
                  rel="noopener noreferrer"
                  @click="openExternal($event, String(source.url))"
                >
                  <div class="tool-source-head">
                    <strong>{{ source.title || source.url }}</strong>
                    <span
                      v-if="sourceHost(String(source.url || ''))"
                      class="tool-source-host"
                    >{{ sourceHost(String(source.url || '')) }}</span>
                  </div>
                  <p
                    v-if="typeof source.summary === 'string' && source.summary.trim()"
                    class="tool-source-summary"
                  >{{ source.summary.trim() }}</p>
                </a>
              </div>
              <pre
                v-if="showFallbackText && isPlainTextTool"
                class="tool-plain-output"
              >{{ text.trimEnd() }}</pre>
              <Markdown
                v-else-if="showFallbackText"
                :content="text"
                :session-i-d="sessionID"
              />
            </template>
            <ToolResultImages
              :entry="entry"
              :session-i-d="sessionID"
            />
          </div>
        </div>
      </Collapse>
    </template>
    <p
      v-if="fileError"
      class="copy-error text-danger"
      role="status"
    >
      {{ fileError }}
    </p>
  </div>
</template>

<script setup lang="ts">
import { computed, defineAsyncComponent, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { sessionEntry } from '@astral/sdk'
import { ChevronRight, CircleAlert } from '@astral/icons'
import { Collapse } from '@astral/ui'
import type { Entry } from '../lib/types'
import type { LiveTool } from '../lib/session-state'
import { errorText, object, request } from '../lib/api'
import { toolPresentation } from '../lib/tool-presentation'
import Markdown from './Markdown.vue'
const TerminalViewer = defineAsyncComponent(() => import('./TerminalViewer.vue'))
const CodeViewer = defineAsyncComponent(() => import('./CodeViewer.vue'))
const props = defineProps<{ entry: Entry; sessionID: string; live?: LiveTool }>()
const emit = defineEmits<{ expand: [] }>()
const { t } = useI18n()
const open = ref(false)
const loaded = ref<Entry>()
const error = ref('')
const busy = ref(false)
const current = computed(() => loaded.value && loaded.value.sequence >= props.entry.sequence ? loaded.value : props.entry)
const tool = computed(() => current.value.tool)
const result = computed(() => tool.value?.result)
const details = computed(() => object(result.value?.details))
const presentation = computed(() => toolPresentation(current.value))
const label = computed(() => presentation.value.label ? t(presentation.value.label) : presentation.value.name)
const active = computed(() => props.live?.started && !props.live?.ended)
const output = computed(() => typeof details.value.output === 'string' ? details.value.output : props.live?.text || '')
const patch = computed(() => typeof (details.value.patch || details.value.diff) === 'string' ? String(details.value.patch || details.value.diff) : '')
const sources = computed(() => Array.isArray(details.value.results) ? details.value.results.map(object) : [])
const text = computed(() => result.value?.content?.filter(part => part.type === 'text').map(part => part.text).join('\n') || '')
async function toggle() {
  emit('expand')
  open.value = !open.value
  if (!open.value || busy.value || loaded.value?.id === props.entry.id) return
  busy.value = true
  const id = props.entry.id
  try {
    const complete = await request(sessionEntry({ path: { id: props.sessionID, entry: id } }))
    if (props.entry.id === id) loaded.value = complete
  } catch (e) { error.value = errorText(e) }
  finally { busy.value = false }
}
watch(() => props.entry.id, () => { loaded.value = undefined; error.value = '' })
</script>
<template>
  <div class="tool-call">
    <button
      class="tool-call-heading"
      :aria-expanded="open"
      @click="toggle"
    >
      <span
        v-if="active"
        class="spinner"
      />
      <span class="tool-name">{{ label }}</span><span
        v-if="presentation.target"
        class="tool-target"
        :title="presentation.target"
      >{{ presentation.target }}</span>
      <ChevronRight
        class="disclosure-chevron"
        :class="{ expanded: open }"
      />
      <CircleAlert
        v-if="tool?.needs_resolution || (result?.is_error && result?.outcome !== 'succeeded')"
        class="tool-warning"
      />
    </button>
    <Collapse :open="open">
      <div class="tool-call-detail">
        <p
          v-if="error"
          class="text-danger small"
        >
          {{ error }}
        </p>
        <p
          v-if="busy && !result"
          class="muted"
        >
          {{ t('loading') }}
        </p>
        <pre
          v-if="presentation.command"
          class="tool-command"
        >$ {{ presentation.command }}</pre>
        <p
          v-if="result?.is_error && result?.outcome === 'succeeded'"
          class="muted small"
        >
          {{ t('resultDeliveryFailed') }}
        </p>
        <div
          v-if="output"
          class="tool-output"
        >
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
          <span
            v-if="details.exit_code !== undefined && details.exit_code !== 0"
            class="tool-exit"
          >exit {{ details.exit_code }}</span>
        </div>
        <CodeViewer
          v-if="patch"
          :content="patch"
          language="diff"
        />
        <div
          v-if="sources.length"
          class="tool-sources"
        >
          <a
            v-for="(source, i) in sources"
            :key="i"
            :href="String(source.url)"
            target="_blank"
            rel="noopener noreferrer"
          ><strong>{{ source.title }}</strong><p>{{ source.summary }}</p></a>
        </div>
        <Markdown
          v-if="text && !output && typeof details.output !== 'string'"
          :content="text"
          :session-i-d="sessionID"
        />
      </div>
    </Collapse>
  </div>
</template>

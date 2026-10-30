<script setup lang="ts">
import { computed, defineAsyncComponent, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { ChevronRight } from '@astral/icons'
import { Collapse } from '@astral/ui'
import type { Entry } from '../lib/types'
import type { LiveTool } from '../lib/session-state'
import { object } from '../lib/api'
import { toolPresentation } from '../lib/tool-presentation'
import Markdown from './Markdown.vue'
const TerminalViewer = defineAsyncComponent(() => import('./TerminalViewer.vue'))
const CodeViewer = defineAsyncComponent(() => import('./CodeViewer.vue'))
const props = defineProps<{ entry: Entry; sessionID: string; live?: LiveTool }>()
const emit = defineEmits<{ expand: [] }>()
const { t } = useI18n()
const open = ref(false)
const tool = computed(() => props.entry.tool)
const result = computed(() => tool.value?.result)
const details = computed(() => object(result.value?.details))
const presentation = computed(() => toolPresentation(props.entry))
const label = computed(() => presentation.value.label ? t(presentation.value.label) : presentation.value.name)
const active = computed(() => props.live?.started && !props.live?.ended)
const output = computed(() => typeof details.value.output === 'string' ? details.value.output : props.live?.text || '')
const patch = computed(() => typeof (details.value.patch || details.value.diff) === 'string' ? String(details.value.patch || details.value.diff) : '')
const sources = computed(() => Array.isArray(details.value.results) ? details.value.results.map(object) : [])
const text = computed(() => result.value?.content?.filter(part => part.type === 'text').map(part => part.text).join('\n') || '')
function toggle() { emit('expand'); open.value = !open.value }
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
    </button>
    <Collapse :open="open">
      <div class="tool-call-detail">
        <pre
          v-if="presentation.command"
          class="tool-command"
        >$ {{ presentation.command }}</pre>
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
        </div>
        <span
          v-if="details.exit_code !== undefined && details.exit_code !== 0"
          class="tool-exit"
        >exit {{ details.exit_code }}</span>
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

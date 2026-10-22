<script setup lang="ts">
import { computed, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { ChevronRight } from '@astral/icons'
import { Collapse } from '@astral/ui'
import type { ProcessItem, SessionState } from '../lib/session-state'
import { toolPresentation } from '../lib/tool-presentation'
import Markdown from './Markdown.vue'
import ProcessStep from './ProcessStep.vue'
const props = defineProps<{ items: ProcessItem[]; sessionID: string; state: SessionState; active?: boolean; status?: string }>()
const emit = defineEmits<{ expand: [] }>()
const { t } = useI18n()
const open = ref(false)
const toolItems = computed(() => props.items.filter(item => item.kind === 'tool'))
const runningTools = computed(() => toolItems.value.filter(item => {
  const live = props.state.tools[item.entry.tool!.invocation.execution_id]
  return live?.started && !live.ended
}))
const busy = computed(() => props.active || Boolean(runningTools.value.length))
const singleReasoning = computed(() => props.items.length === 1 && props.items[0]?.kind === 'reasoning' ? props.items[0] : undefined)
const display = computed(() => {
  if (props.status) return { key: `status:${props.status}`, label: props.status, target: '' }
  const latest = props.active ? props.items.at(-1) : runningTools.value.at(-1)
  if (busy.value && latest?.kind === 'tool') {
    const info = toolPresentation(latest.entry)
    return { key: latest.key, label: info.label ? t(info.label) : info.name, target: info.target }
  }
  if (busy.value && latest?.kind === 'reasoning') {
    const line = latest.text.trim().split('\n').filter(line => line.trim()).at(-1) ?? ''
    return { key: latest.key, label: line.replace(/^\s*(?:#{1,6}\s+|[-*]\s+)/, '').replace(/[*`]/g, ''), target: '' }
  }
  if (!props.items.length) return { key: 'waiting', label: '', target: '' }
  const names = toolItems.value.map(item => item.entry.tool!.invocation.call.name)
  const label = names.length ? t(names.every(name => name === 'exec_command') ? 'commandsSummary' : names.every(name => name === 'read_file') ? 'filesReadSummary' : 'executionSummary', { count: names.length }) : t('thinkingProcess')
  return { key: 'summary', label, target: '' }
})
function toggle() { emit('expand'); open.value = !open.value }
</script>
<template>
  <div class="activity">
    <button
      class="activity-line"
      :aria-expanded="items.length ? open : undefined"
      :aria-label="display.label || t('working')"
      :disabled="!items.length"
      @click="toggle"
    >
      <span
        v-if="busy"
        class="spinner"
        role="status"
        :aria-label="t('working')"
      />
      <span class="activity-label-viewport">
        <Transition name="activity-roll">
          <span
            :key="display.key"
            class="activity-label"
          ><span class="activity-action">{{ display.label }}</span><span
            v-if="display.target"
            class="tool-target"
            :title="display.target"
          >{{ display.target }}</span></span>
        </Transition>
      </span>
      <ChevronRight
        v-if="items.length"
        class="disclosure-chevron"
        :class="{ expanded: open }"
      />
    </button>
    <Collapse :open="open && Boolean(items.length)">
      <div class="activity-details">
        <Markdown
          v-if="singleReasoning"
          :content="singleReasoning.text"
          :streaming="singleReasoning.streaming"
          :session-i-d="sessionID"
        />
        <template v-else>
          <ProcessStep
            v-for="item in items"
            :key="item.key"
            :item="item"
            :session-i-d="sessionID"
            :state="state"
            @expand="emit('expand')"
          />
        </template>
      </div>
    </Collapse>
  </div>
</template>

<script setup lang="ts">
import { computed, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { ChevronRight } from '@astral/icons'
import { Collapse } from '@astral/ui'
import type { ProcessItem, SessionState } from '../lib/session-state'
import { toolPresentation } from '../lib/tool-presentation'
import { useAppStore } from '../stores/app'
import Markdown from './Markdown.vue'
import ProcessStep from './ProcessStep.vue'
import ToolCall from './ToolCall.vue'

const props = defineProps<{ items: ProcessItem[]; sessionID: string; state: SessionState; active?: boolean; status?: string }>()
const emit = defineEmits<{ expand: [] }>()
const { t } = useI18n()
const app = useAppStore()
const open = ref(false)

const toolItems = computed(() => props.items.filter((item): item is Extract<ProcessItem, { kind: 'tool' }> => item.kind === 'tool'))
function isSyncRunning(item: Extract<ProcessItem, { kind: 'tool' }>) {
  const live = props.state.tools[item.entry.tool!.invocation.execution_id]
  return Boolean(live?.started && !live.ended)
}
function isBackgroundRunning(item: Extract<ProcessItem, { kind: 'tool' }>) {
  return Boolean(props.state.processes?.[item.entry.tool!.invocation.execution_id]?.running)
}
const runningTools = computed(() => toolItems.value.filter(isSyncRunning))
const backgroundTools = computed(() => toolItems.value.filter(item => isBackgroundRunning(item) && !isSyncRunning(item)))
const busy = computed(() => Boolean(props.active || runningTools.value.length || backgroundTools.value.length))
const singleReasoning = computed(() => props.items.length === 1 && props.items[0]?.kind === 'reasoning' ? props.items[0] : undefined)
const directSingleTool = computed(() => {
  if (props.status || props.items.length !== 1 || props.items[0]?.kind !== 'tool') return undefined
  const only = props.items[0]
  if (props.active && !isSyncRunning(only) && !isBackgroundRunning(only)) return undefined
  return only
})

const display = computed(() => {
  if (props.status) return { key: `status:${props.status}`, label: props.status, target: '' }
  if (props.active) {
    const summary = props.state.activityStatus
    if (summary?.text) {
      const label = summary.source === 'rule' ? t(`activityRule.${summary.text}`) : summary.text
      return { key: `activity:${label}`, label, target: '' }
    }
  }
  const latest = props.active ? props.items.at(-1) : runningTools.value.at(-1)
  if ((props.active || runningTools.value.length) && latest?.kind === 'tool') {
    const info = toolPresentation(latest.entry, app.computers)
    return { key: latest.key, label: info.label ? t(info.label) : info.name, target: info.target }
  }
  if (props.active && latest?.kind === 'reasoning') {
    const line = latest.text.trim().split('\n').filter(line => line.trim()).at(-1) ?? ''
    return { key: latest.key, label: line.replace(/^\s*(?:#{1,6}\s+|[-*]\s+)/, '').replace(/[*`]/g, ''), target: '' }
  }
  if (!props.items.length) return { key: 'waiting', label: '', target: '' }
  const names = toolItems.value.map(item => item.entry.tool!.invocation.call.name)
  const summary = names.length
    ? t(names.every(name => name === 'exec_command') ? 'commandsSummary' : names.every(name => name === 'read_file' || name === 'read') ? 'filesReadSummary' : 'executionSummary', { count: names.length })
    : t('thinkingProcess')
  if (backgroundTools.value.length) {
    return {
      key: `bg:${backgroundTools.value.length}:${names.length}`,
      label: t('executionWithBackground', { summary, count: backgroundTools.value.length }),
      target: '',
    }
  }
  return { key: 'summary', label: summary, target: '' }
})

function toggle() { emit('expand'); open.value = !open.value }
</script>
<template>
  <div class="activity">
    <ToolCall
      v-if="directSingleTool"
      :entry="directSingleTool.entry"
      :session-i-d="sessionID"
      :live="state.tools[directSingleTool.entry.tool!.invocation.execution_id]"
      :process="state.processes?.[directSingleTool.entry.tool!.invocation.execution_id]"
      @expand="emit('expand')"
    />
    <template v-else>
      <button
        type="button"
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
        <div class="disclosure-body">
          <button
            type="button"
            class="disclosure-rail"
            :aria-label="t('collapseDetails')"
            @click="toggle"
          />
          <div class="activity-details">
            <Markdown
              v-if="singleReasoning"
              :data-scroll-anchor="singleReasoning.key"
              :content="singleReasoning.text"
              :streaming="singleReasoning.streaming"
              :session-i-d="sessionID"
            />
            <template v-else>
              <ProcessStep
                v-for="item in items"
                :key="item.key"
                :data-scroll-anchor="item.key"
                :item="item"
                :session-i-d="sessionID"
                :state="state"
                @expand="emit('expand')"
              />
            </template>
          </div>
        </div>
      </Collapse>
    </template>
  </div>
</template>

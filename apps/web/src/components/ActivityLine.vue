<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import { ChevronRight, Check, CircleAlert } from '@astral/icons'
import type { SessionState } from '../lib/session-state'
import { sortedEntries } from '../lib/session-state'
import { useSurfaceStore } from '../stores/surface'
const props = defineProps<{ runID: string; sessionID: string; state: SessionState }>()
const { t } = useI18n()
const surface = useSurfaceStore()
const entries = computed(() => sortedEntries(props.state.trace).filter(e => e.run_id === props.runID || e.run?.id === props.runID))
const active = computed(() => props.state.view?.run_id === props.runID && !props.state.completedRuns.has(props.runID))
const last = computed(() => entries.value.filter(e => e.run).at(-1)?.run)
const needsAttention = computed(() => last.value?.status === 'failed' || entries.value.some(e => e.kind === 'tool_unresolved'))
const tools = computed(() => {
  const ids = new Map<string, string>()
  for (const entry of entries.value) if (entry.tool) ids.set(entry.tool.invocation.execution_id, entry.tool.invocation.call.name)
  return [...ids.entries()]
})
const label = computed(() => {
  if (active.value) {
    if (props.state.view?.status === 'stopping') return t('stopping')
    const live = tools.value.filter(([id]) => props.state.tools[id]?.started && !props.state.tools[id]?.ended)
    if (live.length) return `${t('running')} · ${live.map(([, name]) => name).join(', ')}`
    const preparing = Object.values(props.state.previews).some(p => p.runID === props.runID && Object.values(p.parts).some(part => part.type === 'tool_call_delta'))
    return t(preparing ? 'preparing' : 'working')
  }
  if (needsAttention.value) return t('needs_attention')
  const start = entries.value.find(e => e.run?.status === 'started')
  const end = entries.value.filter(e => e.run && e.run.status !== 'started').at(-1)
  const seconds = start && end ? Math.max(0, (Date.parse(end.time) - Date.parse(start.time)) / 1000) : undefined
  const time = seconds !== undefined ? `${seconds < 60 ? seconds.toFixed(1) + 's' : Math.round(seconds / 60) + 'm'}` : ''
  const silent = last.value?.end_reason === 'no_reply'
  const count = tools.value.filter(([, name]) => name !== 'no_reply').length
  return [silent ? t('endedSilently') : last.value?.status === 'cancelled' ? t('stop') : t('details'), time, count ? `${count} ${t('result')}` : ''].filter(Boolean).join(' · ')
})
</script>
<template>
  <button
    class="activity-line"
    :class="{ attention: needsAttention }"
    @click="surface.open({ kind: 'trace', runID, sessionID })"
  >
    <span
      v-if="active"
      class="spinner"
    /><CircleAlert v-else-if="needsAttention" /><Check v-else />
    <span>{{ label }}</span><ChevronRight />
  </button>
</template>

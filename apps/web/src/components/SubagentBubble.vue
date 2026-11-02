<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import { Bot, ChevronRight } from '@astral/icons'
import { useAppStore } from '../stores/app'
import { useSurfaceStore } from '../stores/surface'
import { spawnedSubagent, subagentStatus } from '../lib/subagents'
import type { Entry } from '../lib/types'

const props = defineProps<{ entry: Entry; sessionID: string }>()
const emit = defineEmits<{ open: [] }>()
const app = useAppStore()
const surface = useSurfaceStore()
const { t } = useI18n()
const child = computed(() => spawnedSubagent(props.entry))
const view = computed(() => child.value ? app.sessionViews[child.value.sessionID] || app.statuses[child.value.sessionID] : undefined)
const removed = computed(() => !!child.value && app.deletedSessions?.has(child.value.sessionID))
const title = computed(() => view.value?.title || child.value?.title || t('subagent'))
const status = computed(() => removed.value ? 'subagentDeleted' : subagentStatus(view.value?.error ? 'failed' : view.value?.status || child.value?.status))
const expanded = computed(() => surface.current?.kind === 'subagent' && surface.current.path.some(session => session.id === child.value?.sessionID))
function open() {
  if (!child.value || removed.value) return
  emit('open')
  const parentID = child.value.parentSessionID || props.sessionID
  const parent = app.sessionViews[parentID] || app.sessions.find(session => session.id === parentID)
  surface.openSubagent({ ...child.value, title: title.value }, { id: parentID, title: parent?.title || t('conversation') })
}
</script>
<template>
  <button
    v-if="child"
    type="button"
    class="subagent-bubble"
    :aria-label="t('openSubagent', { name: title })"
    aria-controls="context-surface"
    :aria-expanded="expanded"
    :disabled="removed"
    @click="open"
  >
    <Bot aria-hidden="true" />
    <span class="subagent-bubble-copy"><strong>{{ title }}</strong><span>{{ t(status) }}</span></span>
    <ChevronRight
      v-if="!removed"
      aria-hidden="true"
    />
  </button>
</template>

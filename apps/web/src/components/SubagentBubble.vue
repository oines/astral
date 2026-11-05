<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import { Bot } from '@astral/icons'
import { useAppStore } from '../stores/app'
import { useSurfaceStore } from '../stores/surface'
import { subagentActivityStatus, type SubagentActivity } from '../lib/subagents'

const props = defineProps<{ activity: SubagentActivity; sessionID: string }>()
const emit = defineEmits<{ open: [] }>()
const app = useAppStore()
const surface = useSurfaceStore()
const { t } = useI18n()
const child = computed(() => {
  if (!subagentActivityStatus(props.activity.status)) return
  return { sessionID: props.activity.child_session_id, parentSessionID: props.activity.session_id, title: props.activity.name, status: props.activity.status }
})
const removed = computed(() => !!child.value && app.deletedSessions?.has(child.value.sessionID))
const title = computed(() => props.activity.name || t('subagent'))
const activityText = computed(() => t(subagentActivityStatus(props.activity.status) ?? 'subagent', { name: title.value }))
const label = computed(() => removed.value ? activityText.value : `${activityText.value} · ${t('openSubagent', { name: title.value })}`)
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
    :aria-label="label"
    aria-controls="context-surface"
    :aria-expanded="expanded"
    :disabled="removed"
    @click="open"
  >
    <Bot aria-hidden="true" />
    <span
      class="subagent-status-copy"
    >{{ activityText }}</span>
  </button>
</template>

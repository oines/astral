<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import { Bot, ChevronRight } from '@astral/icons'
import { useAppStore } from '../stores/app'
import { useSurfaceStore, type SurfaceSession } from '../stores/surface'
import { subagentStatus, type SubagentActivity } from '../lib/subagents'
import { compareEventTime } from '../lib/event-time'

const props = defineProps<{ rootID: string; activities: Record<string, SubagentActivity> }>()
const app = useAppStore()
const surface = useSurfaceStore()
const { t } = useI18n()
const children = computed(() => {
  const latest = new Map<string, SubagentActivity>()
  for (const activity of Object.values(props.activities)) {
    const previous = latest.get(activity.child_session_id)
    const order = previous ? compareEventTime(activity.time, previous.time) : 1
    if (!previous || order > 0 || order === 0 && activity.sequence > previous.sequence) latest.set(activity.child_session_id, activity)
  }
  const sessions = new Map([...app.sessions, ...Object.values(app.sessionViews)].map(session => [session.id, session]))
  return [...sessions.values()].flatMap(session => {
    if (!session.subagent_parent_id || app.deletedSessions.has(session.id)) return []
    const path: SurfaceSession[] = [{ id: session.id, title: session.title }]
    const visited = new Set([session.id])
    let parent = session.subagent_parent_id
    while (parent && !visited.has(parent)) {
      visited.add(parent)
      const source = sessions.get(parent)
      path.unshift({ id: parent, title: source?.title || t('conversation') })
      if (parent === props.rootID) {
        const activity = latest.get(session.id)
        const currentStatus = app.sessionViews[session.id]?.status || ('status' in session && typeof session.status === 'string' ? session.status : undefined)
        const status = currentStatus || (activity ? ({ started: 'running', completed: 'idle', failed: 'failed', cancelled: 'idle' }[activity.status]) : undefined)
        const running = ['running', 'compacting', 'stopping', 'queued', 'waiting'].includes(status ?? '') || status === 'needs_attention' && 'run_id' in session && !!session.run_id
        const time = activity?.time || session.updated_at || session.created_at
        return [{ id: session.id, title: session.title || activity?.name || t('subagent'), path, status, running, time, sequence: activity?.sequence ?? 0 }]
      }
      parent = source?.subagent_parent_id || ''
    }
    return []
  }).sort((a, b) => compareEventTime(b.time, a.time) || b.sequence - a.sequence || a.title.localeCompare(b.title) || a.id.localeCompare(b.id))
})
const groups = computed(() => [
  { key: 'running', title: t('workspaceSubagentsRunning'), children: children.value.filter(child => child.running) },
  { key: 'ended', title: t('workspaceSubagentsEnded'), children: children.value.filter(child => !child.running) },
].filter(group => group.children.length))
</script>
<template>
  <section
    class="workspace-agent-list"
    :aria-label="t('workspaceSubagents')"
  >
    <p
      v-if="!children.length"
      class="muted"
    >
      {{ t('workspaceNoSubagents') }}
    </p>
    <section
      v-for="group in groups"
      :key="group.key"
      class="workspace-agent-group"
      :aria-label="group.title"
    >
      <h3>{{ group.title }}</h3>
      <button
        v-for="child in group.children"
        :key="child.id"
        type="button"
        class="workspace-agent-row"
        @click="surface.open({ kind: 'subagent', id: child.id, title: child.title, path: child.path })"
      >
        <Bot aria-hidden="true" />
        <span><strong>{{ child.title }}</strong><small class="muted">{{ child.status ? t(subagentStatus(child.status)) : t('workspaceUnknownStatus') }}</small></span>
        <ChevronRight aria-hidden="true" />
      </button>
    </section>
  </section>
</template>
<style scoped>
.workspace-agent-list { padding: 8px 8px 16px; overflow: auto; scrollbar-width: thin; }
.workspace-agent-group + .workspace-agent-group { margin-top: 20px; }
.workspace-agent-group > h3 { margin: 8px 12px 4px; color: var(--muted); font-size: 12px; font-weight: 500; line-height: 20px; }
.workspace-agent-row { position: relative; width: 100%; display: flex; align-items: center; gap: 12px; padding: 12px; min-height: 60px; border: 0; border-radius: var(--radius-control); background: transparent; text-align: left; }
.workspace-agent-row + .workspace-agent-row::before { content: ''; position: absolute; top: 0; left: 12px; right: 12px; height: 1px; background: var(--panel-line); pointer-events: none; }
.workspace-agent-row:hover { background: var(--hover); }
.workspace-agent-row > span { display: flex; flex: 1; min-width: 0; flex-direction: column; gap: 4px; }
.workspace-agent-row strong { font-weight: 500; overflow-wrap: anywhere; }
.workspace-agent-row > svg { width: 16px; height: 16px; color: var(--muted); }
</style>

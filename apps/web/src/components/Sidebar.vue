<script setup lang="ts">
import { computed, nextTick, ref } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { useI18n } from 'vue-i18n'
import { Plus, Search, Settings2, FolderOpen, Pin, Clock3 } from '@astral/icons'
import { Button, Modal, Select } from '@astral/ui'
import { useAppStore } from '../stores/app'
import { errorText } from '../lib/api'
import type { Session } from '../lib/types'
import ChannelIcon from './ChannelIcon.vue'
import SessionMenu from './SessionMenu.vue'
import { useSessionTitles } from '../composables/useSessionTitles'
import { sidebarGroups, visibleSidebarSessions } from '../lib/sidebar-groups'
import { hasScheduledSession } from '../lib/schedules'
const app = useAppStore()
const { titles, typing } = useSessionTitles(() => app.data ? app.sessions : undefined)
const route = useRoute()
const router = useRouter()
const { t } = useI18n()
const search = ref('')
const filtered = computed(() => visibleSidebarSessions(app.sessions, app.schedules, isActive, String(route.params.id || '')).filter(s => s.agent_id === app.activeAgentID && s.title.toLocaleLowerCase().includes(search.value.toLocaleLowerCase())).sort((a, b) => b.updated_at.localeCompare(a.updated_at)))
const rename = ref(false)
const renameTarget = ref<Session>()
const title = ref('')
const renameError = ref('')
const pendingID = ref('')
const actionError = ref('')
function isActive(session: Session) { return ['running', 'stopping', 'compacting'].includes(app.statuses[session.id]?.status ?? '') }
const groups = computed(() => sidebarGroups(filtered.value, app.schedules, isActive))
const planned = computed(() => new Set(app.schedules.filter(hasScheduledSession).map(schedule => schedule.session_id)))
function agentName(id: string) { return app.agents.find(a => a.id === id)?.name || t('missing') }
function navigate() { if (window.innerWidth < 760) app.sidebar = false }
function selectAgent(id: string) {
  if (id === app.activeAgentID) return
  app.activeAgentID = id
  search.value = ''
  void router.push(route.path === '/schedules' ? '/schedules' : '/')
}
function beginRename(session: Session) {
  renameTarget.value = session
  title.value = session.title
  renameError.value = ''
  rename.value = true
}
async function focusMenu(id: string) {
  await nextTick()
  document.querySelector<HTMLButtonElement>(`[data-session-menu="${CSS.escape(id)}"]`)?.focus({ preventScroll: true })
}
function renameClosed(event: Event) {
  event.preventDefault()
  if (renameTarget.value) void focusMenu(renameTarget.value.id)
}
async function presentation(session: Session, patch: { title?: string; pinned?: boolean; archived?: boolean }) {
  if (pendingID.value || (patch.title !== undefined && !patch.title.trim())) return
  const renaming = patch.title !== undefined
  pendingID.value = session.id
  actionError.value = ''
  renameError.value = ''
  try {
    await app.presentation(session.id, patch)
    if (renaming && renameTarget.value?.id === session.id) rename.value = false
    else if (document.activeElement === document.body) await focusMenu(session.id)
  } catch (error) {
    if (renaming) renameError.value = errorText(error)
    else actionError.value = `${session.title}：${errorText(error)}`
  } finally { pendingID.value = '' }
}
</script>
<template>
  <aside
    id="chat-sidebar"
    class="sidebar"
    :aria-label="t('recent')"
  >
    <div class="sidebar-heading">
      <Select
        v-if="app.agents.length"
        :model-value="app.activeAgentID"
        class="sidebar-agent"
        :label="t('agent')"
        :options="app.agents.map(a => ({ value: a.id, label: a.name }))"
        @update:model-value="selectAgent"
      />
    </div>
    <RouterLink
      to="/"
      class="button button-ghost new-session"
      @click="navigate"
    >
      <Plus />{{ t('newSession') }}<kbd>⌘ N</kbd>
    </RouterLink>
    <RouterLink
      to="/schedules"
      class="button button-ghost new-session"
      :class="{ selected: route.path === '/schedules' }"
      :aria-current="route.path === '/schedules' ? 'page' : undefined"
      @click="navigate"
    >
      <Clock3 />{{ t('schedules') }}
    </RouterLink>
    <div class="search-input">
      <Search /><input
        v-model="search"
        :placeholder="t('search')"
        :aria-label="t('search')"
      >
    </div>
    <nav class="session-list">
      <section
        v-for="group in groups"
        :key="group.title"
      >
        <h2 class="sidebar-section">
          {{ t(group.title) }}
        </h2>
        <div
          v-for="s in group.items"
          :key="s.id"
          class="session-row"
          :class="{ selected: route.params.id === s.id }"
        >
          <RouterLink
            :to="`/sessions/${s.id}`"
            class="session-link"
            :class="{ selected: route.params.id === s.id }"
            :title="`${s.title} · ${agentName(s.agent_id)}`"
            :aria-current="route.params.id === s.id ? 'page' : undefined"
            @click="navigate"
          >
            <ChannelIcon
              v-if="s.channel_platform"
              :platform="s.channel_platform"
            />
            <span class="session-title-accessible">{{ s.title }}</span>
            <span
              class="session-label"
              :class="{ 'is-typing': typing[s.id] }"
              aria-hidden="true"
            >{{ titles[s.id] ?? s.title }}</span>
            <span
              v-if="app.statuses[s.id]?.status === 'running'"
              class="running-dot"
              :aria-label="t('running')"
            /><FolderOpen v-else-if="s.archived" /><Clock3
              v-else-if="planned.has(s.id)"
              :aria-label="t('planned')"
            /><Pin v-else-if="s.pinned" />
          </RouterLink>
          <SessionMenu
            :session="s"
            :busy="!!pendingID"
            @rename="beginRename(s)"
            @presentation="presentation(s, $event)"
          />
        </div>
      </section>
      <p
        v-if="!filtered.length"
        class="muted sidebar-empty"
      >
        {{ t(search ? 'noResults' : 'noSessions') }}
      </p>
    </nav>
    <p
      v-if="actionError"
      class="sidebar-action-error text-danger"
      role="alert"
    >
      {{ actionError }}
    </p>
    <RouterLink
      to="/settings/general"
      class="sidebar-settings"
    >
      <Settings2 />{{ t('settings') }}<span class="version">v{{ app.data?.version }}</span>
    </RouterLink>
    <Modal
      v-model:open="rename"
      :title="t('rename')"
      @close-auto-focus="renameClosed"
    >
      <label class="label">{{ t('name') }}<input
        v-model="title"
        :disabled="!!pendingID"
        @keydown.enter="renameTarget && presentation(renameTarget, { title: title.trim() })"
      ></label>
      <p
        v-if="renameError"
        class="text-danger small"
        role="alert"
      >
        {{ renameError }}
      </p>
      <Button
        variant="primary"
        :busy="!!pendingID"
        :disabled="!title.trim() || !!pendingID"
        @click="renameTarget && presentation(renameTarget, { title: title.trim() })"
      >
        {{ t('save') }}
      </Button>
    </Modal>
  </aside>
</template>

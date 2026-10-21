<script setup lang="ts">
import { computed, ref } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { useI18n } from 'vue-i18n'
import { Plus, Search, Settings2, CircleAlert, Pin } from '@astral/icons'
import { Select } from '@astral/ui'
import { useAppStore } from '../stores/app'
const app = useAppStore()
const route = useRoute()
const router = useRouter()
const { t } = useI18n()
const search = ref('')
const filtered = computed(() => app.sessions.filter(s => !s.archived && s.agent_id === app.activeAgentID && s.title.toLocaleLowerCase().includes(search.value.toLocaleLowerCase())).sort((a, b) => b.updated_at.localeCompare(a.updated_at)))
const groups = computed(() => [
  { title: 'now', items: filtered.value.filter(s => app.statuses[s.id] && app.statuses[s.id]?.status !== 'idle') },
  { title: 'pinned', items: filtered.value.filter(s => s.pinned && (!app.statuses[s.id] || app.statuses[s.id]?.status === 'idle')) },
  { title: 'recent', items: filtered.value.filter(s => !s.pinned && (!app.statuses[s.id] || app.statuses[s.id]?.status === 'idle')) },
])
function agentName(id: string) { return app.agents.find(a => a.id === id)?.name || t('missing') }
function navigate() { if (window.innerWidth < 760) app.sidebar = false }
function selectAgent(id: string) {
  if (id === app.activeAgentID) return
  app.activeAgentID = id
  search.value = ''
  void router.push('/')
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
    <div class="search-input">
      <Search /><input
        v-model="search"
        :placeholder="t('search')"
        :aria-label="t('search')"
      >
    </div>
    <nav class="session-list">
      <section
        v-for="group in groups.filter(g => g.items.length)"
        :key="group.title"
      >
        <h2 class="sidebar-section">
          {{ t(group.title) }}
        </h2>
        <RouterLink
          v-for="s in group.items"
          :key="s.id"
          :to="`/sessions/${s.id}`"
          class="session-link"
          :class="{ selected: route.params.id === s.id }"
          :title="`${s.title} · ${agentName(s.agent_id)}`"
          :aria-current="route.params.id === s.id ? 'page' : undefined"
          @click="navigate"
        >
          <span class="session-label">{{ s.title }}</span>
          <span
            v-if="app.statuses[s.id]?.status === 'running'"
            class="running-dot"
            :aria-label="t('running')"
          /><CircleAlert
            v-else-if="app.statuses[s.id]?.status === 'needs_attention'"
            class="text-danger"
            :aria-label="t('needs_attention')"
          /><Pin v-else-if="s.pinned" />
        </RouterLink>
      </section>
      <p
        v-if="!filtered.length"
        class="muted sidebar-empty"
      >
        {{ t(search ? 'noResults' : 'noSessions') }}
      </p>
    </nav>
    <RouterLink
      to="/settings/general"
      class="sidebar-settings"
    >
      <Settings2 />{{ t('settings') }}<span class="version">v{{ app.data?.version }}</span>
    </RouterLink>
  </aside>
</template>

<script setup lang="ts">
import { computed, ref } from 'vue'
import { useRoute } from 'vue-router'
import { useI18n } from 'vue-i18n'
import { Plus, Search, Settings2, PanelLeftClose, MessageCircle, Pin } from '@astral/icons'
import { Button } from '@astral/ui'
import { useAppStore } from '../stores/app'
const app = useAppStore()
const route = useRoute()
const { t } = useI18n()
const search = ref('')
const agent = ref('')
const filtered = computed(() => app.sessions.filter(s => !s.archived && (!agent.value || s.agent_id === agent.value) && s.title.toLocaleLowerCase().includes(search.value.toLocaleLowerCase())).sort((a, b) => b.updated_at.localeCompare(a.updated_at)))
const groups = computed(() => [
  { title: 'now', items: filtered.value.filter(s => app.statuses[s.id] && app.statuses[s.id]?.status !== 'idle') },
  { title: 'pinned', items: filtered.value.filter(s => s.pinned && (!app.statuses[s.id] || app.statuses[s.id]?.status === 'idle')) },
  { title: 'recent', items: filtered.value.filter(s => !s.pinned && (!app.statuses[s.id] || app.statuses[s.id]?.status === 'idle')) },
])
function agentName(id: string) { return app.agents.find(a => a.id === id)?.name || t('missing') }
function navigate() { if (window.innerWidth < 760) app.sidebar = false }
</script>
<template>
  <aside
    class="sidebar"
    :aria-label="t('recent')"
  >
    <div class="sidebar-brand">
      <RouterLink
        to="/"
        class="wordmark"
      >
        ✳ astral
      </RouterLink><Button
        variant="ghost"
        size="icon"
        :aria-label="t('close')"
        @click="app.sidebar = false"
      >
        <PanelLeftClose />
      </Button>
    </div>
    <RouterLink
      to="/"
      class="button button-secondary new-session"
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
    <select
      v-if="app.agents.length > 1"
      v-model="agent"
      class="agent-filter"
      :aria-label="t('agent')"
    >
      <option value="">
        {{ t('agents') }}
      </option><option
        v-for="a in app.agents"
        :key="a.id"
        :value="a.id"
      >
        {{ a.name }}
      </option>
    </select>
    <nav class="session-list">
      <section
        v-for="group in groups.filter(g => g.items.length)"
        :key="group.title"
      >
        <h2 class="sidebar-section">
          {{ t(group.title) }}<span>{{ group.items.length }}</span>
        </h2>
        <RouterLink
          v-for="s in group.items"
          :key="s.id"
          :to="`/sessions/${s.id}`"
          class="session-link"
          :class="{ selected: route.params.id === s.id }"
          @click="navigate"
        >
          <span
            v-if="app.statuses[s.id]?.status === 'running'"
            class="running-dot"
          /><Pin v-else-if="s.pinned" /><MessageCircle v-else />
          <span class="session-label"><span>{{ s.title }}</span><small>{{ agentName(s.agent_id) }}<span
            v-if="app.statuses[s.id]?.status === 'needs_attention'"
            class="text-danger"
          > · {{ t('needs_attention') }}</span></small></span>
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

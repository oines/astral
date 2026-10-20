<script setup lang="ts">
import { computed, defineAsyncComponent, ref } from 'vue'
import { useRoute } from 'vue-router'
import { useI18n } from 'vue-i18n'
import { useQuery } from '@pinia/colada'
import { storageInfoQuery } from '@astral/sdk/colada'
import { logout, updateSession } from '@astral/sdk'
import { ArrowLeft, Settings2, Sun, SlidersHorizontal, Globe, Monitor, MessageCircle, User, HardDrive, Shield, Terminal, Download } from '@astral/icons'
import { Button } from '@astral/ui'
import { useAppStore } from '../stores/app'
import { useAction } from '../composables/useAction'
import { bytes, request } from '../lib/api'
const ModelsSettings = defineAsyncComponent(() => import('../components/settings/ModelsSettings.vue'))
const ComputersSettings = defineAsyncComponent(() => import('../components/settings/ComputersSettings.vue'))
const AgentsSettings = defineAsyncComponent(() => import('../components/settings/AgentsSettings.vue'))
const ChannelsSettings = defineAsyncComponent(() => import('../components/settings/ChannelsSettings.vue'))
const SearchSettings = defineAsyncComponent(() => import('../components/settings/SearchSettings.vue'))
const app = useAppStore()
const route = useRoute()
const { t } = useI18n()
const action = useAction()
const section = computed(() => typeof route.params.section === 'string' ? route.params.section : 'general')
const storage = useQuery({ ...storageInfoQuery(), enabled: () => section.value === 'storage' })
const links = [
  ['general', 'general', Settings2], ['appearance', 'appearance', Sun], ['models', 'models', SlidersHorizontal], ['search', 'webSearch', Globe], ['computers', 'computers', Monitor], ['channels', 'channels', MessageCircle], ['agents', 'agents', User], ['storage', 'storage', HardDrive], ['security', 'security', Shield], ['advanced', 'advanced', Terminal],
] as const
const title = computed(() => links.find(l => l[0] === section.value)?.[1] || 'general')
const desktop = Boolean(window.astralDesktop)
const archives = ref(false)
const sessions = computed(() => app.sessions.filter(s => s.archived === archives.value))
async function setPreference(patch: Parameters<typeof app.preferences>[0]) {
  await action.run(() => app.preferences(patch))
  if (!action.error.value) action.success.value = t('saved')
}
</script>
<template>
  <div class="settings-layout">
    <aside class="settings-nav">
      <RouterLink
        to="/"
        class="settings-back"
      >
        <ArrowLeft />{{ t('back') }}
      </RouterLink>
      <h1>{{ t('settings') }}</h1>
      <nav>
        <RouterLink
          v-for="[path, label, icon] in links"
          :key="path"
          :to="`/settings/${path}`"
          :class="{ selected: section === path }"
        >
          <component :is="icon" />{{ t(label) }}
        </RouterLink>
      </nav>
      <span class="settings-version">Astral · {{ app.data?.version }}</span>
    </aside>
    <main class="settings-main">
      <div class="settings-content">
        <div class="settings-title">
          <span class="eyebrow">ASTRAL / {{ t('settings') }}</span><h1>{{ t(title) }}</h1>
        </div>
        <p
          v-if="action.error.value"
          class="error-box"
        >
          {{ action.error.value }}
        </p><p
          v-if="action.success.value"
          class="success-note"
          role="status"
        >
          {{ action.success.value }}
        </p>
        <ModelsSettings v-if="section === 'models'" />
        <ComputersSettings v-else-if="section === 'computers'" />
        <AgentsSettings v-else-if="section === 'agents'" />
        <ChannelsSettings v-else-if="section === 'channels'" />
        <SearchSettings v-else-if="section === 'search'" />
        <div
          v-else-if="section === 'general'"
          class="stack"
        >
          <label class="preference-row"><span><strong>{{ t('loginStart') }}</strong><small>{{ t('desktopOnly') }}</small></span><input
            type="checkbox"
            :checked="app.data?.preferences.login_start"
            :disabled="!desktop || action.busy.value"
            @change="setPreference({ login_start: ($event.target as HTMLInputElement).checked })"
          ></label>
          <label class="preference-row"><span><strong>{{ t('keepRunning') }}</strong><small>{{ t('desktopOnly') }}</small></span><input
            type="checkbox"
            :checked="app.data?.preferences.keep_running"
            :disabled="!desktop || action.busy.value"
            @change="setPreference({ keep_running: ($event.target as HTMLInputElement).checked })"
          ></label>
          <label class="preference-row"><span><strong>{{ t('inputDefault') }}</strong></span><select
            :value="app.data?.preferences.running_input"
            :disabled="action.busy.value"
            @change="setPreference({ running_input: ($event.target as HTMLSelectElement).value })"
          ><option value="follow_up">{{ t('followUp') }}</option><option value="steer">{{ t('steer') }}</option></select></label>
        </div>
        <div
          v-else-if="section === 'appearance'"
          class="stack"
        >
          <label class="preference-row"><span><strong>{{ t('theme') }}</strong></span><select
            :value="app.data?.preferences.theme"
            :disabled="action.busy.value"
            @change="setPreference({ theme: ($event.target as HTMLSelectElement).value })"
          ><option value="system">{{ t('system') }}</option><option value="light">{{ t('light') }}</option><option value="dark">{{ t('dark') }}</option></select></label>
          <label class="preference-row"><span><strong>{{ t('language') }}</strong></span><select
            :value="app.data?.preferences.language"
            :disabled="action.busy.value"
            @change="setPreference({ language: ($event.target as HTMLSelectElement).value })"
          ><option value="zh">简体中文</option><option value="en">English</option></select></label>
          <div class="appearance-preview">
            <span class="agent-avatar">A</span><div>
              <h3>Astral</h3><p class="muted">
                {{ t('quietWorkspace') }}
              </p><span class="badge good">{{ t('online') }}</span>
            </div>
          </div>
        </div>
        <div
          v-else-if="section === 'storage'"
          class="stack"
        >
          <template v-if="storage.data.value">
            <div class="detail-block">
              <h3>Server</h3><p class="mono muted">
                {{ storage.data.value.directory }}
              </p><div class="row wrap">
                <span class="badge">{{ t('configuration') }} · {{ bytes(storage.data.value.database_bytes) }}</span><span class="badge">Rollouts · {{ bytes(storage.data.value.rollout_bytes) }}</span>
              </div>
            </div>
            <p class="notice">
              {{ t('filesStorage') }}<br>{{ t('mediaStorage') }}
            </p>
          </template>
          <div class="row between">
            <h2>{{ t('recent') }}</h2><label class="row muted"><input
              v-model="archives"
              type="checkbox"
            >{{ t('archive') }}</label>
          </div>
          <div
            v-for="s in sessions"
            :key="s.id"
            class="resource-row"
          >
            <RouterLink :to="`/sessions/${s.id}`">
              <strong>{{ s.title }}</strong><small>{{ app.agents.find(a => a.id === s.agent_id)?.name }}</small>
            </RouterLink>
            <div class="row">
              <a
                class="button button-ghost button-icon"
                :href="`/api/v1/sessions/${s.id}/rollout`"
                :title="t('export')"
              ><Download /></a><Button
                variant="ghost"
                size="small"
                @click="action.run(async () => { await request(updateSession({ path: { id: s.id }, body: { archived: !s.archived } })); await app.refresh() })"
              >
                {{ t(s.archived ? 'unarchive' : 'archive') }}
              </Button>
            </div>
          </div>
        </div>
        <div
          v-else-if="section === 'security'"
          class="stack"
        >
          <div class="detail-block">
            <h3>Owner</h3><p class="muted">
              {{ t('ownerCookie') }}
            </p>
          </div>
          <label class="label">{{ t('serverURL') }}<input
            :value="app.data?.public_url"
            readonly
          ></label>
          <p class="notice">
            {{ t('ownerCommand') }}
          </p>
          <Button
            class="align-start"
            @click="action.run(async () => { await request(logout()); app.authorized = false })"
          >
            {{ t('disconnect') }}
          </Button>
        </div>
        <div
          v-else
          class="stack"
        >
          <div class="detail-block">
            <h3>Astral {{ app.data?.version }}</h3><p class="muted">
              {{ app.data?.host.hostname }} · {{ app.data?.host.os }} / {{ app.data?.host.arch }}
            </p><p class="mono muted">
              {{ app.data?.public_url }}
            </p>
          </div>
          <p
            v-if="app.data?.host.local_error"
            class="error-box"
          >
            {{ app.data.host.local_error }}
          </p>
          <div class="detail-block">
            <h3>{{ t('computers') }}</h3><p
              v-for="c in app.computers"
              :key="c.id"
              class="muted"
            >
              {{ c.name }} · {{ t(c.online ? 'online' : 'offline') }}<span v-if="c.last_error"> · {{ c.last_error }}</span>
            </p>
          </div>
          <div class="detail-block">
            <h3>{{ t('containerRuntime') }}</h3><p class="muted">
              {{ app.data?.host.container_runtime || app.data?.host.container_error || t('none') }}
            </p>
          </div>
          <div class="row">
            <Button
              :busy="action.busy.value"
              @click="action.run(app.refresh)"
            >
              {{ t('refresh') }}
            </Button><a
              href="/openapi.json"
              target="_blank"
              class="button button-secondary"
            >OpenAPI</a>
          </div>
        </div>
      </div>
    </main>
  </div>
</template>

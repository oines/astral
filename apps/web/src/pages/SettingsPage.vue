<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, onDeactivated, ref, watch } from 'vue'
import { usePreferredReducedMotion } from '@vueuse/core'
import { useRoute } from 'vue-router'
import { useI18n } from 'vue-i18n'
import { useQuery } from '@pinia/colada'
import { storageInfoQuery } from '@astral/sdk/colada'
import { logout, updateSession } from '@astral/sdk'
import { ArrowLeft, Settings2, Sun, SlidersHorizontal, Globe, Monitor, MessageCircle, User, HardDrive, Shield, Terminal, Download } from '@astral/icons'
import { Button, Select, Switch, SettingsSection, SettingsRow } from '@astral/ui'
import { useAppStore } from '../stores/app'
import { useAction } from '../composables/useAction'
import { bytes, operatingSystem, request } from '../lib/api'
import ModelsSettings from '../components/settings/ModelsSettings.vue'
import ComputersSettings from '../components/settings/ComputersSettings.vue'
import AgentsSettings from '../components/settings/AgentsSettings.vue'
import ChannelsSettings from '../components/settings/ChannelsSettings.vue'
import SearchSettings from '../components/settings/SearchSettings.vue'
const app = useAppStore()
const route = useRoute()
const { t } = useI18n()
const action = useAction()
const section = ref(typeof route.params.section === 'string' ? route.params.section : 'general')
watch(() => route.path, path => { if (path.startsWith('/settings')) section.value = typeof route.params.section === 'string' ? route.params.section : 'general' })
const panes = { models: ModelsSettings, computers: ComputersSettings, agents: AgentsSettings, channels: ChannelsSettings, search: SearchSettings }
const activeComponent = computed(() => panes[section.value as keyof typeof panes])
const scroll = ref<HTMLElement>()
const content = ref<HTMLElement>()
const reducedMotion = usePreferredReducedMotion()
let reveal: Animation | undefined
function cancelReveal() { reveal?.cancel(); reveal = undefined }
onBeforeUnmount(cancelReveal)
onDeactivated(cancelReveal)
watch(reducedMotion, cancelReveal)
const scrollPositions: Record<string, number> = {}
watch(section, async (next, previous) => {
  scrollPositions[previous] = scroll.value?.scrollTop || 0
  await nextTick()
  if (scroll.value) scroll.value.scrollTop = scrollPositions[next] || 0
  cancelReveal()
  if (reducedMotion.value !== 'reduce') {
    reveal = content.value?.animate([{ opacity: 0, transform: 'translateY(4px)' }, { opacity: 1, transform: 'none' }], { duration: 160, easing: 'cubic-bezier(.2,.8,.2,1)' })
  }
})
async function copyDiagnostics() { await action.run(() => navigator.clipboard.writeText(JSON.stringify({ version: app.data?.version, host: app.data?.host, computers: app.computers.map(c => ({ name: c.name, online: c.online, error: c.last_error })) }, null, 2)), t('copied')) }
const storage = useQuery({ ...storageInfoQuery(), enabled: () => section.value === 'storage' })
const links = [
  ['general', 'general', Settings2], ['appearance', 'appearance', Sun], ['models', 'models', SlidersHorizontal], ['search', 'webSearch', Globe], ['computers', 'computers', Monitor], ['channels', 'channels', MessageCircle], ['agents', 'agents', User], ['storage', 'storage', HardDrive], ['security', 'security', Shield], ['advanced', 'advanced', Terminal],
] as const
watch(section, () => { action.success.value = ''; action.error.value = '' })
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
        :to="app.lastChatPath"
        class="settings-back"
      >
        <ArrowLeft />{{ t('back') }}
      </RouterLink>
      <h1>{{ t('settings') }}</h1>
      <nav>
        <template
          v-for="([path, label, icon], index) in links"
          :key="path"
        >
          <p
            v-if="index === 2 || index === 7"
            class="settings-nav-group"
          >
            {{ t(index === 2 ? 'workspace' : 'application') }}
          </p>
          <RouterLink
            :to="`/settings/${path}`"
            :class="{ selected: section === path }"
          >
            <component :is="icon" />{{ t(label) }}
          </RouterLink>
        </template>
      </nav>
      <span class="settings-version">v{{ app.data?.version }}</span>
    </aside>
    <main
      ref="scroll"
      class="settings-main"
    >
      <div
        ref="content"
        class="settings-content"
      >
        <div
          v-if="section !== 'models'"
          class="settings-title"
        >
          <h1>{{ t(title) }}</h1><span
            v-if="action.success.value"
            class="settings-saved"
            role="status"
          >{{ action.success.value }}</span>
        </div>
        <p
          v-if="action.error.value"
          class="error-box"
        >
          {{ action.error.value }}
        </p>
        <KeepAlive>
          <component
            :is="activeComponent"
            v-if="activeComponent"
            :key="section"
          />
        </KeepAlive>
        <div
          v-if="section === 'general'"
          class="settings-sections"
        >
          <SettingsSection :title="t('application')">
            <SettingsRow
              :label="t('loginStart')"
              :description="t('loginStartHint')"
            >
              <Switch
                :model-value="app.data?.preferences.login_start"
                :disabled="!desktop || action.busy.value"
                :label="t('loginStart')"
                @update:model-value="setPreference({ login_start: $event })"
              />
            </SettingsRow>
            <SettingsRow
              :label="t('keepRunning')"
              :description="t('keepRunningHint')"
            >
              <Switch
                :model-value="app.data?.preferences.keep_running"
                :disabled="!desktop || action.busy.value"
                :label="t('keepRunning')"
                @update:model-value="setPreference({ keep_running: $event })"
              />
            </SettingsRow>
          </SettingsSection>
          <SettingsSection :title="t('conversation')">
            <SettingsRow
              :label="t('inputDefault')"
              :description="t('inputDefaultHint')"
            >
              <Select
                :model-value="app.data?.preferences.running_input"
                :disabled="action.busy.value"
                :label="t('inputDefault')"
                :options="[{ value: 'follow_up', label: t('followUp') }, { value: 'steer', label: t('steer') }]"
                @update:model-value="setPreference({ running_input: $event })"
              />
            </SettingsRow>
          </SettingsSection>
          <p
            v-if="!desktop"
            class="muted"
          >
            {{ t('desktopOnly') }}
          </p>
        </div>
        <div
          v-else-if="section === 'appearance'"
          class="settings-sections"
        >
          <SettingsSection :title="t('interface')">
            <SettingsRow
              :label="t('theme')"
            >
              <Select
                :model-value="app.data?.preferences.theme"
                :disabled="action.busy.value"
                :label="t('theme')"
                :options="[{ value: 'system', label: t('system') }, { value: 'light', label: t('light') }, { value: 'dark', label: t('dark') }]"
                @update:model-value="setPreference({ theme: $event })"
              />
            </SettingsRow>
            <SettingsRow
              :label="t('language')"
            >
              <Select
                :model-value="app.data?.preferences.language"
                :disabled="action.busy.value"
                :label="t('language')"
                :options="[{ value: 'zh', label: '简体中文' }, { value: 'en', label: 'English' }]"
                @update:model-value="setPreference({ language: $event })"
              />
            </SettingsRow>
          </SettingsSection>
        </div>
        <div
          v-else-if="section === 'storage'"
          class="settings-sections"
        >
          <SettingsSection
            v-if="storage.data.value"
            :title="t('dataOverview')"
          >
            <SettingsRow :label="t('configurationData')">
              <span class="muted">{{ bytes(storage.data.value.database_bytes) }}</span>
            </SettingsRow>
            <SettingsRow :label="t('conversationData')">
              <span class="muted">{{ bytes(storage.data.value.rollout_bytes) }}</span>
            </SettingsRow>
          </SettingsSection>
          <p class="section-caption">
            {{ t('storageFilesHint') }}
          </p>
          <details
            v-if="storage.data.value"
            class="form-disclosure"
          >
            <summary>{{ t('dataDirectory') }}</summary><p class="mono muted">
              {{ storage.data.value.directory }}
            </p>
          </details>
          <SettingsSection :title="t('manageConversations')">
            <template #actions>
              <Select
                :label="t('manageConversations')"
                :model-value="archives ? 'archived' : 'active'"
                :options="[{ value: 'active', label: t('activeConversations') }, { value: 'archived', label: t('archivedConversations') }]"
                @update:model-value="archives = $event === 'archived'"
              />
            </template>
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
                  :aria-label="t('exportHistory')"
                  :title="t('exportHistory')"
                ><Download /></a>
                <Button
                  variant="ghost"
                  size="small"
                  @click="action.run(async () => { await request(updateSession({ path: { id: s.id }, body: { archived: !s.archived } })); await app.refresh() })"
                >
                  {{ t(s.archived ? 'unarchive' : 'archive') }}
                </Button>
              </div>
            </div>
            <p
              v-if="!sessions.length"
              class="empty"
            >
              {{ t(archives ? 'noArchived' : 'noSessions') }}
            </p>
          </SettingsSection>
        </div>
        <div
          v-else-if="section === 'security'"
          class="settings-sections"
        >
          <SettingsSection :title="t('accessSession')">
            <SettingsRow
              :label="t('signedIn')"
              :description="t('accessDescription')"
            >
              <span class="badge good">{{ t('connectionReady') }}</span>
            </SettingsRow>
            <SettingsRow :label="t('connectionAddress')">
              <span class="muted mono">{{ app.data?.public_url }}</span>
            </SettingsRow>
          </SettingsSection>
          <details class="form-disclosure">
            <summary>{{ t('technicalDetails') }}</summary><p class="muted">
              {{ t('ownerCookie') }}
            </p><p class="muted">
              {{ t('ownerCommand') }}
            </p>
          </details>
          <div class="danger-row">
            <p class="muted">
              {{ t('signOutHint') }}
            </p><Button @click="action.run(async () => { await request(logout()); app.authorized = false })">
              {{ t('disconnect') }}
            </Button>
          </div>
        </div>
        <div
          v-else-if="section === 'advanced'"
          class="settings-sections"
        >
          <SettingsSection :title="t('aboutAstral')">
            <SettingsRow :label="t('versionLabel')">
              <span class="muted">Astral {{ app.data?.version }}</span>
            </SettingsRow>
            <SettingsRow :label="t('host')">
              <span class="muted">{{ app.data?.host.hostname }}</span>
            </SettingsRow>
            <SettingsRow :label="t('operatingSystem')">
              <span class="muted">{{ operatingSystem(app.data?.host.os) }} / {{ app.data?.host.arch }}</span>
            </SettingsRow>
            <SettingsRow :label="t('containerRuntime')">
              <span class="muted">{{ app.data?.host.container_runtime || t('unavailable') }}</span>
            </SettingsRow>
          </SettingsSection>
          <p
            v-if="app.data?.host.local_error || app.data?.host.container_error"
            class="error-box"
          >
            {{ app.data.host.local_error || app.data.host.container_error }}
          </p>
          <SettingsSection :title="t('computerStatus')">
            <SettingsRow
              v-for="c in app.computers"
              :key="c.id"
              :label="c.name"
              :description="c.last_error"
            >
              <span
                class="badge"
                :class="{ good: c.online }"
              >{{ t(c.online ? 'online' : 'offline') }}</span>
            </SettingsRow>
            <p
              v-if="!app.computers.length"
              class="empty"
            >
              {{ t('noComputers') }}
            </p>
          </SettingsSection>
          <div class="row">
            <Button
              :busy="action.busy.value"
              @click="action.run(app.refresh)"
            >
              {{ t('refresh') }}
            </Button><Button @click="copyDiagnostics">
              {{ t('copyDiagnostics') }}
            </Button>
          </div>
          <SettingsSection :title="t('developerResources')">
            <SettingsRow :label="t('apiReference')">
              <a
                href="/openapi.json"
                target="_blank"
                class="button button-secondary button-small"
              >OpenAPI</a>
            </SettingsRow>
          </SettingsSection>
        </div>
      </div>
    </main>
  </div>
</template>

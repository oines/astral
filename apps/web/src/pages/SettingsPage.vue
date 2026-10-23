<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, onDeactivated, ref, watch } from 'vue'
import { usePreferredReducedMotion } from '@vueuse/core'
import { useRoute } from 'vue-router'
import { useI18n } from 'vue-i18n'
import { useQuery } from '@pinia/colada'
import { storageInfoQuery } from '@astral/sdk/colada'
import { getPreferences, logout, updateSession } from '@astral/sdk'
import { ArrowLeft, Settings2, Sun, SlidersHorizontal, Globe, Monitor, Bot, HardDrive, Shield, Terminal, Download, AstralLogo, Copy, Search } from '@astral/icons'
import { Button, Select, Switch, SettingsSection, SettingsRow } from '@astral/ui'
import { useAppStore } from '../stores/app'
import { useAction } from '../composables/useAction'
import { useSettingsSave } from '../composables/useSettingsSave'
import { bytes, request } from '../lib/api'
import ModelsSettings from '../components/settings/ModelsSettings.vue'
import ComputersSettings from '../components/settings/ComputersSettings.vue'
import AgentsSettings from '../components/settings/AgentsSettings.vue'
import SearchSettings from '../components/settings/SearchSettings.vue'
import BotSettingsNav from '../components/settings/BotSettingsNav.vue'
import SettingsFeedback from '../components/settings/SettingsFeedback.vue'
const app = useAppStore()
const route = useRoute()
const { t } = useI18n()
const action = useAction()
const saves = useSettingsSave()
const section = ref(typeof route.params.section === 'string' ? route.params.section : 'general')
watch(() => route.path, path => { if (path.startsWith('/settings')) section.value = typeof route.params.section === 'string' ? route.params.section : 'general' })
const panes = { models: ModelsSettings, computers: ComputersSettings, agents: AgentsSettings, search: SearchSettings }
const activeComponent = computed(() => panes[section.value as keyof typeof panes])
const currentBot = computed(() => section.value === 'agents' ? app.agents.find(a => a.id === route.query.agent) : undefined)
const scroll = ref<HTMLElement>()
const content = ref<HTMLElement>()
const reducedMotion = usePreferredReducedMotion()
let reveal: Animation | undefined
function cancelReveal() { reveal?.cancel(); reveal = undefined }
onBeforeUnmount(cancelReveal)
onDeactivated(cancelReveal)
watch(reducedMotion, cancelReveal)
const scrollPositions: Record<string, number> = {}
watch(() => route.fullPath, async (next, previous) => {
  if (!next.startsWith('/settings')) return
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
  ['general', 'general', Settings2], ['appearance', 'appearance', Sun], ['models', 'models', SlidersHorizontal], ['search', 'webSearch', Globe], ['computers', 'computers', Monitor], ['agents', 'agents', Bot], ['storage', 'storage', HardDrive], ['security', 'security', Shield], ['advanced', 'advanced', Terminal],
] as const
watch(section, () => { action.success.value = ''; action.error.value = '' })
const title = computed(() => links.find(l => l[0] === section.value)?.[1] || 'general')
const desktop = Boolean(window.astralDesktop)
const archives = ref(false)
const sessionSearch = ref('')
const sessions = computed(() => app.sessions.filter(s => s.archived === archives.value && s.title.toLocaleLowerCase().includes(sessionSearch.value.toLocaleLowerCase())))
async function copyText(value: string) { await action.run(() => navigator.clipboard.writeText(value), t('copied')) }
async function setPreference(patch: Parameters<typeof app.preferences>[0]) {
  const field = Object.keys(patch)[0] || 'preferences'
  await saves.run('preferences', field, {
    write: () => app.preferences(patch),
    reconcile: async () => {
      const current = await request(getPreferences())
      await app.refresh().catch(() => undefined)
      return Object.entries(patch).every(([key, value]) => current[key as keyof typeof current] === value) ? 'applied' : 'not-applied'
    },
  }, Object.values(patch)[0])
}
</script>
<template>
  <div class="settings-layout">
    <BotSettingsNav
      v-if="currentBot"
      :bot="currentBot"
    />
    <aside
      v-else
      class="settings-nav"
    >
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
            v-if="index === 2 || index === 6"
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
          v-if="section !== 'models' && !currentBot"
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
          <SettingsSection
            v-if="desktop"
            :title="t('application')"
          >
            <SettingsRow
              :label="t('loginStart')"
              :description="t('loginStartHint')"
            >
              <Switch
                :model-value="saves.value('preferences/login_start', app.data?.preferences.login_start)"
                :label="t('loginStart')"
                @update:model-value="setPreference({ login_start: $event })"
              />
            </SettingsRow>
            <SettingsRow
              :label="t('keepRunning')"
              :description="t('keepRunningHint')"
            >
              <Switch
                :model-value="saves.value('preferences/keep_running', app.data?.preferences.keep_running)"
                :label="t('keepRunning')"
                @update:model-value="setPreference({ keep_running: $event })"
              />
            </SettingsRow>
            <SettingsFeedback :scope="['preferences/login_start', 'preferences/keep_running']" />
          </SettingsSection>
          <SettingsSection
            :title="t('inputDefault')"
            :description="t('runningInputScope')"
          >
            <label
              v-for="choice in [{ value: 'follow_up', label: 'followUp', hint: 'followUpHint' }, { value: 'steer', label: 'steer', hint: 'steerHint' }]"
              :key="choice.value"
              class="settings-choice"
            >
              <input
                type="radio"
                name="running-input"
                :checked="saves.value('preferences/running_input', app.data?.preferences.running_input) === choice.value"
                @change="setPreference({ running_input: choice.value })"
              >
              <span><strong>{{ t(choice.label) }}</strong><small>{{ t(choice.hint) }}</small></span>
            </label>
            <SettingsFeedback scope="preferences/running_input" />
          </SettingsSection>
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
                :model-value="saves.value('preferences/theme', app.data?.preferences.theme)"
                :label="t('theme')"
                :options="[{ value: 'system', label: t('system') }, { value: 'light', label: t('light') }, { value: 'dark', label: t('dark') }]"
                @update:model-value="setPreference({ theme: $event })"
              />
            </SettingsRow>
            <SettingsRow
              :label="t('language')"
            >
              <Select
                :model-value="saves.value('preferences/language', app.data?.preferences.language)"
                :label="t('language')"
                :options="[{ value: 'zh', label: '简体中文' }, { value: 'en', label: 'English' }]"
                @update:model-value="setPreference({ language: $event })"
              />
            </SettingsRow>
            <SettingsFeedback :scope="['preferences/theme', 'preferences/language']" />
          </SettingsSection>
        </div>
        <div
          v-else-if="section === 'storage'"
          class="settings-sections"
        >
          <SettingsSection
            :title="t('manageConversations')"
            :description="t('archiveHint')"
          >
            <template #actions>
              <Select
                :label="t('manageConversations')"
                :model-value="archives ? 'archived' : 'active'"
                :options="[{ value: 'active', label: t('activeConversations') }, { value: 'archived', label: t('archivedConversations') }]"
                @update:model-value="archives = $event === 'archived'"
              />
            </template>
            <div class="settings-list-search search-input">
              <Search /><input
                v-model="sessionSearch"
                :placeholder="t('search')"
                :aria-label="t('search')"
              >
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
                  :aria-label="t('exportHistory')"
                  :title="t('exportHistory')"
                ><Download /></a>
                <Button
                  variant="ghost"
                  size="small"
                  :disabled="action.busy.value"
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
              {{ t(sessionSearch ? 'noResults' : archives ? 'noArchived' : 'noSessions') }}
            </p>
          </SettingsSection>
          <SettingsSection
            v-if="storage.data.value"
            :title="t('dataOverview')"
          >
            <div class="storage-summary">
              <span>{{ t('configurationData') }} <strong>{{ bytes(storage.data.value.database_bytes) }}</strong></span>
              <span>{{ t('conversationData') }} <strong>{{ bytes(storage.data.value.rollout_bytes) }}</strong></span>
            </div>
            <div class="storage-location">
              <div>
                <strong>{{ t('dataDirectory') }}</strong><code>{{ storage.data.value.directory }}</code><p class="muted small">
                  {{ t('storageLocationHint') }}
                </p>
              </div>
              <Button
                variant="ghost"
                size="icon"
                :aria-label="t('copyDirectory')"
                :title="t('copyDirectory')"
                @click="copyText(storage.data.value.directory)"
              >
                <Copy />
              </Button>
            </div>
          </SettingsSection>
          <p
            v-else-if="storage.error.value"
            class="error-box"
            role="alert"
          >
            {{ t('storageLoadFailed') }}
          </p>
        </div>
        <div
          v-else-if="section === 'security'"
          class="settings-sections"
        >
          <SettingsSection
            :title="t('accessSession')"
            :description="t('accessDescription')"
          >
            <SettingsRow :label="t('connectionAddress')">
              <span class="muted mono break-word">{{ app.data?.public_url }}</span>
              <Button
                variant="ghost"
                size="icon"
                :aria-label="t('copy')"
                :title="t('copy')"
                @click="copyText(app.data?.public_url || '')"
              >
                <Copy />
              </Button>
            </SettingsRow>
          </SettingsSection>
          <SettingsSection>
            <SettingsRow
              :label="t('disconnect')"
              :description="t('signOutHint')"
            >
              <Button
                :busy="action.busy.value"
                @click="action.run(async () => { await request(logout()); app.authorized = false })"
              >
                {{ t('disconnect') }}
              </Button>
            </SettingsRow>
          </SettingsSection>
        </div>
        <div
          v-else-if="section === 'advanced'"
          class="settings-sections"
        >
          <SettingsSection>
            <div class="about-identity">
              <AstralLogo decorative /><div>
                <h2>Astral</h2><p class="muted">
                  {{ t('versionLabel') }} {{ app.data?.version }}
                </p>
              </div>
            </div>
          </SettingsSection>
          <SettingsSection :title="t('diagnostics')">
            <SettingsRow
              :label="t('copyDiagnostics')"
              :description="t('diagnosticsHint')"
            >
              <Button
                :busy="action.busy.value"
                @click="copyDiagnostics"
              >
                <Copy />{{ t('copy') }}
              </Button>
            </SettingsRow>
          </SettingsSection>
          <p
            v-if="app.data?.host.local_error || app.data?.host.container_error"
            class="error-box"
          >
            {{ app.data.host.local_error || app.data.host.container_error }}
          </p>
          <RouterLink
            v-if="app.data?.host.local_error || app.data?.host.container_error"
            to="/settings/computers"
            class="small"
          >
            {{ t('manageComputers') }}
          </RouterLink>
          <SettingsSection :title="t('developerResources')">
            <SettingsRow
              :label="t('apiReference')"
              :description="t('apiReferenceHint')"
            >
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

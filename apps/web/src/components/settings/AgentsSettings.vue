<script setup lang="ts">
import { computed, onActivated, ref } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { useQuery } from '@pinia/colada'
import { listSearchProvidersQuery } from '@astral/sdk/colada'
import { deleteAgent, listAgents, saveAgent } from '@astral/sdk'
import { Button, Modal, Select, Switch, SettingsRow, SettingsSection } from '@astral/ui'
import { ChevronRight, Plus, Search } from '@astral/icons'
import { useI18n } from 'vue-i18n'
import type { Agent } from '../../lib/types'
import { newMeta, operatingSystem, request } from '../../lib/api'
import { useAction } from '../../composables/useAction'
import { useSettingsSave } from '../../composables/useSettingsSave'
import { useAppStore } from '../../stores/app'
import ResourceDelete from './ResourceDelete.vue'
import ChannelsSettings from './ChannelsSettings.vue'
import SettingsFeedback from './SettingsFeedback.vue'
import SettingsText from './SettingsText.vue'

const { t } = useI18n()
const app = useAppStore()
onActivated(() => { void app.refresh().catch(() => undefined) })
const route = useRoute()
const router = useRouter()
const action = useAction()
const saves = useSettingsSave()
const searches = useQuery(listSearchProvidersQuery())
const selected = computed(() => app.agents.find(a => a.id === route.query.agent))
const resource = computed(() => `agent:${selected.value?.id || ''}`)
const platform = computed(() => typeof route.query.platform === 'string' ? route.query.platform : route.query.tab === 'channels' ? 'telegram' : '')
const computersPage = computed(() => route.query.tab === 'computers' && !platform.value)
const creating = ref<Agent>()
const deleting = ref(false)
const search = ref('')
const visibleAgents = computed(() => app.agents.filter(a => a.name.toLocaleLowerCase().includes(search.value.toLocaleLowerCase())))
const computers = computed(() => app.computers.filter(c => !c.revoked || selected.value?.allowed_computers?.includes(c.id)))
const allowedComputers = computed(() => app.computers.filter(c => !c.revoked && selected.value?.allowed_computers?.includes(c.id)))

function newBot() { action.error.value = ''; creating.value = { ...newMeta(), name: '', avatar: '', default_model_id: '', allowed_computers: [] } }
function openAgent(id: string) { void router.push({ path: '/settings/agents', query: { agent: id } }) }
async function latest(id: string) {
  const value = (await request(listAgents())).find(a => a.id === id)
  if (!value) throw { code: 'not_found', message: t('botMissing') }
  return value
}
async function update(id: string, field: string, patch: Partial<Agent> | ((agent: Agent) => Partial<Agent>), intent?: unknown, matches?: (agent: Agent) => boolean) {
  let applied: Partial<Agent> | undefined
  return saves.run(`agent:${id}`, field, {
    write: async () => {
      const current = await latest(id)
      applied = typeof patch === 'function' ? patch(current) : patch
      if (Object.entries(applied).some(([key, value]) => JSON.stringify(current[key as keyof Agent]) !== JSON.stringify(value))) saves.confirmAgent(await request(saveAgent({ body: { ...current, ...applied } })))
      await app.refresh().catch(() => undefined)
    },
    reconcile: async () => {
      const current = await latest(id)
      saves.confirmAgent(current)
      await app.refresh().catch(() => undefined)
      return (matches ? matches(current) : applied && Object.entries(applied).every(([key, value]) => JSON.stringify(current[key as keyof Agent] ?? '') === JSON.stringify(value))) ? 'applied' : 'not-applied'
    },
  }, intent)
}
function permission(id: string, computerID: string, enabled: boolean) {
  return update(id, `computer:${computerID}`, current => ({
    allowed_computers: enabled ? [...new Set([...(current.allowed_computers || []), computerID])] : (current.allowed_computers || []).filter(value => value !== computerID),
    ...(!enabled && current.default_computer_id === computerID ? { default_computer_id: '' } : {}),
  }), enabled, current => !!current.allowed_computers?.includes(computerID) === enabled && (enabled || current.default_computer_id !== computerID))
}
function modelOptions(value: string | undefined, fallback: string) {
  const options = [{ value: '', label: fallback }, ...app.availableModels.map(m => ({ value: m.id, label: m.name }))]
  if (value && !options.some(o => o.value === value)) options.push({ value, label: `${app.models.find(m => m.id === value)?.name || t('missing')} · ${t('disabled')}` })
  return options
}
async function create() {
  await action.run(async () => {
    if (!creating.value) return
    const bot = await request(saveAgent({ body: { ...creating.value, name: creating.value.name.trim() } }))
    creating.value = undefined
    await app.refresh()
    openAgent(bot.id)
  })
}
async function remove() {
  const bot = selected.value
  if (!bot) return
  await action.run(async () => {
    await request(deleteAgent({ path: { id: bot.id }, query: { revision: bot.revision } }))
    deleting.value = false
    await app.refresh()
    await router.push('/settings/agents')
  })
}
</script>
<template>
  <div class="settings-sections bot-settings">
    <template v-if="!selected">
      <p
        v-if="route.query.agent"
        class="error-box"
        role="alert"
      >
        {{ t('botMissing') }}
      </p>
      <div class="settings-toolbar">
        <div class="search-input">
          <Search /><input
            v-model="search"
            :placeholder="t('settingsSearchBots')"
            :aria-label="t('settingsSearchBots')"
          >
        </div>
        <Button
          size="small"
          @click="newBot"
        >
          <Plus />{{ t('newAgent') }}
        </Button>
      </div>
      <div
        v-if="app.agents.length"
        class="resource-list"
      >
        <button
          v-for="bot in visibleAgents"
          :key="bot.id"
          type="button"
          class="resource-row"
          @click="openAgent(bot.id)"
        >
          <span class="agent-avatar">{{ bot.avatar || bot.name.slice(0, 1) }}</span><span><strong>{{ bot.name }}</strong><small>{{ app.models.find(m => m.id === bot.default_model_id)?.name || t('agentNoModel') }}</small></span><ChevronRight />
        </button>
        <p
          v-if="!visibleAgents.length"
          class="empty"
        >
          {{ t('agentsNoMatch') }}
        </p>
      </div>
      <div
        v-else
        class="empty-state"
      >
        <strong>{{ t('noAgents') }}</strong><p>{{ t('noAgentsHint') }}</p><Button
          variant="primary"
          @click="newBot"
        >
          {{ t('newAgent') }}
        </Button>
      </div>
    </template>
    <template v-else-if="!platform">
      <div class="settings-title">
        <h1>{{ t(computersPage ? 'botComputerPermissions' : 'botBasics') }}</h1>
      </div>
      <template v-if="!computersPage">
        <SettingsSection :title="t('botIdentity')">
          <SettingsRow :label="t('name')">
            <SettingsText
              :value="selected.name"
              :draft-key="`${resource}/name`"
              :label="t('name')"
              required
              @commit="update(selected!.id, 'name', { name: $event })"
            />
          </SettingsRow>
          <SettingsRow
            :label="t('avatar')"
            :description="t('botAvatarHint')"
          >
            <div class="avatar-control">
              <span class="agent-avatar">{{ selected.avatar || selected.name.slice(0, 1) }}</span><SettingsText
                :value="selected.avatar || ''"
                :draft-key="`${resource}/avatar`"
                :label="t('avatar')"
                :maxlength="8"
                :placeholder="selected.name.slice(0, 1)"
                @commit="update(selected!.id, 'avatar', { avatar: $event })"
              />
            </div>
          </SettingsRow>
          <SettingsFeedback :scope="[`${resource}/name`, `${resource}/avatar`]" />
        </SettingsSection>
        <SettingsSection :title="t('botModelSettings')">
          <SettingsRow
            :label="t('defaultModel')"
            :description="t('defaultModelHint')"
          >
            <Select
              :model-value="saves.value(`${resource}/default-model`, selected.default_model_id || '')"
              :label="t('defaultModel')"
              :options="modelOptions(selected.default_model_id, t('chooseInConversation'))"
              @update:model-value="update(selected!.id, 'default-model', { default_model_id: $event }, $event)"
            />
          </SettingsRow>
          <SettingsRow
            :label="t('probeModel')"
            :description="t('probeModelHint')"
          >
            <Select
              :model-value="saves.value(`${resource}/probe-model`, selected.probe_model_id || '')"
              :label="t('probeModel')"
              :options="modelOptions(selected.probe_model_id, t('followSessionModel'))"
              @update:model-value="update(selected!.id, 'probe-model', { probe_model_id: $event }, $event)"
            />
          </SettingsRow>
          <div class="settings-group-footer">
            <RouterLink
              to="/settings/models"
              class="settings-inline-link"
            >
              {{ t('manageModels') }}<ChevronRight />
            </RouterLink><SettingsFeedback :scope="[`${resource}/default-model`, `${resource}/probe-model`]" />
          </div>
        </SettingsSection>
        <SettingsSection :title="t('botSearchSettings')">
          <SettingsRow
            :label="t('searchServices')"
            :description="t('agentSearchHint')"
          >
            <Select
              :model-value="saves.value(`${resource}/search`, selected.search_provider_id || '')"
              :label="t('webSearch')"
              :options="[{ value: '', label: t('notEnabled') }, ...(searches.data.value || []).map(p => ({ value: p.id, label: p.name }))]"
              @update:model-value="update(selected!.id, 'search', { search_provider_id: $event }, $event)"
            />
          </SettingsRow>
          <div class="settings-group-footer">
            <RouterLink
              to="/settings/search"
              class="settings-inline-link"
            >
              {{ t('manageSearchServices') }}<ChevronRight />
            </RouterLink><SettingsFeedback :scope="`${resource}/search`" />
          </div>
        </SettingsSection>
        <SettingsSection>
          <SettingsRow
            :label="t('deleteAgent')"
            :description="t('deleteAgentHint')"
          >
            <Button
              variant="danger"
              size="small"
              @click="action.error.value = ''; deleting = true"
            >
              {{ t('delete') }}
            </Button>
          </SettingsRow>
        </SettingsSection>
      </template>
      <template v-else>
        <SettingsSection>
          <SettingsRow
            :label="t('defaultComputer')"
            :description="t('defaultComputerHint')"
          >
            <Select
              :model-value="saves.value(`${resource}/default-computer`, selected.default_computer_id || '')"
              :label="t('defaultComputer')"
              :options="[{ value: '', label: t('noDefaultComputer') }, ...allowedComputers.map(c => ({ value: c.id, label: c.name }))]"
              @update:model-value="update(selected!.id, 'default-computer', { default_computer_id: $event }, $event)"
            />
          </SettingsRow>
          <SettingsFeedback :scope="`${resource}/default-computer`" />
        </SettingsSection>
        <SettingsSection
          :title="t('allowedComputers')"
          :description="t('computerPermissionsHint')"
        >
          <SettingsRow
            v-for="computer in computers"
            :key="computer.id"
            :label="computer.name"
            :description="operatingSystem(computer.os)"
          >
            <span
              class="settings-inline-status"
              :class="{ 'danger-text': computer.revoked }"
            >{{ t(computer.revoked ? 'revoked' : computer.online ? 'online' : 'offline') }}</span>
            <Switch
              :model-value="saves.value(`${resource}/computer:${computer.id}`, !!selected.allowed_computers?.includes(computer.id))"
              :disabled="computer.revoked && !selected.allowed_computers?.includes(computer.id)"
              :label="t('grantComputer', { name: computer.name })"
              @update:model-value="permission(selected!.id, computer.id, $event)"
            />
          </SettingsRow>
          <p
            v-if="!computers.length"
            class="empty"
          >
            {{ t('noComputers') }}
          </p>
          <SettingsFeedback
            :scope="computers.map(c => `${resource}/computer:${c.id}`)"
            :saving="t('grantingAccess')"
            :saved="t('accessUpdated')"
          />
        </SettingsSection>
        <SettingsSection>
          <SettingsRow
            :label="t('manageComputerConnections')"
            :description="t('manageComputerConnectionsHint')"
          >
            <RouterLink
              to="/settings/computers"
              class="button button-secondary"
            >
              {{ t('manageComputers') }}<ChevronRight />
            </RouterLink>
          </SettingsRow>
        </SettingsSection>
      </template>
      <ResourceDelete
        :id="selected.id"
        :open="deleting"
        kind="agents"
        :name="selected.name"
        :busy="action.busy.value"
        :error-message="action.error.value"
        @update:open="deleting = $event"
        @confirm="remove"
      />
    </template>
    <KeepAlive>
      <ChannelsSettings
        v-if="selected && platform"
        :agent-i-d="selected.id"
        :platform="platform"
      />
    </KeepAlive>
    <Modal
      :open="!!creating"
      :title="t('newAgent')"
      @update:open="value => { if (!value) creating = undefined }"
    >
      <form
        v-if="creating"
        class="stack"
        @submit.prevent="create"
      >
        <p
          v-if="action.error.value"
          class="error-box"
          role="alert"
        >
          {{ action.error.value }}
        </p>
        <label class="label">{{ t('name') }}<input
          v-model="creating.name"
          required
          autofocus
        ></label>
        <label class="label">{{ t('avatar') }}<input
          v-model="creating.avatar"
          maxlength="8"
          :placeholder="creating.name.slice(0, 1)"
        ></label>
        <label class="label">{{ t('defaultModel') }}<Select
          v-model="creating.default_model_id"
          :label="t('defaultModel')"
          :options="modelOptions('', t('chooseInConversation'))"
        /></label>
        <div class="form-footer">
          <Button
            variant="ghost"
            @click="creating = undefined"
          >
            {{ t('cancel') }}
          </Button><Button
            type="submit"
            variant="primary"
            :busy="action.busy.value"
          >
            {{ t('newAgent') }}
          </Button>
        </div>
      </form>
    </Modal>
  </div>
</template>

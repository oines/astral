<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { useQuery } from '@pinia/colada'
import { listBindingsQuery, listConnectionsQuery, listConversationsQuery, listSearchProvidersQuery } from '@astral/sdk/colada'
import { bindConversation, deleteAgent, saveAgent, unbindConversation, type AstralInternalDomainConversationBinding } from '@astral/sdk'
import { Button, Select, SettingsRow, SettingsSection } from '@astral/ui'
import { ArrowLeft, ChevronRight, Plus, Search, User } from '@astral/icons'
import { useI18n } from 'vue-i18n'
import type { Agent } from '../../lib/types'
import { clone, newMeta, operatingSystem, request } from '../../lib/api'
import { useAction } from '../../composables/useAction'
import { useAppStore } from '../../stores/app'
import ResourceDelete from './ResourceDelete.vue'
import ConversationPicker from './ConversationPicker.vue'
const { t } = useI18n()
const app = useAppStore()
const route = useRoute()
const router = useRouter()
const action = useAction()
const searches = useQuery(listSearchProvidersQuery())
const bindings = useQuery(listBindingsQuery())
const conversations = useQuery(listConversationsQuery())
const connections = useQuery(listConnectionsQuery())
const selected = ref<Agent>()
const tab = ref('identity')
const search = ref('')
const visibleAgents = computed(() => app.agents.filter(a => a.name.toLocaleLowerCase().includes(search.value.toLocaleLowerCase())))
const adding = ref(false)
const deleting = ref(false)
const ownBindings = computed(() => bindings.data.value?.filter(b => b.agent_id === selected.value?.id) ?? [])
function edit(agent?: Agent) {
  tab.value = 'identity'
  action.error.value = ''; action.success.value = ''
  selected.value = agent ? clone(agent) : { ...newMeta(), name: '', avatar: '', allowed_computers: [], default_model_id: '', default_computer_id: '', search_provider_id: '' }
  normalize()
}
watch(() => route.fullPath, () => {
  if (route.path !== '/settings/agents' || typeof route.query.agent !== 'string') return
  const agent = app.agents.find(a => a.id === route.query.agent)
  if (!agent) return
  if (selected.value?.id !== agent.id) edit(agent)
  if (route.query.tab === 'computers') tab.value = 'computers'
}, { immediate: true })
function closeAgent() {
  selected.value = undefined
  if (route.query.agent) void router.replace('/settings/agents')
}
function normalize() {
  if (!selected.value) return
  selected.value.avatar ??= ''
  selected.value.default_model_id ??= ''
  selected.value.default_computer_id ??= ''
  selected.value.search_provider_id ??= ''
  selected.value.allowed_computers ??= []
}
function allowComputer(id: string, checked: boolean) {
  if (!selected.value) return
  selected.value.allowed_computers = checked ? [...new Set([...selected.value.allowed_computers, id])] : selected.value.allowed_computers.filter(x => x !== id)
  if (!checked && selected.value.default_computer_id === id) selected.value.default_computer_id = ''
}
async function save() {
  await action.run(async () => {
    if (selected.value) selected.value = await request(saveAgent({ body: selected.value }))
    normalize()
    await app.refresh()
  }, t('saved'))
}
async function toggle(binding: AstralInternalDomainConversationBinding) {
  await action.run(async () => {
    if (binding.enabled) await request(unbindConversation({ path: { id: binding.id }, query: { revision: binding.revision } }))
    else await request(bindConversation({ body: { agent_id: binding.agent_id, conversation_id: binding.conversation_id } }))
    await app.refresh()
  })
}
async function remove() {
  await action.run(async () => { if (selected.value) await request(deleteAgent({ path: { id: selected.value.id }, query: { revision: selected.value.revision } })); deleting.value = false; selected.value = undefined; await app.refresh() })
}
function conversation(id: string) { return conversations.data.value?.find(c => c.id === id) }
</script>
<template>
  <div class="stack">
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
    <template v-if="!selected">
      <div class="settings-toolbar">
        <div class="search-input">
          <Search /><input
            v-model="search"
            :placeholder="t('searchPlaceholder')"
            :aria-label="t('agents')"
          >
        </div><Button
          size="small"
          @click="edit()"
        >
          <Plus />{{ t('setupAgent') }}
        </Button>
      </div>
      <div
        v-if="app.agents.length"
        class="resource-list"
      >
        <button
          v-for="a in visibleAgents"
          :key="a.id"
          class="resource-row"
          @click="edit(a)"
        >
          <span class="agent-avatar">{{ a.avatar || a.name.slice(0, 1) }}</span><span><strong>{{ a.name }}</strong><small>{{ app.models.find(m => m.id === a.default_model_id)?.name || t('missing') }} · {{ a.allowed_computers.length }} Computers</small></span><ChevronRight />
        </button>
        <p
          v-if="!visibleAgents.length"
          class="empty"
        >
          {{ t('noResults') }}
        </p>
      </div>
      <div
        v-else
        class="empty-state"
      >
        <User /><strong>{{ t('noAgents') }}</strong><p>{{ t('noAgentsHint') }}</p><Button
          variant="primary"
          @click="edit()"
        >
          {{ t('setupAgent') }}
        </Button>
      </div>
    </template>
    <form
      v-else
      class="stack settings-form"
      @submit.prevent="save"
    >
      <div class="object-heading">
        <Button
          variant="ghost"
          size="icon"
          :aria-label="t('agents')"
          @click="closeAgent"
        >
          <ArrowLeft />
        </Button><span class="agent-avatar">{{ selected.avatar || selected.name.slice(0, 1) || 'A' }}</span><h2>{{ selected.name || t('setupAgent') }}</h2>
      </div>
      <div
        class="settings-tabs"
        role="tablist"
        :aria-label="t('agents')"
      >
        <button
          v-for="item in [['identity', 'agentIdentity'], ['computers', 'agentComputerAccess'], ['channels', 'agentChannels']]"
          :key="item[0]"
          type="button"
          role="tab"
          :aria-selected="tab === item[0]"
          :disabled="item[0] === 'channels' && !selected.id"
          @click="tab = item[0]!"
        >
          {{ t(item[1]!) }}
        </button>
      </div>
      <template v-if="tab === 'identity'">
        <SettingsSection :title="t('agentIdentity')">
          <SettingsRow :label="t('name')">
            <input
              v-model="selected.name"
              :aria-label="t('name')"
              required
            >
          </SettingsRow>
          <SettingsRow :label="t('avatar')">
            <div class="avatar-control">
              <span class="agent-avatar">{{ selected.avatar || selected.name.slice(0, 1) || 'A' }}</span><input
                v-model="selected.avatar"
                :aria-label="t('avatar')"
                maxlength="8"
                :placeholder="selected.name.slice(0, 1)"
              >
            </div>
          </SettingsRow>
        </SettingsSection>
        <SettingsSection :title="t('agentCapabilities')">
          <SettingsRow :label="t('defaultModel')">
            <Select
              v-model="selected.default_model_id"
              :label="t('defaultModel')"
              :placeholder="t('missing')"
              :options="[{ value: '', label: t('none') }, ...app.availableModels.map(m => ({ value: m.id, label: m.name }))]"
            /><RouterLink
              v-if="!app.models.length"
              to="/settings/models"
              class="small"
            >
              {{ t('setupModels') }}
            </RouterLink>
          </SettingsRow>
          <SettingsRow :label="t('webSearch')">
            <Select
              v-model="selected.search_provider_id"
              :label="t('webSearch')"
              :options="[{ value: '', label: t('none') }, ...(searches.data.value || []).map(s => ({ value: s.id, label: s.name }))]"
            /><RouterLink
              v-if="!searches.data.value?.length"
              to="/settings/search"
              class="small"
            >
              {{ t('configure') }}{{ t('webSearch') }}
            </RouterLink>
          </SettingsRow>
        </SettingsSection>
      </template>
      <template v-else-if="tab === 'computers'">
        <SettingsSection :title="t('allowedComputers')">
          <label
            v-for="c in app.computers"
            :key="c.id"
            class="computer-check"
          ><input
            type="checkbox"
            :checked="selected.allowed_computers.includes(c.id)"
            :disabled="c.revoked && !selected.allowed_computers.includes(c.id)"
            @change="allowComputer(c.id, ($event.target as HTMLInputElement).checked)"
          ><span><strong>{{ c.name }}</strong><small>{{ operatingSystem(c.os) }}</small></span><span
            class="badge"
            :class="{ good: c.online, bad: c.revoked }"
          >{{ t(c.revoked ? 'revoked' : c.online ? 'online' : 'offline') }}</span></label>
          <div
            v-if="!app.computers.length"
            class="empty-state"
          >
            <p>{{ t('noComputersHint') }}</p><RouterLink
              to="/settings/computers"
              class="button button-secondary"
            >
              {{ t('newComputer') }}
            </RouterLink>
          </div>
        </SettingsSection>
        <SettingsSection>
          <SettingsRow :label="t('defaultComputer')">
            <Select
              v-model="selected.default_computer_id"
              :label="t('defaultComputer')"
              :options="[{ value: '', label: t('none') }, ...app.computers.filter(c => selected?.allowed_computers.includes(c.id)).map(c => ({ value: c.id, label: c.name, description: t(c.online ? 'online' : 'offline') }))]"
            />
          </SettingsRow>
        </SettingsSection>
        <p class="section-caption">
          {{ t('computerAccessHint') }}
        </p>
      </template>
      <template v-else-if="selected.id">
        <SettingsSection :title="t('conversations')">
          <template #actions>
            <Button
              size="small"
              @click="adding = true"
            >
              <Plus />{{ t('add') }}
            </Button>
          </template>
          <div
            v-for="b in ownBindings"
            :key="b.id"
            class="resource-row"
          >
            <span><strong>{{ conversation(b.conversation_id)?.title || t('missing') }}</strong><small>Telegram · {{ connections.data.value?.find(c => c.id === conversation(b.conversation_id)?.connection_id)?.name }}</small><RouterLink
              :to="`/sessions/${b.session_id}`"
              class="small"
            >{{ t('openConversationHistory') }}</RouterLink></span><Button
              size="small"
              :variant="b.enabled ? 'secondary' : 'ghost'"
              :busy="action.busy.value"
              @click="toggle(b)"
            >
              {{ t(b.enabled ? 'unbind' : 'enableAgain') }}
            </Button>
          </div>
          <div
            v-if="!ownBindings.length"
            class="empty-state"
          >
            <strong>{{ t('noBindings') }}</strong><p>{{ t('noBindingsHint') }}</p><Button @click="adding = true">
              {{ t('add') }}{{ t('conversations') }}
            </Button>
          </div>
        </SettingsSection><p class="section-caption">
          {{ t('rebindHint') }}
        </p><ConversationPicker
          v-model:open="adding"
          :agent-i-d="selected.id"
        />
      </template>
      <div
        v-if="tab !== 'channels'"
        class="form-footer"
      >
        <Button @click="closeAgent">
          {{ t('cancel') }}
        </Button><Button
          type="submit"
          variant="primary"
          :busy="action.busy.value"
        >
          {{ t('save') }}
        </Button>
      </div>
      <div
        v-if="selected.id && tab === 'identity'"
        class="danger-row"
      >
        <strong>{{ t('delete') }} Agent</strong><Button
          size="small"
          variant="danger"
          @click="deleting = true"
        >
          {{ t('delete') }}
        </Button>
      </div>
      <ResourceDelete
        :id="selected.id"
        :open="deleting"
        kind="agents"
        :name="selected.name"
        :busy="action.busy.value"
        @update:open="deleting = $event"
        @confirm="remove"
      />
    </form>
  </div>
</template>

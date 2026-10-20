<script setup lang="ts">
import { computed, ref } from 'vue'
import { useQuery } from '@pinia/colada'
import { listBindingsQuery, listConnectionsQuery, listConversationsQuery, listSearchProvidersQuery } from '@astral/sdk/colada'
import { bindConversation, deleteAgent, saveAgent, unbindConversation, type AstralInternalDomainConversationBinding } from '@astral/sdk'
import { Button } from '@astral/ui'
import { ArrowLeft, ChevronRight, Plus } from '@astral/icons'
import { useI18n } from 'vue-i18n'
import type { Agent } from '../../lib/types'
import { clone, newMeta, request } from '../../lib/api'
import { useAction } from '../../composables/useAction'
import { useAppStore } from '../../stores/app'
import ResourceDelete from './ResourceDelete.vue'
import ConversationPicker from './ConversationPicker.vue'
const { t } = useI18n()
const app = useAppStore()
const action = useAction()
const searches = useQuery(listSearchProvidersQuery())
const bindings = useQuery(listBindingsQuery())
const conversations = useQuery(listConversationsQuery())
const connections = useQuery(listConnectionsQuery())
const selected = ref<Agent>()
const adding = ref(false)
const deleting = ref(false)
const ownBindings = computed(() => bindings.data.value?.filter(b => b.agent_id === selected.value?.id) ?? [])
function edit(agent?: Agent) {
  selected.value = agent ? clone(agent) : { ...newMeta(), name: '', avatar: '', allowed_computers: [], default_model_id: '', default_computer_id: '', search_provider_id: '' }
  normalize()
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
    >
      {{ action.success.value }}
    </p>
    <template v-if="!selected">
      <div class="row between">
        <p class="muted">
          {{ t('agentsHint') }}
        </p><Button
          size="small"
          @click="edit()"
        >
          <Plus />{{ t('add') }}
        </Button>
      </div>
      <div class="resource-list">
        <button
          v-for="a in app.agents"
          :key="a.id"
          class="resource-row"
          @click="edit(a)"
        >
          <span class="agent-avatar">{{ a.avatar || a.name.slice(0, 1) }}</span><span><strong>{{ a.name }}</strong><small>{{ app.models.find(m => m.id === a.default_model_id)?.name || t('missing') }} · {{ a.allowed_computers.length }} Computers</small></span><ChevronRight />
        </button><p
          v-if="!app.agents.length"
          class="empty"
        >
          {{ t('empty') }}
        </p>
      </div>
    </template>
    <form
      v-else
      class="stack"
      @submit.prevent="save"
    >
      <div class="row between">
        <Button
          variant="ghost"
          size="small"
          @click="selected = undefined"
        >
          <ArrowLeft />{{ t('agents') }}
        </Button><Button
          v-if="selected.id"
          variant="ghost"
          size="small"
          @click="deleting = true"
        >
          {{ t('delete') }}
        </Button>
      </div>
      <div class="agent-identity-edit">
        <span class="agent-avatar large">{{ selected.avatar || selected.name.slice(0, 1) || 'A' }}</span><label class="label grow">{{ t('name') }}<input
          v-model="selected.name"
          required
        ></label><label class="label avatar-field">{{ t('avatar') }}<input
          v-model="selected.avatar"
          maxlength="8"
          placeholder="✳"
        ></label>
      </div>
      <label class="label">{{ t('defaultModel') }}<select v-model="selected.default_model_id"><option value="">{{ t('none') }}</option><option
        v-if="selected.default_model_id && !app.models.some(m => m.id === selected?.default_model_id)"
        :value="selected.default_model_id"
      >{{ t('missing') }}</option><option
        v-for="m in app.models"
        :key="m.id"
        :value="m.id"
      >{{ m.name }} · {{ m.verification?.tool_calling || 'unverified' }}</option></select><RouterLink
        v-if="!app.models.length"
        to="/settings/models"
        class="muted"
      >{{ t('setupModels') }}</RouterLink></label>
      <label class="label">{{ t('webSearch') }}<select v-model="selected.search_provider_id"><option value="">{{ t('none') }}</option><option
        v-if="selected.search_provider_id && !searches.data.value?.some(s => s.id === selected?.search_provider_id)"
        :value="selected.search_provider_id"
      >{{ t('missing') }}</option><option
        v-for="s in searches.data.value"
        :key="s.id"
        :value="s.id"
      >{{ s.name }} · {{ s.adapter }}</option></select></label>
      <div class="stack">
        <h3>{{ t('allowedComputers') }}</h3><div class="computer-checks">
          <label
            v-for="c in app.computers"
            :key="c.id"
            class="computer-check"
          ><input
            type="checkbox"
            :checked="selected.allowed_computers.includes(c.id)"
            :disabled="c.revoked && !selected.allowed_computers.includes(c.id)"
            @change="allowComputer(c.id, ($event.target as HTMLInputElement).checked)"
          ><span><strong>{{ c.name }}</strong><small>{{ c.os }} · {{ c.kind }}</small></span><span
            class="badge"
            :class="{ good: c.online, bad: c.revoked }"
          >{{ c.revoked ? 'Revoked' : t(c.online ? 'online' : 'offline') }}</span></label>
        </div><RouterLink
          v-if="!app.computers.length"
          to="/settings/computers"
        >
          {{ t('newComputer') }}
        </RouterLink>
      </div>
      <label class="label">{{ t('defaultComputer') }}<select v-model="selected.default_computer_id"><option value="">{{ t('none') }}</option><option
        v-for="c in app.computers.filter(c => selected?.allowed_computers.includes(c.id))"
        :key="c.id"
        :value="c.id"
      >{{ c.name }}{{ c.online ? '' : ' · ' + t('offline') }}</option></select></label>
      <div class="form-footer">
        <Button
          type="submit"
          variant="primary"
          :busy="action.busy.value"
        >
          {{ t('save') }}
        </Button>
      </div>
      <template v-if="selected.id">
        <hr class="separator"><div class="row between">
          <h2>{{ t('conversations') }}</h2><Button
            size="small"
            @click="adding = true"
          >
            <Plus />{{ t('add') }}
          </Button>
        </div>
        <div class="resource-list">
          <div
            v-for="b in ownBindings"
            :key="b.id"
            class="resource-row"
          >
            <span><strong>{{ conversation(b.conversation_id)?.title || t('missing') }}</strong><small>Telegram · {{ connections.data.value?.find(c => c.id === conversation(b.conversation_id)?.connection_id)?.name }} · {{ conversation(b.conversation_id)?.kind }}<span v-if="conversation(b.conversation_id)?.topic_id"> · Topic {{ conversation(b.conversation_id)?.topic_id }}</span></small><RouterLink
              :to="`/sessions/${b.session_id}`"
              class="small"
            >{{ t('openConversationHistory') }}</RouterLink></span>
            <Button
              size="small"
              :variant="b.enabled ? 'secondary' : 'ghost'"
              :busy="action.busy.value"
              @click="toggle(b)"
            >
              {{ t(b.enabled ? 'unbind' : 'enableAgain') }}
            </Button>
          </div><p
            v-if="!ownBindings.length"
            class="empty"
          >
            {{ t('empty') }}
          </p>
        </div><p class="muted small">
          {{ t('rebindHint') }}
        </p>
        <ConversationPicker
          v-model:open="adding"
          :agent-i-d="selected.id"
        />
      </template>
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

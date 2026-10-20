<script setup lang="ts">
import { computed, ref } from 'vue'
import { useQuery } from '@pinia/colada'
import { listBindingsQuery, listConnectionsQuery, listConversationsQuery } from '@astral/sdk/colada'
import { conversationMessages, deleteConnection, observationDelivery, resolveConversation, saveConnection, type AstralInternalHttpapiConnectionInput, type AstralInternalHttpapiConnectionView, type AstralInternalGatewayMessagePage, type AstralInternalPersistenceDelivery } from '@astral/sdk'
import { Button, Modal } from '@astral/ui'
import { ArrowLeft, ChevronRight, MessageCircle, Plus } from '@astral/icons'
import { useI18n } from 'vue-i18n'
import { newMeta, object, request } from '../../lib/api'
import { useAction } from '../../composables/useAction'
import { useAppStore } from '../../stores/app'
import OutboxReceipts from '../OutboxReceipts.vue'
const { t } = useI18n()
const app = useAppStore()
const action = useAction()
const list = useQuery(listConnectionsQuery())
const conversations = useQuery(listConversationsQuery())
const bindings = useQuery(listBindingsQuery())
const selected = ref<AstralInternalHttpapiConnectionInput>()
const token = ref('')
const endpoint = ref('')
const deleting = ref(false)
const chatID = ref('')
const topicID = ref('')
const messages = ref<AstralInternalGatewayMessagePage>()
const selectedConversation = ref('')
const deliveries = ref<Record<string, AstralInternalPersistenceDelivery>>({})
const status = computed(() => list.data.value?.find(c => c.id === selected.value?.id))
const ownConversations = computed(() => conversations.data.value?.filter(c => c.connection_id === selected.value?.id) ?? [])
function edit(value?: AstralInternalHttpapiConnectionView) {
  selected.value = value ? { id: value.id, revision: value.revision, created_at: value.created_at, updated_at: value.updated_at, name: value.name, platform: value.platform, adapter: value.adapter, enabled: value.enabled, account_id: value.account_id, username: value.username } : { ...newMeta(), name: '', platform: 'telegram', adapter: 'telegram.bot', enabled: true }
  token.value = ''
  endpoint.value = String(object(value?.config).base_url || '')
  messages.value = undefined
  selectedConversation.value = ''
}
async function save() {
  await action.run(async () => {
    if (!selected.value) return
    const saved = await request(saveConnection({ body: { ...selected.value, config: endpoint.value ? { base_url: endpoint.value } : {}, ...(token.value ? { token: token.value } : {}) } }))
    selected.value = saved
    token.value = ''
    await app.refresh()
  }, t('saved'))
}
async function verifyChat() {
  await action.run(async () => {
    if (!selected.value) return
    await request(resolveConversation({ path: { id: selected.value.id }, body: { external_id: chatID.value.trim(), topic_id: topicID.value.trim() } }))
    await conversations.refetch()
  })
}
async function showMessages(id: string) {
  selectedConversation.value = id
  await action.run(async () => { messages.value = await request(conversationMessages({ path: { id }, query: { limit: 100 } })) })
}
async function delivery(id: string) {
  await action.run(async () => { deliveries.value[id] = await request(observationDelivery({ path: { id } })) })
}
async function remove() {
  await action.run(async () => {
    if (!selected.value) return
    await request(deleteConnection({ path: { id: selected.value.id }, query: { revision: selected.value.revision } }))
    selected.value = undefined
    deleting.value = false
    await app.refresh()
  })
}
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
          Telegram Bot API
        </p><Button
          size="small"
          @click="edit()"
        >
          <Plus />{{ t('add') }} Telegram
        </Button>
      </div>
      <div class="resource-list">
        <button
          v-for="c in list.data.value"
          :key="c.id"
          class="resource-row"
          @click="edit(c)"
        >
          <span class="resource-symbol"><MessageCircle /></span><span><strong>{{ c.name }}</strong><small>@{{ c.username }} · Telegram</small></span><span
            class="badge"
            :class="{ good: c.status.state === 'polling', bad: Boolean(c.status.error) }"
          >{{ c.enabled ? c.status.state : t('disabled') }}</span><ChevronRight />
        </button><p
          v-if="!list.data.value?.length"
          class="empty"
        >
          {{ t('empty') }}
        </p>
      </div>
      <h2>{{ t('outboundReceipts') }}</h2><OutboxReceipts all />
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
          <ArrowLeft />{{ t('channels') }}
        </Button><Button
          v-if="selected.id"
          variant="ghost"
          size="small"
          @click="deleting = true"
        >
          {{ t('delete') }}
        </Button>
      </div>
      <label class="label">{{ t('name') }}<input
        v-model="selected.name"
        required
      ></label>
      <label class="label">{{ t('botToken') }}<input
        v-model="token"
        type="password"
        autocomplete="new-password"
        :placeholder="selected.id ? t('storedSecret') : t('botTokenHint')"
      ></label>
      <label class="row"><input
        v-model="selected.enabled"
        type="checkbox"
      >{{ t('enabled') }}</label>
      <details class="form-disclosure">
        <summary>{{ t('customBotServer') }}</summary><label class="label">Base URL<input
          v-model="endpoint"
          type="url"
          placeholder="https://api.telegram.org"
        ></label>
      </details>
      <div class="form-footer">
        <Button
          type="submit"
          variant="primary"
          :busy="action.busy.value"
        >
          {{ t(selected.enabled ? 'saveAndVerify' : 'save') }}
        </Button>
      </div>
      <template v-if="status">
        <div class="notice">
          <strong>@{{ status.username }} · {{ status.status.state }}</strong><p v-if="status.status.error">
            {{ status.status.error }}
          </p><p
            v-for="diagnostic in status.diagnostics"
            :key="diagnostic"
          >
            {{ diagnostic }}
          </p><p class="muted small">
            {{ t('edits') }}: {{ t(status.capabilities.message_edits ? 'supported' : 'unavailable') }} · {{ t('deletionEvents') }}: {{ t(status.capabilities.deletion_events ? 'supported' : 'unavailable') }} · {{ t('deliveryReceipts') }}: {{ t(status.capabilities.delivery_receipts ? 'supported' : 'unavailable') }}
          </p>
        </div>
        <h2>{{ t('conversations') }}</h2><p class="muted">
          {{ t('channelChatsHint') }}
        </p>
        <div class="row end">
          <label class="label grow">{{ t('chatID') }}<input v-model="chatID"></label><label class="label">{{ t('topicID') }}<input v-model="topicID"></label><Button
            :busy="action.busy.value"
            :disabled="!chatID.trim()"
            @click="verifyChat"
          >
            {{ t('verifyAccess') }}
          </Button>
        </div>
        <div class="resource-list">
          <button
            v-for="c in ownConversations"
            :key="c.id"
            class="resource-row"
            @click="showMessages(c.id)"
          >
            <span><strong>{{ c.title }}</strong><small>{{ c.kind }}{{ c.topic_id ? ' · Topic ' + c.topic_id : '' }}</small></span><span class="badge">{{ t(bindings.data.value?.some(b => b.conversation_id === c.id && b.enabled) ? 'bound' : 'unbound') }}</span><ChevronRight />
          </button>
        </div>
        <RouterLink to="/settings/agents">
          {{ t('assign') }}<ChevronRight style="display: inline" />
        </RouterLink>
      </template>
      <Modal
        :open="Boolean(selectedConversation)"
        :title="t('conversations')"
        @update:open="v => { if (!v) selectedConversation = '' }"
      >
        <div
          v-for="item in messages?.items"
          :key="item.observation.id"
          class="observation"
        >
          <div class="row between">
            <strong>{{ item.observation.sender.name }}</strong><span class="muted small">{{ item.observation.kind }}</span>
          </div><p>{{ item.observation.text }}</p><span class="muted small">{{ new Date(item.observation.sent_at).toLocaleString() }} · {{ item.observation.trigger || t('observation') }}</span><Button
            size="small"
            variant="ghost"
            @click="delivery(item.observation.id)"
          >
            {{ t('deliveryDetails') }}
          </Button><p
            v-if="deliveries[item.observation.id]"
            class="notice"
          >
            {{ deliveries[item.observation.id]?.state }}<br>{{ deliveries[item.observation.id]?.error }}
          </p>
        </div><p
          v-if="!messages?.items?.length"
          class="muted"
        >
          {{ t('empty') }}
        </p>
      </Modal>
      <Modal
        v-model:open="deleting"
        :title="t('delete') + ' ' + selected.name"
      >
        <p class="muted">
          {{ t('deleteConnectionHint') }}
        </p>
        <p
          v-for="c in ownConversations"
          :key="c.id"
        >
          {{ c.title }} · {{ bindings.data.value?.some(b => b.conversation_id === c.id && b.enabled) ? 'Bound' : 'Unbound' }}
        </p>
        <Button
          variant="danger"
          :busy="action.busy.value"
          @click="remove"
        >
          {{ t('delete') }}
        </Button>
      </Modal>
    </form>
  </div>
</template>

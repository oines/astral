<script setup lang="ts">
import { computed, ref } from 'vue'
import { useQuery } from '@pinia/colada'
import { listBindingsQuery, listConnectionsQuery, listConversationsQuery } from '@astral/sdk/colada'
import { bindConversation, resolveConversation } from '@astral/sdk'
import { Button, Modal, Select } from '@astral/ui'
import { useI18n } from 'vue-i18n'
import { useAction } from '../../composables/useAction'
import { request } from '../../lib/api'
import { useAppStore } from '../../stores/app'
const props = defineProps<{ open: boolean; agentID: string }>()
const emit = defineEmits<{ 'update:open': [value: boolean]; saved: [] }>()
const { t } = useI18n()
const app = useAppStore()
const action = useAction()
const connections = useQuery(listConnectionsQuery())
const conversations = useQuery(listConversationsQuery())
const bindings = useQuery(listBindingsQuery())
const connectionID = ref('')
const conversationID = ref('')
const externalID = ref('')
const topicID = ref('')
const acknowledge = ref(false)
const current = computed(() => connections.data.value?.find(c => c.id === connectionID.value))
const options = computed(() => conversations.data.value?.filter(c => c.connection_id === connectionID.value) ?? [])
const existing = computed(() => bindings.data.value?.find(b => b.conversation_id === conversationID.value && b.enabled && b.agent_id !== props.agentID))
async function verify() {
  await action.run(async () => {
    const conversation = await request(resolveConversation({ path: { id: connectionID.value }, body: { external_id: externalID.value.trim(), topic_id: topicID.value.trim() } }))
    await conversations.refetch()
    conversationID.value = conversation.id
  })
}
async function save() {
  await action.run(async () => {
    await request(bindConversation({ body: { agent_id: props.agentID, conversation_id: conversationID.value } }))
    await app.refresh()
    emit('saved')
    emit('update:open', false)
  })
}
</script>
<template>
  <Modal
    :open="open"
    :title="t('add') + ' · ' + t('conversations')"
    @update:open="emit('update:open', $event)"
  >
    <p
      v-if="action.error.value"
      class="error-box"
    >
      {{ action.error.value }}
    </p>
    <label class="label">{{ t('channels') }}<Select
      v-model="connectionID"
      :label="t('channels')"
      :placeholder="t('select')"
      :options="(connections.data.value || []).filter(c => c.enabled).map(c => ({ value: c.id, label: c.name, description: '@' + c.username }))"
      @update:model-value="conversationID = ''; acknowledge = false"
    /></label>
    <template v-if="current">
      <label
        v-if="options.length"
        class="label"
      >{{ t('conversations') }}<Select
        v-model="conversationID"
        :label="t('conversations')"
        :placeholder="t('select')"
        :options="options.map(c => ({ value: c.id, label: c.title, description: c.kind + (c.topic_id ? ' · Topic ' + c.topic_id : '') }))"
        @update:model-value="acknowledge = false"
      /></label>
      <div class="detail-block stack">
        <p
          v-if="!current.capabilities.enumerate_conversations"
          class="muted"
        >
          {{ t('conversationsHint') }}
        </p>
        <div class="field-grid">
          <label class="label">{{ t('chatID') }}<input
            v-model="externalID"
            placeholder="-100… / user ID"
          ></label><label
            v-if="current.capabilities.conversation_kinds.includes('topic')"
            class="label"
          >{{ t('optionalTopicID') }}<input v-model="topicID"></label>
        </div>
        <Button
          class="align-start"
          :busy="action.busy.value"
          :disabled="!externalID.trim()"
          @click="verify"
        >
          {{ t('verifyConversation') }}
        </Button>
      </div>
      <label
        v-if="existing"
        class="notice row"
      ><input
        v-model="acknowledge"
        type="checkbox"
      ><span>{{ t('reassignHint', { name: app.agents.find(a => a.id === existing?.agent_id)?.name || t('missing') }) }}</span></label>
      <Button
        variant="primary"
        :busy="action.busy.value"
        :disabled="!conversationID || (Boolean(existing) && !acknowledge)"
        @click="save"
      >
        {{ t('save') }}
      </Button>
    </template>
    <RouterLink
      v-if="!connections.data.value?.some(c => c.enabled)"
      to="/settings/channels"
      @click="emit('update:open', false)"
    >
      {{ t('add') }} Telegram
    </RouterLink>
  </Modal>
</template>

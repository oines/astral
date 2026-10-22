<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { useQuery } from '@pinia/colada'
import { listBindingsQuery, listConnectionsQuery, listConversationsQuery } from '@astral/sdk/colada'
import { bindConversation, resolveConversation } from '@astral/sdk'
import { Button, Modal, Select } from '@astral/ui'
import { useI18n } from 'vue-i18n'
import { useAction } from '../../composables/useAction'
import { request } from '../../lib/api'
import { useAppStore } from '../../stores/app'
const props = defineProps<{ open: boolean; agentID: string; connectionID: string; conversationID?: string }>()
const emit = defineEmits<{ 'update:open': [value: boolean]; saved: [] }>()
const { t } = useI18n()
const app = useAppStore()
const action = useAction()
const connections = useQuery(listConnectionsQuery())
const conversations = useQuery(listConversationsQuery())
const bindings = useQuery(listBindingsQuery())
const selectedConversationID = ref('')
const sessionID = ref('')
const externalID = ref('')
const topicID = ref('')
const manual = ref(false)
const current = computed(() => connections.data.value?.find(c => c.id === props.connectionID && c.agent_id === props.agentID))
const chatIDCommand = computed(() => current.value?.username ? `/chat_id@${current.value.username}` : '/chat_id')
const options = computed(() => conversations.data.value?.filter(c => c.connection_id === props.connectionID && (c.id === props.conversationID || c.id === selectedConversationID.value || !bindings.data.value?.some(b => b.conversation_id === c.id && b.enabled))) ?? [])
const selectedChat = computed(() => options.value.find(c => c.id === selectedConversationID.value))
const sessions = computed(() => app.sessions.filter(s => s.agent_id === props.agentID && !s.binding_id && !s.archived))
watch(() => props.open, value => {
  if (!value) return
  selectedConversationID.value = props.conversationID || ''; sessionID.value = ''; externalID.value = ''; topicID.value = ''; manual.value = false; action.error.value = ''
  void Promise.allSettled([conversations.refetch(), bindings.refetch()])
})
async function verify() {
  await action.run(async () => {
    const conversation = await request(resolveConversation({ path: { id: props.connectionID }, body: { external_id: externalID.value.trim(), topic_id: topicID.value.trim() } }))
    await conversations.refetch()
    selectedConversationID.value = conversation.id
    manual.value = false
  })
}
async function save() {
  await action.run(async () => {
    await request(bindConversation({ body: { conversation_id: selectedConversationID.value, session_id: sessionID.value || undefined } }))
    await app.refresh()
    emit('saved')
    emit('update:open', false)
  })
}
</script>
<template>
  <Modal
    :open="open"
    :title="t(props.conversationID ? 'changeSession' : 'addChat')"
    @update:open="emit('update:open', $event)"
  >
    <p
      v-if="action.error.value"
      class="error-box"
      role="alert"
    >
      {{ action.error.value }}
    </p>
    <template v-if="current">
      <template v-if="!props.conversationID">
        <label
          v-if="options.length"
          class="label"
        >{{ t('chooseChat') }}<Select
          v-model="selectedConversationID"
          :label="t('chooseChat')"
          :placeholder="t('chooseChat')"
          :options="options.map(c => ({ value: c.id, label: c.title, description: c.kind === 'topic' ? t('topicChat', { id: c.topic_id }) : t(c.kind === 'private' ? 'privateChat' : c.kind === 'channel' ? 'channelChat' : 'groupChat') }))"
        /></label>
        <p
          v-else
          class="muted"
        >
          {{ t('discoverChatHint') }}
        </p>
        <p
          v-if="current.adapter === 'telegram.bot'"
          class="muted small"
        >
          {{ t('telegramChatIDHint', { command: chatIDCommand }) }}
        </p>
        <button
          v-if="!manual"
          type="button"
          class="text-button"
          @click="manual = true"
        >
          {{ t('findChatByID') }}
        </button>
        <form
          v-else
          class="stack chat-lookup"
          @submit.prevent="verify"
        >
          <div class="field-grid">
            <label class="label">{{ t('chatID') }}<input
              v-model="externalID"
              placeholder="-100… / user ID"
              required
            ></label>
            <label class="label">{{ t('optionalTopicID') }}<input v-model="topicID"></label>
          </div>
          <div class="row">
            <Button
              size="small"
              :busy="action.busy.value"
              :disabled="!externalID.trim()"
              type="submit"
            >
              {{ t('findChatByIDAction') }}
            </Button><Button
              size="small"
              variant="ghost"
              @click="manual = false"
            >
              {{ t('cancel') }}
            </Button>
          </div>
        </form>
      </template>
      <p
        v-else-if="selectedChat"
        class="chat-picker-name"
      >
        {{ selectedChat.title }}
      </p>
      <template v-if="selectedConversationID">
        <label
          v-if="sessions.length || props.conversationID"
          class="label"
        >{{ t('chatConversation') }}<Select
          v-model="sessionID"
          :label="t('chatConversation')"
          :options="[{ value: '', label: t('createChatSession') }, ...sessions.map(s => ({ value: s.id, label: s.title }))]"
        /></label>
        <p class="muted small">
          {{ t(props.conversationID ? 'replaceSessionHint' : sessionID ? 'bindSessionHint' : 'newChatConversationHint') }}
        </p>
      </template>
      <div class="form-footer">
        <Button
          variant="ghost"
          @click="emit('update:open', false)"
        >
          {{ t('cancel') }}
        </Button><Button
          variant="primary"
          :busy="action.busy.value"
          :disabled="!selectedConversationID"
          @click="save"
        >
          {{ t(props.conversationID ? 'changeSession' : 'addChat') }}
        </Button>
      </div>
    </template>
  </Modal>
</template>

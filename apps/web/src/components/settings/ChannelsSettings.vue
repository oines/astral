<script setup lang="ts">
import QRCode from 'qrcode'
import { computed, onActivated, onDeactivated, reactive, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { useIntervalFn } from '@vueuse/core'
import { useQuery } from '@pinia/colada'
import { listBindingsQuery, listConnectionsQuery, listConnectionsQueryKey, listConversationsQuery, listConversationsQueryKey } from '@astral/sdk/colada'
import { startWeixinLogin, pollWeixinLogin, deleteConnection, listConnections, listConversations, saveConnection, unbindConversation, setConversationProactive, type AstralInternalHttpapiConnectionInput, type AstralInternalHttpapiConnectionView, type AstralInternalDomainConversation, type AstralInternalDomainConnection } from '@astral/sdk'
import { Button, Modal, Select, Switch, SettingsRow, SettingsSection } from '@astral/ui'
import { ArrowLeft, ChevronRight, Plus, Search } from '@astral/icons'
import { useI18n } from 'vue-i18n'
import { errorText, newMeta, object, request } from '../../lib/api'
import { useAction } from '../../composables/useAction'
import { useSettingsSave } from '../../composables/useSettingsSave'
import { useAppStore } from '../../stores/app'
import ChannelIcon from '../ChannelIcon.vue'
import ConversationPicker from './ConversationPicker.vue'
import SettingsFeedback from './SettingsFeedback.vue'
import SettingsText from './SettingsText.vue'
import IMConnectionFields from './IMConnectionFields.vue'
import { adapterInfo, adapterLabel, imAdapters, platformLabel } from '../../lib/im-platforms'

type Connection = AstralInternalDomainConnection
type Conversation = AstralInternalDomainConversation
const props = defineProps<{ agentID: string; platform: string }>()
const { t, locale } = useI18n()
const app = useAppStore()
const route = useRoute()
const router = useRouter()
const action = useAction()
const createAction = useAction()
const saves = useSettingsSave()
const list = useQuery(listConnectionsQuery())
const conversations = useQuery(listConversationsQuery())
const bindings = useQuery(listBindingsQuery())
const polling = useIntervalFn(() => {
  if (document.visibilityState === 'visible') void Promise.allSettled([list.refetch(), conversations.refetch(), bindings.refetch()])
}, 3000)
onActivated(polling.resume)
onDeactivated(polling.pause)
const platformName = computed(() => platformLabel(props.platform, locale.value))
const ownAccounts = computed(() => list.data.value?.filter(c => c.agent_id === props.agentID && c.platform === props.platform) || [])
const accountID = computed(() => typeof route.query.account === 'string' ? route.query.account : '')
const account = computed(() => ownAccounts.value.find(c => c.id === accountID.value))
const resource = computed(() => `connection:${accountID.value}`)
const search = ref('')
const ownConversations = computed(() => conversations.data.value?.filter(c => c.connection_id === accountID.value && chatBinding(c.id)) || [])
const visibleConversations = computed(() => ownConversations.value.filter(c => c.title.toLocaleLowerCase().includes(search.value.toLocaleLowerCase())))
type Draft = { values: Record<string, string>; token: string; baseline: string }
const drafts = reactive<Record<string, Draft>>({})
const draft = computed(() => drafts[accountID.value])
const dirty = computed(() => !!draft.value && (!!draft.value.token || JSON.stringify(draft.value.values) !== draft.value.baseline))
const hasHistory = computed(() => conversations.data.value?.some(c => c.connection_id === accountID.value))
const creating = ref<{ agentID: string; platform: string; adapter: string; name: string; token: string; values: Record<string, string>; connectionID?: string }>()
const login = ref<{ id: string; qrcode_url: string; status: string }>()
const qrImage = ref('')
watch(() => login.value?.qrcode_url, async content => {
  qrImage.value = ''
  if (!content) return
  try {
    const image = await QRCode.toDataURL(content, { width: 220, margin: 2 })
    if (login.value?.qrcode_url === content) qrImage.value = image
  } catch { createAction.error.value = t('weixinQRFailed') }
})
const qrLink = computed(() => login.value?.qrcode_url.startsWith('https://') ? login.value.qrcode_url : undefined)
const verifyCode = ref('')
const qrPolling = ref(false)
const pageActive = ref(true)
let connectionAttempt = 0
const qrLoop = useIntervalFn(() => { if (login.value && !['expired', 'verify_code_blocked', 'binded_redirect', 'need_verifycode'].includes(login.value.status) && creating.value && !qrPolling.value && !createAction.busy.value) void pollQR() }, 2000)
onActivated(() => { pageActive.value = true; qrLoop.resume() })
onDeactivated(() => { pageActive.value = false; qrLoop.pause(); login.value = undefined })
watch(() => `${props.agentID}:${props.platform}`, () => {
  if (creating.value && (creating.value.agentID !== props.agentID || creating.value.platform !== props.platform)) { creating.value = undefined; login.value = undefined; connectionAttempt++; createAction.busy.value = false; createAction.error.value = '' }
})
const deleting = ref(false)
const adding = ref(false)
const rebindID = ref('')
const removing = ref<Conversation>()
const expanding = ref<Connection>()
watch(account, value => {
  if (!value) return
  const values = connectionValues(value)
  const baseline = JSON.stringify(values)
  const existing = drafts[value.id]
  if (!existing) drafts[value.id] = { values, token: '', baseline }
  else if (JSON.stringify(existing.values) === existing.baseline) { existing.values = values; existing.baseline = baseline }
}, { immediate: true })
watch(accountID, () => { search.value = ''; action.error.value = ''; adding.value = false; removing.value = undefined; deleting.value = false })
watch(() => [route.fullPath, props.agentID, props.platform], () => {
  if (route.path === '/settings/agents' && route.query.agent === props.agentID && route.query.connect === '1' && route.query.platform === props.platform && imAdapters.some(a => a.platform === props.platform)) {
    createAccount()
    const query = { ...route.query }
    delete query.connect
    void router.replace({ path: route.path, query })
  }
}, { immediate: true })

function connectionState(value: AstralInternalHttpapiConnectionView) {
  if (!value.enabled) return 'disabled'
  if (value.status.error) return 'needs_attention'
  return ['polling', 'connected'].includes(value.status.state) ? 'connectionReady' : 'connecting'
}
function accountIdentity(value: Connection) { return value.username ? (value.platform === 'telegram' ? '@' : '') + value.username : adapterLabel(value.adapter, locale.value) }
function chatBinding(id: string) { return bindings.data.value?.find(b => b.conversation_id === id && b.enabled) }
function chatKind(conversation: Conversation) {
  if (conversation.kind === 'topic') return t('topicChat', { id: conversation.topic_id })
  return t(conversation.kind === 'private' ? 'privateChat' : conversation.kind === 'channel' ? 'channelChat' : 'groupChat')
}
function accountRoute(id?: string) { return { path: '/settings/agents', query: { agent: props.agentID, platform: props.platform, ...(id ? { account: id } : {}) } } }
function input(value: Connection): AstralInternalHttpapiConnectionInput {
  return { id: value.id, revision: value.revision, created_at: value.created_at, updated_at: value.updated_at, name: value.name, agent_id: value.agent_id, access_mode: value.access_mode, platform: value.platform, adapter: value.adapter, enabled: value.enabled, account_id: value.account_id, username: value.username, config: value.config }
}
async function latest(id: string) {
  const current = (await request(listConnections())).find(c => c.id === id)
  if (!current) throw { code: 'not_found', message: t('accountMissing') }
  return current
}
async function patchAccount(id: string, field: string, patch: Partial<Connection>, intent?: unknown) {
  return saves.run(`connection:${id}`, field, {
    write: async () => {
      const current = await latest(id)
      if (Object.entries(patch).some(([key, value]) => current[key as keyof Connection] !== value)) saves.confirm(listConnectionsQueryKey(), await request(saveConnection({ body: { ...input(current), ...patch } })))
      await app.refresh().catch(() => undefined)
    },
    reconcile: async () => {
      const current = await latest(id)
      saves.confirm(listConnectionsQueryKey(), current)
      await app.refresh().catch(() => undefined)
      return Object.entries(patch).every(([key, value]) => current[key as keyof Connection] === value) ? 'applied' : 'not-applied'
    },
  }, intent)
}
function connectionValues(value: Connection) {
  const config = object(value.config)
  return Object.fromEntries((adapterInfo(value.adapter)?.fields || []).map(f => [f.key, String(config[f.key] || '')]))
}
async function saveConnectionSettings() {
  const id = accountID.value
  const editing = drafts[id]
  if (!account.value || !editing) return
  const values = Object.fromEntries(Object.entries(editing.values).map(([key, value]) => [key, value.trim()]))
  const token = editing.token.trim()
  const submittedDraft = JSON.stringify(editing.values)
  let credentialID: string | undefined
  const accept = async () => {
    const current = drafts[id]
    if (current) {
      current.baseline = JSON.stringify(values)
      if (JSON.stringify(current.values) === submittedDraft) current.values = { ...values }
      if (current.token.trim() === token) current.token = ''
    }
    await app.refresh().catch(() => undefined)
  }
  await saves.run(`connection:${id}`, 'config', {
    write: async () => {
      const current = await latest(id)
      credentialID = current.credential_id
      await request(saveConnection({ body: { ...input(current), config: { ...object(current.config), ...values }, ...(token ? { token } : {}) } }))
      await accept()
    },
    reconcile: async () => {
      const current = await latest(id)
      const applied = Object.entries(values).every(([key, value]) => String(object(current.config)[key] || '') === value) && (!token || (!!credentialID && current.credential_id !== credentialID))
      if (applied) await accept()
      else await app.refresh().catch(() => undefined)
      return applied ? 'applied' : token && !credentialID ? 'unknown' : 'not-applied'
    },
  })
}
function createAccount() {
  connectionAttempt++; createAction.busy.value = false
  createAction.error.value = ''; login.value = undefined; verifyCode.value = ''
  creating.value = { agentID: props.agentID, platform: props.platform, adapter: imAdapters.find(a => a.platform === props.platform)?.id || '', name: platformName.value, token: '', values: {} }
}
function reconnectWeixin() {
  createAccount()
  if (creating.value && account.value) { creating.value.name = account.value.name; creating.value.connectionID = account.value.id }
}
function currentForm(form: NonNullable<typeof creating.value>) {
  return pageActive.value && creating.value === form && form.agentID === props.agentID && form.platform === props.platform && route.path === '/settings/agents' && route.query.agent === form.agentID && (route.query.platform || (route.query.tab === 'channels' ? 'telegram' : '')) === form.platform
}
async function connected(id: string, form: NonNullable<typeof creating.value>) {
  if (!currentForm(form)) return
  const attempt = connectionAttempt
  creating.value = undefined; login.value = undefined
  await app.refresh().catch(() => undefined)
  if (pageActive.value && attempt === connectionAttempt && route.query.agent === form.agentID && (route.query.platform || (route.query.tab === 'channels' ? 'telegram' : '')) === form.platform) await router.push({ path: '/settings/agents', query: { agent: form.agentID, platform: form.platform, account: id } })
}
async function connectAccount() {
  const form = creating.value
  if (!form || !currentForm(form) || createAction.busy.value) return
  const attempt = ++connectionAttempt
  createAction.busy.value = true; createAction.error.value = ''
  try {
    if (form.adapter === 'weixin.ilink') {
      login.value = undefined; verifyCode.value = ''
      const result = await request(startWeixinLogin({ path: { id: form.agentID }, body: { name: form.name.trim(), connection_id: form.connectionID || undefined } }))
      if (currentForm(form) && attempt === connectionAttempt) login.value = result
      return
    }
    const config = Object.fromEntries(Object.entries(form.values).map(([key, value]) => [key, value.trim()]))
    const result = await request(saveConnection({ body: { ...newMeta(), name: form.name.trim(), agent_id: form.agentID, platform: form.platform, adapter: form.adapter, access_mode: 'specified', enabled: true, config, token: form.token.trim() } }))
    if (attempt === connectionAttempt) await connected(result.id, form)
  } catch (error) {
    if (currentForm(form) && attempt === connectionAttempt) createAction.error.value = errorText(error)
  } finally { if (attempt === connectionAttempt) createAction.busy.value = false }
}

async function pollQR(submitCode = false) {
  if (!login.value || !creating.value || !currentForm(creating.value) || qrPolling.value || submitCode && !/^\d{1,32}$/.test(verifyCode.value)) return
  const pending = login.value; const form = creating.value
  qrPolling.value = true
  try {
    const result = await request(pollWeixinLogin({ path: { id: pending.id }, body: { verify_code: submitCode ? verifyCode.value || undefined : undefined } }))
    if (login.value !== pending || !currentForm(form)) return
    pending.status = result.status
    if (result.status !== 'need_verifycode') verifyCode.value = ''
    createAction.error.value = ''
    if (result.connection) await connected(result.connection.id, form)
  } catch (error) { if (login.value === pending && currentForm(form)) createAction.error.value = errorText(error) }
  finally { qrPolling.value = false }
}

function accessMode(mode: string) {
  if (!account.value || mode === account.value.access_mode) return
  if (mode === 'all') expanding.value = account.value
  else void patchAccount(account.value.id, 'access', { access_mode: 'specified' }, 'specified')
}
async function expandAccess() {
  if (expanding.value && await patchAccount(expanding.value.id, 'access', { access_mode: 'all' }, 'all')) expanding.value = undefined
}
async function removeChat() {
  const target = removing.value
  if (!target) return
  await action.run(async () => {
    const current = await latest(target.connection_id)
    if (current.access_mode !== 'specified') throw new Error(t('accessAllChatsHint'))
    await bindings.refetch()
    const binding = chatBinding(target.id)
    if (binding) await request(unbindConversation({ path: { id: binding.id }, query: { revision: binding.revision } }))
    removing.value = undefined
    await app.refresh()
  })
}
async function setProactive(conversation: Conversation, value: string) {
  const id = conversation.id
  const enabled = value === 'proactive'
  await saves.run(`conversation:${id}`, 'reply', {
    write: async () => {
      const current = (await request(listConversations())).find(c => c.id === id)
      if (!current) throw { code: 'not_found', message: t('noMatchingChats') }
      if (current.proactive_enabled !== enabled) saves.confirm(listConversationsQueryKey(), await request(setConversationProactive({ path: { id }, body: { revision: current.revision, enabled } })))
      await conversations.refetch().catch(() => undefined)
    },
    reconcile: async () => {
      const current = (await request(listConversations())).find(c => c.id === id)
      if (current) saves.confirm(listConversationsQueryKey(), current)
      await conversations.refetch().catch(() => undefined)
      return current?.proactive_enabled === enabled ? 'applied' : 'not-applied'
    },
  }, value)
}
async function removeConnection() {
  const target = account.value
  if (!target) return
  await action.run(async () => {
    const current = await latest(target.id)
    await request(deleteConnection({ path: { id: target.id }, query: { revision: current.revision } }))
    deleting.value = false
    await app.refresh()
    await router.push(accountRoute())
  })
}
</script>
<template>
  <div class="settings-sections account-settings">
    <p
      v-if="list.error.value || conversations.error.value || bindings.error.value"
      class="error-box"
      role="alert"
    >
      {{ errorText(list.error.value || conversations.error.value || bindings.error.value) }}
    </p>
    <template v-if="!account">
      <div class="settings-title">
        <h1>{{ t('platformAccount', { platform: platformName }) }}</h1><Button
          v-if="imAdapters.some(a => a.platform === platform)"
          size="small"
          @click="createAccount"
        >
          <Plus />{{ t('connectPlatformAccount') }}
        </Button>
      </div>
      <p
        v-if="accountID"
        class="error-box"
        role="alert"
      >
        {{ t('accountMissing') }}
      </p>
      <div
        v-if="ownAccounts.length"
        class="resource-list"
      >
        <RouterLink
          v-for="item in ownAccounts"
          :key="item.id"
          :to="accountRoute(item.id)"
          class="resource-row"
        >
          <span class="resource-symbol"><ChannelIcon :platform="item.platform" /></span><span><strong>{{ item.name }}</strong><small>{{ accountIdentity(item) }}</small></span><span
            class="settings-inline-status"
            :class="{ 'danger-text': connectionState(item) === 'needs_attention' }"
          >{{ t(connectionState(item)) }}</span><ChevronRight />
        </RouterLink>
      </div>
      <div
        v-else-if="list.data.value"
        class="empty-state"
      >
        <strong>{{ t('noAccountsOnPlatform', { platform: platformName }) }}</strong><p>{{ t('connectAccountHint') }}</p>
      </div>
      <p
        v-else
        class="settings-loading"
      >
        {{ t('loading') }}
      </p>
    </template>
    <template v-else>
      <RouterLink
        :to="accountRoute()"
        class="provider-back"
      >
        <ArrowLeft />{{ t('allPlatformAccounts', { platform: platformName }) }}
      </RouterLink>
      <div class="settings-detail-heading">
        <span class="resource-symbol"><ChannelIcon :platform="account.platform" /></span><div class="settings-detail-identity">
          <h1>{{ account.name }}</h1><p>{{ accountIdentity(account) }}</p>
        </div><span
          class="settings-inline-status"
          :class="{ 'danger-text': connectionState(account) === 'needs_attention' }"
        >{{ t(connectionState(account)) }}</span>
      </div>
      <SettingsSection :title="t('accountInfo')">
        <SettingsRow :label="t('accountName')">
          <SettingsText
            :value="account.name"
            :draft-key="`${resource}/name`"
            :label="t('accountName')"
            required
            @commit="patchAccount(account!.id, 'name', { name: $event })"
          />
        </SettingsRow>
        <SettingsRow
          :label="t('accountEnabled')"
          :description="t('accountEnabledHintUx')"
        >
          <Switch
            :model-value="saves.value(`${resource}/enabled`, account.enabled)"
            :label="t('accountEnabled')"
            @update:model-value="patchAccount(account!.id, 'enabled', { enabled: $event }, $event)"
          />
        </SettingsRow>
        <p
          v-if="account.status.error"
          class="error-box"
          role="alert"
        >
          {{ account.status.error }}
        </p>
        <p
          v-for="diagnostic in account.diagnostics"
          :key="diagnostic"
          class="settings-context-note"
        >
          {{ diagnostic }}
        </p>
        <SettingsFeedback :scope="[`${resource}/name`, `${resource}/enabled`]" />
      </SettingsSection>
      <form
        v-if="draft && account.adapter !== 'weixin.ilink'"
        class="settings-form"
        @submit.prevent="saveConnectionSettings"
      >
        <SettingsSection
          :title="t('connectionSettings')"
          :description="adapterInfo(account.adapter)?.hint ? t(adapterInfo(account.adapter)!.hint) : undefined"
        >
          <IMConnectionFields
            v-model:token="draft.token"
            :adapter="account.adapter"
            :values="draft.values"
            @field="(key, value) => { draft!.values[key] = value }"
          />
          <div class="settings-submit-row">
            <p class="muted small">
              {{ t('connectionDraftHint') }}
            </p><Button
              type="submit"
              variant="primary"
              :busy="saves.state(`${resource}/config`).status === 'saving'"
              :disabled="!dirty"
            >
              {{ t('saveConnectionSettings') }}
            </Button>
          </div>
          <SettingsFeedback
            :scope="`${resource}/config`"
            :saving="t('connectionChecking')"
            :retry="saveConnectionSettings"
          />
        </SettingsSection>
      </form>
      <SettingsSection
        v-if="account.adapter === 'weixin.ilink'"
        :title="t('connectionSettings')"
      >
        <SettingsRow
          :label="t('weixinLogin')"
          :description="t('weixinPlatformHint')"
        >
          <Button
            size="small"
            @click="reconnectWeixin"
          >
            {{ t('weixinReconnect') }}
          </Button>
        </SettingsRow>
      </SettingsSection>
      <SettingsSection :title="t('chatAccess')">
        <label
          v-for="mode in ['all', 'specified']"
          :key="mode"
          class="settings-choice"
        ><input
          type="radio"
          name="account-access"
          :checked="saves.value(`${resource}/access`, account.access_mode) === mode"
          @change="accessMode(mode)"
        ><span><strong>{{ t(mode === 'all' ? 'accessAllChats' : 'accessSpecifiedChats') }}</strong><small>{{ t(mode === 'all' ? 'accessAllChatsHint' : 'accessSpecifiedChatsHint') }}</small></span></label>
        <SettingsFeedback :scope="`${resource}/access`" />
      </SettingsSection>
      <SettingsSection :title="t('connectedChats')">
        <template #actions>
          <Button
            v-if="account.access_mode === 'specified'"
            size="small"
            @click="rebindID = ''; adding = true"
          >
            <Plus />{{ t('addChat') }}
          </Button>
        </template>
        <div
          v-if="ownConversations.length > 5"
          class="search-input account-chat-search"
        >
          <Search /><input
            v-model="search"
            :placeholder="t('findChat')"
            :aria-label="t('findChat')"
          >
        </div>
        <div
          v-for="conversation in visibleConversations"
          :key="conversation.id"
          class="account-chat-row"
        >
          <RouterLink
            :to="`/sessions/${chatBinding(conversation.id)?.session_id}`"
            class="channel-chat-name"
          >
            <strong>{{ conversation.title }}</strong><span>{{ chatKind(conversation) }}<ChevronRight /></span>
          </RouterLink>
          <div class="account-chat-controls">
            <Select
              v-if="conversation.kind !== 'private'"
              :model-value="saves.value(`conversation:${conversation.id}/reply`, conversation.proactive_enabled ? 'proactive' : 'mentions')"
              :label="t('chatReplyMode', { name: conversation.title })"
              :options="[{ value: 'mentions', label: t('replyWhenMentioned'), description: t('replyWhenMentionedHint') }, { value: 'proactive', label: t('joinDiscussion'), description: t('joinDiscussionHint') }]"
              @update:model-value="setProactive(conversation, $event)"
            />
            <span
              v-else
              class="chat-direct-reply"
            >{{ t('directReply') }}</span>
            <div class="account-chat-actions">
              <Button
                variant="ghost"
                size="small"
                @click="rebindID = conversation.id; adding = true"
              >
                {{ t('changeSession') }}
              </Button><Button
                v-if="account.access_mode === 'specified'"
                variant="ghost"
                size="small"
                class="danger-text"
                @click="action.error.value = ''; removing = conversation"
              >
                {{ t('removeChat') }}
              </Button>
            </div>
          </div>
        </div>
        <p
          v-if="ownConversations.length && !visibleConversations.length"
          class="empty"
        >
          {{ t('noMatchingChats') }}
        </p>
        <div
          v-else-if="!ownConversations.length"
          class="empty-state compact-empty"
        >
          <strong>{{ t('noAccessibleChats') }}</strong><p>{{ t(account.access_mode === 'all' ? 'firstAutomaticChatHint' : 'firstSpecifiedChatHint') }}</p>
        </div>
        <SettingsFeedback
          v-if="ownConversations.length"
          :scope="ownConversations.map(c => `conversation:${c.id}/reply`)"
        />
      </SettingsSection>
      <SettingsSection v-if="!hasHistory">
        <SettingsRow
          :label="t('deleteConnection')"
          :description="t('accountDeleteHint')"
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
      <ConversationPicker
        v-model:open="adding"
        :agent-i-d="agentID"
        :connection-i-d="account.id"
        :conversation-i-d="rebindID"
      />
    </template>
    <Modal
      :open="!!creating"
      :title="t('connectPlatformAccount')"
      @update:open="value => { if (!value) creating = undefined }"
    >
      <form
        v-if="creating"
        class="stack"
        @submit.prevent="connectAccount"
      >
        <p
          v-if="createAction.error.value"
          class="error-box"
          role="alert"
        >
          {{ createAction.error.value }}
        </p>
        <label class="label">{{ t('accountName') }}<input
          v-model="creating.name"
          required
          autofocus
        ></label>
        <label
          v-if="creating.platform === 'qq'"
          class="label"
        >{{ t('connectionMethod') }}<Select
          v-model="creating.adapter"
          :label="t('connectionMethod')"
          :options="imAdapters.filter(a => a.platform === 'qq').map(a => ({ value: a.id, label: adapterLabel(a.id, locale) }))"
          @update:model-value="creating.values = {}; creating.token = ''"
        /></label>
        <IMConnectionFields
          v-if="creating.adapter !== 'weixin.ilink'"
          v-model:token="creating.token"
          :adapter="creating.adapter"
          :values="creating.values"
          creating
          @field="(key, value) => { creating!.values[key] = value }"
        />
        <template v-else>
          <p class="muted small">
            {{ t('weixinPlatformHint') }}
          </p>
          <template v-if="login">
            <p
              class="muted"
              role="status"
            >
              {{ t('weixinStatus_' + login.status) }}
            </p>
            <a
              v-if="qrLink"
              :href="qrLink"
              target="_blank"
              rel="noopener noreferrer"
            >{{ t('weixinOpenQR') }}</a>
            <img
              v-if="qrImage"
              :src="qrImage"
              :alt="t('weixinLogin')"
              width="220"
              height="220"
            >
            <label
              v-if="login.status === 'need_verifycode'"
              class="label"
            >{{ t('weixinVerifyCode') }}<input
              v-model="verifyCode"
              inputmode="numeric"
              autocomplete="one-time-code"
              @keydown.enter.prevent="pollQR(true)"
            ></label>
            <Button
              v-if="login.status === 'need_verifycode'"
              :busy="qrPolling"
              :disabled="!/^\d{1,32}$/.test(verifyCode)"
              @click="pollQR(true)"
            >
              {{ t('weixinVerifyAction') }}
            </Button>
          </template>
        </template>
        <div class="form-footer">
          <Button
            variant="ghost"
            @click="creating = undefined"
          >
            {{ t('cancel') }}
          </Button><Button
            type="submit"
            variant="primary"
            :busy="createAction.busy.value"
          >
            {{ t(creating.adapter === 'weixin.ilink' ? login ? 'weixinRefreshQR' : 'weixinStartQR' : 'connectAccountAction') }}
          </Button>
        </div>
      </form>
    </Modal>
    <Modal
      :open="!!expanding"
      :title="t('expandChatAccess')"
      @update:open="value => { if (!value) expanding = undefined }"
    >
      <p class="muted">
        {{ t('expandChatAccessHint') }}
      </p>
      <SettingsFeedback
        v-if="expanding"
        :scope="`connection:${expanding.id}/access`"
      />
      <div class="form-footer">
        <Button
          variant="ghost"
          @click="expanding = undefined"
        >
          {{ t('cancel') }}
        </Button><Button
          variant="primary"
          :busy="!!expanding && saves.state(`connection:${expanding.id}/access`).status === 'saving'"
          @click="expandAccess"
        >
          {{ t('allowAllChatsAction') }}
        </Button>
      </div>
    </Modal>
    <Modal
      :open="!!removing"
      :title="t('removeChatTitle', { name: removing?.title })"
      @update:open="value => { if (!value) removing = undefined }"
    >
      <p class="muted">
        {{ t('removeSpecifiedChatHint') }}
      </p><p
        v-if="action.error.value"
        class="error-box"
        role="alert"
      >
        {{ action.error.value }}
      </p>
      <div class="form-footer">
        <Button
          variant="ghost"
          @click="removing = undefined"
        >
          {{ t('cancel') }}
        </Button><Button
          variant="danger"
          :busy="action.busy.value"
          @click="removeChat"
        >
          {{ t('removeChat') }}
        </Button>
      </div>
    </Modal>
    <Modal
      v-model:open="deleting"
      :title="t('deleteConnection')"
    >
      <p class="muted">
        {{ t('deleteConnectionHint') }}
      </p><p
        v-if="action.error.value"
        class="error-box"
        role="alert"
      >
        {{ action.error.value }}
      </p>
      <div class="form-footer">
        <Button
          variant="ghost"
          @click="deleting = false"
        >
          {{ t('cancel') }}
        </Button><Button
          variant="danger"
          :busy="action.busy.value"
          @click="removeConnection"
        >
          {{ t('delete') }}
        </Button>
      </div>
    </Modal>
  </div>
</template>

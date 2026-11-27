<script setup lang="ts">
import { computed, nextTick, onActivated, onBeforeUnmount, onDeactivated, onMounted, ref, useId } from 'vue'
import { useI18n } from 'vue-i18n'
import { createAcpToken, createBotApiToken, listAcpTokens, listBotApiTokens, revokeAcpToken, revokeBotApiToken, type ListAcpTokensResponse } from '@astral/sdk'
import { Button, ConfirmPopover, Modal, Select, SettingsRow, SettingsSection, useDelayedPending } from '@astral/ui'
import { errorText, request } from '../../lib/api'
import { copyToClipboard } from '../../lib/browser'
import { formatDateTime } from '../../lib/timezone'
import { useAction } from '../../composables/useAction'
import { useClipboard } from '../../composables/useClipboard'
import { useAppStore } from '../../stores/app'

const props = defineProps<{ agentId: string }>()
const { t, locale } = useI18n()
const app = useAppStore()
type Purpose = 'botApi' | 'acp'
type TokenDirectory = ListAcpTokensResponse
type AccessToken = TokenDirectory['tokens'][number] & { purpose: Purpose }
const purposes: Purpose[] = ['botApi', 'acp']
const purposeLabels = { botApi: 'Bot API', acp: 'ACP' }
const guideSlugs = { botApi: 'bot-api', acp: 'acp' }
const guideBase = computed(() => `https://astral.oines.dev/${locale.value.startsWith('zh') ? 'zh-cn/' : ''}docs/hosting/`)
const tokenAPI = {
  botApi: { list: listBotApiTokens, create: createBotApiToken, revoke: revokeBotApiToken },
  acp: { list: listAcpTokens, create: createAcpToken, revoke: revokeAcpToken },
}
const directories = ref<Partial<Record<Purpose, TokenDirectory>>>({})
const tokens = computed(() => purposes.flatMap(purpose => (directories.value[purpose]?.tokens || []).map(token => ({ ...token, purpose })))
  .sort((left, right) => Date.parse(right.created_at) - Date.parse(left.created_at)))
const hasDirectory = computed(() => purposes.some(purpose => directories.value[purpose]))
const purposeOptions = computed(() => purposes.map(purpose => ({
  value: purpose, label: purposeLabels[purpose], disabled: !directories.value[purpose],
})))
const loading = ref(false)
const loadErrors = ref<Partial<Record<Purpose, string>>>({})
const loadPending = useDelayedPending(loading)
const endpointCopies = { botApi: useClipboard(), acp: useClipboard() }
let loadRequest: AbortController | undefined
const directoryRevisions = { botApi: 0, acp: 0 }

const creating = ref(false)
const name = ref('')
const purpose = ref<Purpose | ''>('')
const permissionsId = useId()
const createdToken = ref('')
const tokenCopied = ref(false)
const copyError = ref(false)
const copyButton = ref<InstanceType<typeof Button>>()
const createAction = useAction()
let creationRound = 0

const revoking = ref('')
const revokeAction = useAction()
const tokenKey = (token: AccessToken) => `${token.purpose}:${token.id}`

async function load() {
  loadRequest?.abort()
  const controller = new AbortController()
  loadRequest = controller
  loading.value = true
  loadErrors.value = {}
  try {
    const loaded: Partial<Record<Purpose, TokenDirectory>> = {}
    await Promise.all(purposes.map(async purpose => {
      const revision = directoryRevisions[purpose]
      try {
        const directory = await request<TokenDirectory>(tokenAPI[purpose].list({ path: { id: props.agentId }, signal: controller.signal }))
        if (controller.signal.aborted || directoryRevisions[purpose] !== revision) return
        loaded[purpose] = directory
        directories.value[purpose] = directory
      } catch (error) {
        if (!controller.signal.aborted && directoryRevisions[purpose] === revision) loadErrors.value[purpose] = errorText(error)
      }
    }))
    return controller.signal.aborted ? undefined : loaded
  } finally {
    if (loadRequest === controller) {
      loadRequest = undefined
      loading.value = false
    }
  }
}

function closeCreation() {
  creationRound++
  creating.value = false
  name.value = ''
  purpose.value = ''
  createdToken.value = ''
  tokenCopied.value = false
  copyError.value = false
  createAction.error.value = ''
}
function openCreation() {
  createAction.error.value = ''
  creating.value = true
}
function deactivate() {
  loadRequest?.abort()
  loadRequest = undefined
  loading.value = false
  closeCreation()
  revoking.value = ''
}
function loadOnOpen() { if (!loadRequest) void load() }
onMounted(loadOnOpen)
onActivated(loadOnOpen)
onDeactivated(deactivate)
onBeforeUnmount(deactivate)

function selectPurpose(value: string) {
  purpose.value = value === 'botApi' || value === 'acp' ? value : ''
}

function copyEndpoint(kind: Purpose) {
  const address = directories.value[kind]?.endpoint
  if (address) void endpointCopies[kind].copy(address)
}

async function create() {
  const label = name.value.trim()
  const selected = purpose.value
  if (!label || !selected || !directories.value[selected] || createAction.busy.value) return
  const round = creationRound
  await createAction.run(async () => {
    const { token, id, name, created_at, last_used_at } = await request(tokenAPI[selected].create({ path: { id: props.agentId }, body: { name: label } }))
    directoryRevisions[selected]++
    const directory = directories.value[selected]
    if (directory) directory.tokens = [{ id, name, created_at, last_used_at }, ...directory.tokens.filter(current => current.id !== id)]
    if (round !== creationRound) return
    createdToken.value = token
    await nextTick()
    copyButton.value?.$el.focus()
  })
  if (createAction.error.value && round === creationRound) void load()
}

async function copyToken() {
  const round = creationRound
  const success = await copyToClipboard(createdToken.value)
  if (round !== creationRound) return
  tokenCopied.value = success
  copyError.value = !success
}

function confirmRevocation(token: AccessToken, open: boolean) {
  if (revokeAction.busy.value) return
  revoking.value = open ? tokenKey(token) : ''
  revokeAction.error.value = ''
}
async function revoke({ id, purpose }: AccessToken) {
  if (revokeAction.busy.value) return
  await revokeAction.run(async () => {
    try {
      await request(tokenAPI[purpose].revoke({ path: { id: props.agentId, token_id: id } }))
      directoryRevisions[purpose]++
      const directory = directories.value[purpose]
      if (directory) directory.tokens = directory.tokens.filter(token => token.id !== id)
    } catch (error) {
      const latest = await load()
      if (!latest?.[purpose] || latest[purpose].tokens.some(token => token.id === id)) throw error
    }
    revoking.value = ''
  })
}
</script>

<template>
  <SettingsSection
    :title="t('externalAccess.title')"
    class="auth-list-section external-access"
  >
    <template
      v-for="kind in purposes"
      :key="kind"
    >
      <SettingsRow
        v-if="directories[kind]?.endpoint"
        :label="t(`externalAccess.address.${kind}`)"
        :description="directories[kind]?.endpoint"
        class="access-address"
      >
        <a
          :href="`${guideBase}${guideSlugs[kind]}/`"
          :aria-label="t('externalAccess.docsLabel', { purpose: purposeLabels[kind] })"
          target="_blank"
          rel="noopener noreferrer"
          class="settings-inline-link"
        >{{ t('externalAccess.docs') }}</a>
        <Button
          size="small"
          :aria-label="t('externalAccess.copyEndpoint', { purpose: purposeLabels[kind] })"
          @click="copyEndpoint(kind)"
        >
          {{ t(endpointCopies[kind].copied.value ? 'copied' : 'copy') }}
        </Button>
      </SettingsRow>
    </template>
    <template v-if="hasDirectory">
      <SettingsRow :label="t('externalAccess.tokens')">
        <Button
          size="small"
          :disabled="createAction.busy.value"
          @click="openCreation"
        >
          {{ t('externalAccess.create') }}
        </Button>
      </SettingsRow>
      <div
        v-for="token in tokens"
        :key="tokenKey(token)"
        class="resource-row"
      >
        <div class="auth-device-copy">
          <strong>{{ token.name }}</strong><small>{{ purposeLabels[token.purpose] }} · {{ token.last_used_at && !token.last_used_at.startsWith('0001-') ? t('externalAccess.lastUsed', { time: formatDateTime(token.last_used_at, locale, app.displayTimezone) }) : t('externalAccess.neverUsed') }}</small>
        </div>
        <ConfirmPopover
          :open="revoking === tokenKey(token)"
          :title="t('externalAccess.revokeTitle', { name: token.name, purpose: purposeLabels[token.purpose] })"
          :description="t(`externalAccess.revokeHint.${token.purpose}`)"
          :confirm-label="t('externalAccess.revoke')"
          :cancel-label="t('cancel')"
          :busy="revokeAction.busy.value"
          :error="revoking === tokenKey(token) ? revokeAction.error.value : undefined"
          @update:open="confirmRevocation(token, $event)"
          @confirm="revoke(token)"
        >
          <template #trigger>
            <Button
              size="small"
              variant="ghost"
              :aria-label="t('externalAccess.revokeTitle', { name: token.name, purpose: purposeLabels[token.purpose] })"
              :disabled="revokeAction.busy.value"
            >
              {{ t('externalAccess.revoke') }}
            </Button>
          </template>
        </ConfirmPopover>
      </div>
    </template>
    <div
      v-if="loadPending"
      class="settings-feedback"
      role="status"
    >
      <span
        class="spinner"
        aria-hidden="true"
      /><span class="sr-only">{{ t('loading') }}</span>
    </div>
    <div
      v-if="Object.keys(loadErrors).length"
      class="settings-feedback settings-feedback-error auth-group-error"
      role="alert"
    >
      <div>
        <div
          v-for="(error, kind) in loadErrors"
          :key="kind"
        >
          {{ t('externalAccess.loadFailed', { purpose: purposeLabels[kind], error }) }}
        </div>
      </div>
      <button
        type="button"
        class="text-button"
        @click="load"
      >
        {{ t('retry') }}
      </button>
    </div>
  </SettingsSection>

  <Modal
    :open="creating"
    :title="t(createdToken ? 'externalAccess.created' : 'externalAccess.create')"
    :description="t(createdToken ? 'externalAccess.copyOnce' : 'externalAccess.trustedClient')"
    @update:open="open => { if (!open && !createAction.busy.value) closeCreation() }"
  >
    <div
      v-if="createdToken"
      class="stack"
    >
      <code class="auth-secret">{{ createdToken }}</code>
      <p
        v-if="copyError"
        class="error-box"
        role="alert"
      >
        {{ t('externalAccess.copyManually') }}
      </p>
      <div class="form-footer">
        <Button
          variant="ghost"
          @click="closeCreation"
        >
          {{ t('close') }}
        </Button>
        <Button
          ref="copyButton"
          variant="primary"
          @click="copyToken"
        >
          {{ t(tokenCopied ? 'copied' : 'externalAccess.copyToken') }}
        </Button>
      </div>
    </div>
    <form
      v-else
      class="stack"
      @submit.prevent="create"
    >
      <div class="label">
        {{ t('externalAccess.purpose') }}
        <Select
          :model-value="purpose"
          :options="purposeOptions"
          :placeholder="t('externalAccess.choosePurpose')"
          :label="t('externalAccess.purpose')"
          :aria-describedby="purpose ? permissionsId : undefined"
          :disabled="createAction.busy.value"
          required
          @update:model-value="selectPurpose"
        />
        <small
          v-if="purpose"
          :id="permissionsId"
          class="muted"
        >{{ t(`externalAccess.permissions.${purpose}`) }}</small>
      </div>
      <label class="label">{{ t('externalAccess.tokenName') }}<input
        v-model="name"
        autocomplete="off"
        :disabled="createAction.busy.value"
        required
      ></label>
      <p
        v-if="createAction.error.value"
        class="error-box"
        role="alert"
      >
        {{ createAction.error.value }}
      </p>
      <div class="form-footer">
        <Button
          variant="ghost"
          :disabled="createAction.busy.value"
          @click="closeCreation"
        >
          {{ t('cancel') }}
        </Button>
        <Button
          type="submit"
          variant="primary"
          :disabled="!purpose || !name.trim() || createAction.busy.value"
          :busy="createAction.busy.value"
        >
          {{ t('externalAccess.create') }}
        </Button>
      </div>
    </form>
  </Modal>
</template>

<style scoped>
.external-access :deep(.preference-copy p),
.external-access .auth-device-copy strong { overflow-wrap: anywhere; }
.access-address :deep(.preference-control) { flex-wrap: wrap; row-gap: 4px; }
.external-access :deep(.preference-row + .resource-row) {
  position: relative;
}
.external-access :deep(.preference-row + .resource-row)::before {
  content: '';
  position: absolute;
  inset: 0 var(--settings-group-padding) auto;
  height: 1px;
  background: var(--panel-line);
  pointer-events: none;
}
</style>

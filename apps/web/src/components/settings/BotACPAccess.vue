<script setup lang="ts">
import { nextTick, onActivated, onBeforeUnmount, onDeactivated, onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { createAcpToken, listAcpTokens, revokeAcpToken, type ListAcpTokensResponse } from '@astral/sdk'
import { Button, ConfirmPopover, Modal, SettingsRow, SettingsSection, useDelayedPending } from '@astral/ui'
import { errorText, request } from '../../lib/api'
import { copyToClipboard } from '../../lib/browser'
import { formatDateTime } from '../../lib/timezone'
import { useAction } from '../../composables/useAction'
import { useClipboard } from '../../composables/useClipboard'
import { useAppStore } from '../../stores/app'

const props = defineProps<{ agentId: string }>()
const { t, locale } = useI18n()
const app = useAppStore()
const directory = ref<ListAcpTokensResponse>()
const loading = ref(false)
const loadError = ref('')
const loadPending = useDelayedPending(loading)
const endpointCopy = useClipboard()
let loadRequest: AbortController | undefined

const creating = ref(false)
const name = ref('')
const createdToken = ref('')
const tokenCopied = ref(false)
const copyError = ref(false)
const copyButton = ref<InstanceType<typeof Button>>()
const createAction = useAction()
let creationRound = 0

const revoking = ref('')
const revokeAction = useAction()

async function load() {
  loadRequest?.abort()
  const controller = new AbortController()
  loadRequest = controller
  loading.value = true
  loadError.value = ''
  try {
    const result = await request(listAcpTokens({ path: { id: props.agentId }, signal: controller.signal }))
    if (controller.signal.aborted) return
    directory.value = result
    return result
  } catch (error) {
    if (!controller.signal.aborted) loadError.value = errorText(error)
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
  createdToken.value = ''
  tokenCopied.value = false
  copyError.value = false
  createAction.error.value = ''
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

async function create() {
  const label = name.value.trim()
  if (!label || createAction.busy.value) return
  const round = creationRound
  await createAction.run(async () => {
    const { token, id, name, created_at, last_used_at } = await request(createAcpToken({ path: { id: props.agentId }, body: { name: label } }))
    loadRequest?.abort()
    if (directory.value) directory.value.tokens = [{ id, name, created_at, last_used_at }, ...directory.value.tokens.filter(current => current.id !== id)]
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

function confirmRevocation(id: string, open: boolean) {
  if (revokeAction.busy.value) return
  revoking.value = open ? id : ''
  revokeAction.error.value = ''
}
async function revoke(id: string) {
  if (revokeAction.busy.value) return
  await revokeAction.run(async () => {
    try {
      await request(revokeAcpToken({ path: { id: props.agentId, token_id: id } }))
      loadRequest?.abort()
      if (directory.value) directory.value.tokens = directory.value.tokens.filter(token => token.id !== id)
    } catch (error) {
      const latest = await load()
      if (!latest || latest.tokens.some(token => token.id === id)) throw error
    }
    revoking.value = ''
  })
}
</script>

<template>
  <SettingsSection
    :title="t('acpAccess.title')"
    class="auth-list-section acp-access"
  >
    <template v-if="directory">
      <SettingsRow
        v-if="directory.endpoint"
        :label="t('acpAccess.endpoint')"
        :description="directory.endpoint"
      >
        <Button
          size="small"
          :aria-label="t('acpAccess.copyEndpoint')"
          @click="endpointCopy.copy(directory.endpoint)"
        >
          {{ t(endpointCopy.copied.value ? 'copied' : 'copy') }}
        </Button>
      </SettingsRow>
      <div
        v-for="token in directory.tokens"
        :key="token.id"
        class="resource-row"
      >
        <div class="auth-device-copy">
          <strong>{{ token.name }}</strong><small>{{ token.last_used_at && !token.last_used_at.startsWith('0001-') ? t('acpAccess.lastUsed', { time: formatDateTime(token.last_used_at, locale, app.displayTimezone) }) : t('acpAccess.neverUsed') }}</small>
        </div>
        <ConfirmPopover
          :open="revoking === token.id"
          :title="t('acpAccess.revokeTitle', { name: token.name })"
          :description="t('acpAccess.revokeHint')"
          :confirm-label="t('acpAccess.revoke')"
          :cancel-label="t('cancel')"
          :busy="revokeAction.busy.value"
          :error="revoking === token.id ? revokeAction.error.value : undefined"
          @update:open="confirmRevocation(token.id, $event)"
          @confirm="revoke(token.id)"
        >
          <template #trigger>
            <Button
              size="small"
              variant="ghost"
              :disabled="revokeAction.busy.value"
            >
              {{ t('acpAccess.revoke') }}
            </Button>
          </template>
        </ConfirmPopover>
      </div>
      <SettingsRow :label="t('acpAccess.tokens')">
        <Button
          size="small"
          :disabled="createAction.busy.value"
          @click="creating = true"
        >
          {{ t('acpAccess.create') }}
        </Button>
      </SettingsRow>
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
      v-if="loadError"
      class="settings-feedback settings-feedback-error"
      role="alert"
    >
      <span>{{ loadError }}</span><button
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
    :title="t(createdToken ? 'acpAccess.created' : 'acpAccess.create')"
    :description="t(createdToken ? 'acpAccess.copyOnce' : 'acpAccess.nameHint')"
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
        {{ t('acpAccess.copyManually') }}
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
          {{ t(tokenCopied ? 'copied' : 'acpAccess.copyToken') }}
        </Button>
      </div>
    </div>
    <form
      v-else
      class="stack"
      @submit.prevent="create"
    >
      <label class="label">{{ t('acpAccess.tokenName') }}<input
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
          :disabled="!name.trim() || createAction.busy.value"
          :busy="createAction.busy.value"
        >
          {{ t('acpAccess.create') }}
        </Button>
      </div>
    </form>
  </Modal>
</template>

<style scoped>
.acp-access :deep(.preference-copy p) { overflow-wrap: anywhere; }
</style>

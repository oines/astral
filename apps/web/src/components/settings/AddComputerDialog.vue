<script setup lang="ts">
import { computed, ref, useId, watch } from 'vue'
import { useQuery } from '@pinia/colada'
import { useIntervalFn } from '@vueuse/core'
import { listComputersQuery } from '@astral/sdk/colada'
import { createLocalContainer, enrollComputer, getComputerOperation, getEnrollment, useLocalComputer, type AstralInternalDomainProvisioning, type AstralInternalDomainEnrollment, type AstralInternalComputersEnrollmentResult } from '@astral/sdk'
import { Button, Modal, SettingsRow, SettingsSection } from '@astral/ui'
import { ArrowLeft, Check, ChevronRight, Copy, Monitor, Server, Terminal } from '@astral/icons'
import { useI18n } from 'vue-i18n'
import type { Computer } from '../../lib/types'
import { errorText, request } from '../../lib/api'
import { computerSummary } from '../../lib/computer-info'
import { useAction } from '../../composables/useAction'
import { useAppStore } from '../../stores/app'
import { useClipboard } from '../../composables/useClipboard'
import { randomID } from '../../lib/browser'
import { formatDateTime } from '../../lib/timezone'
defineProps<{ confirmLabel?: string }>()
const adding = defineModel<boolean>('open', { default: false })
const emit = defineEmits<{ connected: [computer: Computer] }>()
const { t, locale } = useI18n()
const app = useAppStore()
const action = useAction()
const { copy, copied } = useClipboard()
const list = useQuery(listComputersQuery())
const mode = ref<'choose' | 'local' | 'container' | 'remote'>('choose')
const container = ref(false)
const commandPlatform = ref<'shell' | 'powershell'>('shell')
const operation = ref<AstralInternalDomainProvisioning>()
const enrollment = ref<AstralInternalDomainEnrollment>()
const enrollmentResult = ref<AstralInternalComputersEnrollmentResult>()
const computerID = ref('')
const connected = computed(() => list.data.value?.find(c => c.id === computerID.value && c.online))
const intent = ref('')
const serverAddress = ref('')
const addressListID = useId()
const displayedCommand = computed(() => commandPlatform.value === 'powershell' ? enrollmentResult.value?.powershell_command || '' : enrollmentResult.value?.command || '')
const activeOperationKey = 'astral:pending-container'
let restoring = false
let interacted = false
let remoteGeneration = 0
const pending = localStorage.getItem(activeOperationKey)
if (pending) {
  try {
    const saved = JSON.parse(pending) as { id: string; intent: string }
    void request(getComputerOperation({ path: { id: saved.id } })).then(value => {
      if (interacted) return
      operation.value = value
      intent.value = saved.intent
      computerID.value = value.computer_id || ''
      if (value.state !== 'connected') { mode.value = 'container'; restoring = true; adding.value = true }
      else localStorage.removeItem(activeOperationKey)
    }).catch(() => localStorage.removeItem(activeOperationKey))
  } catch { localStorage.removeItem(activeOperationKey) }
}
function add() {
  remoteGeneration++
  interacted = true
  mode.value = 'choose'
  operation.value = undefined
  enrollment.value = undefined
  enrollmentResult.value = undefined
  computerID.value = ''
  copied.value = false
  serverAddress.value = app.data?.network.public_urls.value[0] || ''
  commandPlatform.value = app.data?.host.os === 'windows' ? 'powershell' : 'shell'
  intent.value = randomID()
  action.error.value = ''
}
function chooseMode(value: typeof mode.value) {
  interacted = true
  if (mode.value !== value) remoteGeneration++
  mode.value = value
  computerID.value = value === 'container' ? operation.value?.computer_id || '' : value === 'remote' ? enrollment.value?.computer_id || '' : ''
  action.error.value = ''
}
async function local() {
  if (action.busy.value) return
  chooseMode('local')
  await action.run(async () => {
    const c = await request(useLocalComputer())
    computerID.value = c.id
    await list.refetch()
    await app.refresh()
  })
}
async function localContainer() {
  if (action.busy.value) return
  chooseMode('container')
  await action.run(async () => {
    operation.value = await request(createLocalContainer({ body: { id: intent.value } }))
    computerID.value = operation.value.computer_id || ''
    localStorage.setItem(activeOperationKey, JSON.stringify({ id: operation.value.id, intent: intent.value }))
  })
}
async function remote() {
  if (action.busy.value) return
  chooseMode('remote')
  copied.value = false
  const generation = ++remoteGeneration
  const serverURL = serverAddress.value.trim()
  const useContainer = container.value
  const current = () => generation === remoteGeneration && adding.value && mode.value === 'remote'
    && serverAddress.value.trim() === serverURL && container.value === useContainer
  await action.run(async () => {
    try {
      const result = await request(enrollComputer({ body: { container: useContainer, server_url: serverURL } }))
      if (!current()) return
      enrollmentResult.value = result
      enrollment.value = result.enrollment
    } catch (error) { if (current()) throw error }
  })
}
let polling = false
useIntervalFn(async () => {
  if (polling || document.visibilityState === 'hidden') return
  polling = true
  const currentMode = mode.value
  try {
    if (!adding.value) return
    await list.refetch()
    if (mode.value !== currentMode || !adding.value) return
    if (currentMode === 'container' && operation.value && !['connected', 'failed'].includes(operation.value.state)) {
      const id = operation.value.id
      const value = await request(getComputerOperation({ path: { id } }))
      if (mode.value !== currentMode || operation.value?.id !== id) return
      operation.value = value
      if (value.computer_id) computerID.value = value.computer_id
      if (value.state === 'connected') { localStorage.removeItem(activeOperationKey); await app.refresh() }
    }
    if (currentMode === 'remote' && enrollment.value?.state === 'waiting') {
      const id = enrollment.value.id
      const value = await request(getEnrollment({ path: { id } }))
      if (mode.value !== currentMode || enrollment.value?.id !== id) return
      enrollment.value = value
      if (value.computer_id) { computerID.value = value.computer_id; await app.refresh() }
    }
  } catch (e) { if (adding.value && mode.value === currentMode) action.error.value = errorText(e) }
  finally { polling = false }
}, 3000)
async function copyCommand() {
  if (!enrollmentResult.value) return
  await copy(displayedCommand.value)
}
watch(commandPlatform, () => { copied.value = false })
watch([serverAddress, container], () => {
  remoteGeneration++
  enrollmentResult.value = undefined
  enrollment.value = undefined
  computerID.value = ''
  copied.value = false
})
watch(adding, open => {
  if (!open) remoteGeneration++
  if (restoring) { restoring = false; return }
  const provisioning = operation.value && !['connected', 'failed'].includes(operation.value.state)
  if (open && !action.busy.value && !provisioning && enrollment.value?.state !== 'waiting') add()
})
function finish() {
  if (!connected.value) return
  const computer = connected.value
  adding.value = false
  emit('connected', computer)
}
</script>
<template>
  <Modal
    v-model:open="adding"
    :title="t('newComputer')"
  >
    <p
      v-if="action.error.value"
      class="error-box"
    >
      {{ action.error.value }}
    </p>
    <template v-if="mode === 'choose'">
      <p class="muted">
        {{ t('serverMachine', { name: app.data?.host.hostname }) }}
      </p>
      <button
        class="computer-option"
        @click="local"
      >
        <Monitor /><span><strong>{{ t('useServerMachine') }}</strong><small>{{ t('serverMachineHint') }}</small></span><ChevronRight />
      </button>
      <button
        class="computer-option"
        @click="localContainer"
      >
        <Server /><span><strong>{{ t('localContainer') }}</strong><small>{{ t('containerMachineHint') }}</small></span><ChevronRight />
      </button>
      <button
        class="computer-option"
        @click="chooseMode('remote')"
      >
        <Terminal /><span><strong>{{ t('remote') }}</strong><small>{{ t('remoteMachineHint') }}</small></span><ChevronRight />
      </button>
    </template>
    <template v-else-if="!connected">
      <button
        type="button"
        class="provider-back"
        :disabled="action.busy.value"
        @click="chooseMode('choose')"
      >
        <ArrowLeft />{{ t('backToComputerOptions') }}
      </button>
      <div
        v-if="mode === 'remote'"
        class="stack"
      >
        <SettingsSection>
          <SettingsRow
            :label="t('computerCommand.serverAddress')"
            :description="t('computerCommand.serverAddressHint')"
          >
            <input
              v-model="serverAddress"
              type="url"
              :list="addressListID"
              :aria-label="t('computerCommand.serverAddress')"
              :disabled="action.busy.value"
              autocomplete="url"
              spellcheck="false"
              required
            >
            <datalist :id="addressListID">
              <option
                v-for="address in app.data?.network.addresses"
                :key="address.url"
                :value="address.url"
              >
                {{ address.label }}
              </option>
            </datalist>
          </SettingsRow>
          <SettingsRow :label="t(container ? 'computerCommand.hostPlatform' : 'computerCommand.targetPlatform')">
            <select
              v-model="commandPlatform"
              :aria-label="t(container ? 'computerCommand.hostPlatform' : 'computerCommand.targetPlatform')"
            >
              <option value="shell">
                {{ t('computerCommand.shell') }}
              </option>
              <option value="powershell">
                {{ t('computerCommand.powershell') }}
              </option>
            </select>
          </SettingsRow>
        </SettingsSection>
        <template v-if="!enrollmentResult">
          <p class="muted">
            {{ t('commandHint') }}
          </p><label class="row"><input
            v-model="container"
            type="checkbox"
            :disabled="action.busy.value"
          >{{ t('useContainer') }}</label><Button
            variant="primary"
            :busy="action.busy.value"
            :disabled="!serverAddress.trim()"
            @click="remote"
          >
            {{ t('generateCommand') }}
          </Button>
        </template>
        <template v-else>
          <pre class="connection-command">{{ displayedCommand }}</pre><Button
            :busy="action.busy.value"
            :disabled="!displayedCommand"
            @click="copyCommand"
          >
            <Check v-if="copied" /><Copy v-else />{{ t('copy') }}
          </Button>
          <p
            v-if="enrollment?.state === 'waiting'"
            class="row muted"
          >
            <span class="spinner" />{{ t('waitingComputer') }}
          </p><template v-else-if="enrollment?.state === 'expired'">
            <p class="muted">
              {{ t('enrollmentExpired') }}
            </p><Button
              :busy="action.busy.value"
              @click="remote"
            >
              {{ t('generateCommand') }}
            </Button>
          </template><p class="muted small">
            {{ t('expires') }}: {{ formatDateTime(enrollmentResult.enrollment.expires_at, locale, app.displayTimezone) }}
          </p>
        </template>
      </div>
      <div
        v-else-if="mode === 'container'"
        class="stack"
      >
        <p class="row">
          <span
            v-if="operation && operation.state !== 'failed'"
            class="spinner"
          />{{ operation?.phase ? t(`containerPhase.${operation.phase}`) : t('loading') }}
        </p>
        <pre
          v-if="operation?.log?.length"
          class="connection-command"
        >{{ operation.log.join('\n') }}</pre><p
          v-if="operation?.error"
          class="error-box"
        >
          {{ operation.error }}
        </p>
        <Button
          v-if="operation?.state === 'failed' || action.error.value"
          :busy="action.busy.value"
          @click="localContainer"
        >
          {{ t('retry') }}
        </Button>
      </div>
      <div
        v-else
        class="row"
      >
        <span
          v-if="action.busy.value"
          class="spinner"
        /><span v-if="!action.error.value">{{ t('waitingComputer') }}</span><Button
          v-if="action.error.value"
          @click="local"
        >
          {{ t('retry') }}
        </Button>
      </div>
    </template>
    <div
      v-else
      class="stack"
    >
      <span class="badge good align-start"><Check />{{ t('online') }}</span><h2>{{ connected.name }}</h2><p class="muted">
        {{ computerSummary(connected, t) }}
      </p><Button
        variant="primary"
        @click="finish"
      >
        {{ confirmLabel || t('chooseBotAccess') }}
      </Button>
    </div>
  </Modal>
</template>

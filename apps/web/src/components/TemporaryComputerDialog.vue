<script setup lang="ts">
import { computed, ref, useId, watch } from 'vue'
import { useIntervalFn } from '@vueuse/core'
import { enrollComputer, getEnrollment, type AstralInternalComputersEnrollmentResult, type AstralInternalDomainEnrollment } from '@astral/sdk'
import { Button, Modal, SettingsRow, SettingsSection } from '@astral/ui'
import { Check, Copy } from '@astral/icons'
import { useI18n } from 'vue-i18n'
import type { Computer } from '../lib/types'
import { errorText, request } from '../lib/api'
import { applyDownloadSource, useDownloadSource } from '../lib/download-source'
import { computerServerAddresses } from '../lib/network-access'
import { formatDateTime } from '../lib/timezone'
import { useAction } from '../composables/useAction'
import { useClipboard } from '../composables/useClipboard'
import { useAppStore } from '../stores/app'
import DownloadSourceRow from './settings/DownloadSourceRow.vue'

const props = defineProps<{
  sessionId: string
  ensureSession: () => Promise<string>
}>()
const open = defineModel<boolean>('open', { default: false })
const emit = defineEmits<{ connected: [computer: Computer] }>()

const { t, locale } = useI18n()
const app = useAppStore()
const action = useAction()
const { copy, copied } = useClipboard()
const downloadSource = useDownloadSource()

const TTL_OPTIONS = [2, 4, 8] as const
const ttlHours = ref<number>(4)
const commandPlatform = ref<'shell' | 'powershell'>('shell')
const useDirectBinary = ref(false)
const serverAddress = ref('')
const addressListID = useId()
const enrollment = ref<AstralInternalDomainEnrollment>()
const enrollmentResult = ref<AstralInternalComputersEnrollmentResult>()
const computerID = ref('')

const serverAddresses = computed(() => computerServerAddresses(
  app.data?.network.addresses || [],
  window.location.origin,
  Boolean(window.astralDesktop),
  t('networkBrowserAddress'),
))

const connected = computed(() => app.computers.find(c => c.id === computerID.value && c.online && !c.revoked))

const hasDirectCommand = computed(() => Boolean(enrollmentResult.value?.direct_command || enrollmentResult.value?.direct_powershell_command))

const displayedCommand = computed(() => {
  if (!enrollmentResult.value) return ''
  if (useDirectBinary.value) {
    return commandPlatform.value === 'powershell'
      ? enrollmentResult.value.direct_powershell_command || ''
      : enrollmentResult.value.direct_command || ''
  }
  if (!downloadSource.valid.value) return ''
  const raw = commandPlatform.value === 'powershell'
    ? enrollmentResult.value.powershell_command || ''
    : enrollmentResult.value.command || ''
  return applyDownloadSource(raw, commandPlatform.value, downloadSource.proxy.value)
})

const expiresLabel = computed(() => {
  const iso = connected.value?.expires_at
  if (!iso) return ''
  return t('temporaryComputer.expiresAt', {
    time: formatDateTime(iso, locale.value, app.displayTimezone, { timeStyle: 'short' }),
  })
})

function defaultPlatform(): 'shell' | 'powershell' {
  if (typeof navigator !== 'undefined' && /windows/i.test(navigator.userAgent)) return 'powershell'
  return app.data?.host.os === 'windows' ? 'powershell' : 'shell'
}

let generation = 0

function reset() {
  generation++
  ttlHours.value = 4
  commandPlatform.value = defaultPlatform()
  useDirectBinary.value = false
  serverAddress.value = serverAddresses.value[0]?.url || app.data?.network.public_urls.value[0] || ''
  enrollment.value = undefined
  enrollmentResult.value = undefined
  computerID.value = ''
  copied.value = false
  action.error.value = ''
}

function resetEnrollment() {
  generation++
  enrollment.value = undefined
  enrollmentResult.value = undefined
  computerID.value = ''
  copied.value = false
}

async function generate() {
  if (action.busy.value) return
  if (!serverAddress.value.trim()) return
  if (!useDirectBinary.value && !downloadSource.valid.value) return
  copied.value = false
  const currentGeneration = ++generation
  const serverURL = serverAddress.value.trim()
  const ttlSeconds = ttlHours.value * 3600
  const isCurrent = () => currentGeneration === generation && open.value && serverAddress.value.trim() === serverURL && ttlHours.value * 3600 === ttlSeconds

  await action.run(async () => {
    try {
      const targetSessionID = props.sessionId || await props.ensureSession()
      if (!isCurrent()) return
      const result = await request(enrollComputer({
        body: {
          container: false,
          temporary: true,
          session_id: targetSessionID,
          ttl_seconds: ttlSeconds,
          server_url: serverURL,
        },
      }))
      if (!isCurrent()) return
      enrollmentResult.value = result
      enrollment.value = result.enrollment
    } catch (error) {
      if (isCurrent()) throw error
    }
  })
}

let polling = false
useIntervalFn(async () => {
  if (polling || !open.value || document.visibilityState === 'hidden') return
  if (enrollment.value?.state !== 'waiting' && !(computerID.value && !connected.value)) return
  polling = true
  try {
    if (enrollment.value?.state === 'waiting') {
      const id = enrollment.value.id
      const value = await request(getEnrollment({ path: { id } }))
      if (!open.value || enrollment.value?.id !== id) return
      enrollment.value = value
      if (value.computer_id) {
        computerID.value = value.computer_id
        await app.refresh()
      }
    } else if (computerID.value && !connected.value) {
      await app.refresh()
    }
  } catch (e) {
    if (open.value) action.error.value = errorText(e)
  } finally {
    polling = false
  }
}, 2500)

async function copyCommand() {
  if (!displayedCommand.value) return
  await copy(displayedCommand.value)
}

function finish() {
  const item = connected.value
  open.value = false
  if (item) emit('connected', item)
}

watch([commandPlatform, useDirectBinary, downloadSource.preset, downloadSource.customURL], () => {
  copied.value = false
})
watch([serverAddress, ttlHours], () => {
  if (enrollmentResult.value) resetEnrollment()
})
watch(open, isOpen => {
  if (!isOpen) {
    generation++
    return
  }
  if (!action.busy.value && enrollment.value?.state !== 'waiting') reset()
}, { immediate: true })
</script>
<template>
  <Modal
    v-model:open="open"
    :title="t('temporaryComputer.title')"
  >
    <p
      v-if="action.error.value"
      class="error-box"
    >
      {{ action.error.value }}
    </p>
    <template v-if="!connected">
      <p class="muted">
        {{ t('temporaryComputer.subtitle') }}
      </p>
      <div class="stack">
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
                v-for="address in serverAddresses"
                :key="address.url"
                :value="address.url"
              >
                {{ address.label }}
              </option>
            </datalist>
          </SettingsRow>
          <SettingsRow :label="t('temporaryComputer.duration')">
            <select
              v-model.number="ttlHours"
              :aria-label="t('temporaryComputer.duration')"
              :disabled="action.busy.value"
            >
              <option
                v-for="hours in TTL_OPTIONS"
                :key="hours"
                :value="hours"
              >
                {{ t('temporaryComputer.hours', { count: hours }) }}
              </option>
            </select>
          </SettingsRow>
          <SettingsRow :label="t('computerCommand.targetPlatform')">
            <select
              v-model="commandPlatform"
              :aria-label="t('computerCommand.targetPlatform')"
            >
              <option value="shell">
                {{ t('computerCommand.shell') }}
              </option>
              <option value="powershell">
                {{ t('computerCommand.powershell') }}
              </option>
            </select>
          </SettingsRow>
          <DownloadSourceRow
            v-if="!useDirectBinary"
            :disabled="action.busy.value"
          />
        </SettingsSection>
        <template v-if="!enrollmentResult">
          <Button
            variant="primary"
            :busy="action.busy.value"
            :disabled="!serverAddress.trim() || (!useDirectBinary && !downloadSource.valid.value)"
            @click="generate"
          >
            {{ t('generateCommand') }}
          </Button>
        </template>
        <template v-else>
          <label
            v-if="hasDirectCommand"
            class="row"
          >
            <input
              v-model="useDirectBinary"
              type="checkbox"
              :disabled="action.busy.value"
            >
            <span>{{ t('temporaryComputer.alreadyDownloaded') }}</span>
          </label>
          <pre class="connection-command">{{ displayedCommand }}</pre>
          <div class="row">
            <Button
              :busy="action.busy.value"
              :disabled="!displayedCommand"
              @click="copyCommand"
            >
              <Check v-if="copied" /><Copy v-else />{{ t('copy') }}
            </Button>
            <Button
              :disabled="action.busy.value"
              @click="resetEnrollment"
            >
              {{ t('computerCommand.editConnection') }}
            </Button>
          </div>
          <p
            v-if="enrollment?.state === 'waiting'"
            class="row muted"
          >
            <span class="spinner" />{{ t('waitingComputer') }}
          </p>
          <template v-else-if="enrollment?.state === 'expired'">
            <p class="muted">
              {{ t('enrollmentExpired') }}
            </p>
            <Button
              :busy="action.busy.value"
              @click="generate"
            >
              {{ t('generateCommand') }}
            </Button>
          </template>
          <p class="muted small">
            {{ t('expires') }}: {{ formatDateTime(enrollmentResult.enrollment.expires_at, locale, app.displayTimezone) }}
          </p>
        </template>
      </div>
    </template>
    <div
      v-else
      class="stack"
    >
      <span class="badge good align-start"><Check />{{ t('online') }}</span>
      <h2>{{ connected.name }}</h2>
      <p
        v-if="connected.initial_cwd || expiresLabel"
        class="muted"
      >
        {{ [connected.initial_cwd, expiresLabel].filter(Boolean).join(locale === 'zh' ? '，' : ', ') }}
      </p>
      <p class="muted">
        {{ t('temporaryComputer.connectedNote') }}
      </p>
      <Button
        variant="primary"
        @click="finish"
      >
        {{ t('temporaryComputer.done') }}
      </Button>
    </div>
  </Modal>
</template>

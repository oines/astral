<script setup lang="ts">
import { computed, ref } from 'vue'
import { useQuery } from '@pinia/colada'
import { useIntervalFn } from '@vueuse/core'
import { listComputersQuery, listComputersQueryKey } from '@astral/sdk/colada'
import { createLocalContainer, enrollComputer, getComputerOperation, getEnrollment, revokeComputer, listComputers, updateComputer, useLocalComputer, type AstralInternalDomainProvisioning, type AstralInternalDomainEnrollment, type AstralInternalComputersEnrollmentResult } from '@astral/sdk'
import { Button, Modal, SettingsSection, SettingsRow } from '@astral/ui'
import { ArrowLeft, Check, ChevronRight, Copy, Monitor, Plus, Server, Terminal, Search } from '@astral/icons'
import { useI18n } from 'vue-i18n'
import type { Computer } from '../../lib/types'
import { errorText, operatingSystem, request } from '../../lib/api'
import { useAction } from '../../composables/useAction'
import { useSettingsSave } from '../../composables/useSettingsSave'
import SettingsText from './SettingsText.vue'
import SettingsFeedback from './SettingsFeedback.vue'
import { useAppStore } from '../../stores/app'
import ResourceDelete from './ResourceDelete.vue'
const { t } = useI18n()
const app = useAppStore()
const action = useAction()
const saves = useSettingsSave()
const list = useQuery(listComputersQuery())
const search = ref('')
const visibleComputers = computed(() => (list.data.value || []).filter(c => `${c.name} ${c.hostname}`.toLocaleLowerCase().includes(search.value.toLocaleLowerCase())).sort((a, b) => Number(a.revoked) - Number(b.revoked) || Number(b.online) - Number(a.online)))
const selectedID = ref('')
const selected = computed(() => list.data.value?.find(c => c.id === selectedID.value))
const adding = ref(false)
const mode = ref<'choose' | 'local' | 'container' | 'remote'>('choose')
const container = ref(false)
const operation = ref<AstralInternalDomainProvisioning>()
const enrollment = ref<AstralInternalDomainEnrollment>()
const enrollmentResult = ref<AstralInternalComputersEnrollmentResult>()
const computerID = ref('')
const connected = computed(() => list.data.value?.find(c => c.id === computerID.value && c.online))
const intent = ref('')
const copied = ref(false)
const assign = ref(false)
const deleting = ref(false)
const activeOperationKey = 'astral:pending-container'
const pending = localStorage.getItem(activeOperationKey)
if (pending) {
  try {
    const saved = JSON.parse(pending) as { id: string; intent: string }
    void request(getComputerOperation({ path: { id: saved.id } })).then(value => {
      operation.value = value
      intent.value = saved.intent
      computerID.value = value.computer_id || ''
      if (value.state !== 'connected') { mode.value = 'container'; adding.value = true }
      else localStorage.removeItem(activeOperationKey)
    }).catch(() => localStorage.removeItem(activeOperationKey))
  } catch { localStorage.removeItem(activeOperationKey) }
}
function edit(computer: Computer) { selectedID.value = computer.id; action.success.value = ''; action.error.value = '' }
function add() {
  mode.value = 'choose'
  operation.value = undefined
  enrollment.value = undefined
  enrollmentResult.value = undefined
  computerID.value = ''
  copied.value = false
  intent.value = crypto.randomUUID()
  action.error.value = ''
  adding.value = true
}
async function local() {
  mode.value = 'local'
  await action.run(async () => {
    const c = await request(useLocalComputer())
    computerID.value = c.id
    await list.refetch()
    await app.refresh()
  })
}
async function localContainer() {
  mode.value = 'container'
  await action.run(async () => {
    operation.value = await request(createLocalContainer({ body: { id: intent.value } }))
    computerID.value = operation.value.computer_id || ''
    localStorage.setItem(activeOperationKey, JSON.stringify({ id: operation.value.id, intent: intent.value }))
  })
}
async function remote() {
  mode.value = 'remote'
  copied.value = false
  await action.run(async () => {
    enrollmentResult.value = await request(enrollComputer({ body: { container: container.value } }))
    enrollment.value = enrollmentResult.value.enrollment
  })
}
let polling = false
useIntervalFn(async () => {
  if (polling || document.visibilityState === 'hidden') return
  polling = true
  try {
    await list.refetch()
    if (!adding.value) return
    if (operation.value && !['connected', 'failed'].includes(operation.value.state)) {
      operation.value = await request(getComputerOperation({ path: { id: operation.value.id } }))
      if (operation.value.computer_id) computerID.value = operation.value.computer_id
      if (operation.value.state === 'connected') { localStorage.removeItem(activeOperationKey); await app.refresh() }
    }
    if (enrollment.value?.state === 'waiting') {
      enrollment.value = await request(getEnrollment({ path: { id: enrollment.value.id } }))
      if (enrollment.value.computer_id) { computerID.value = enrollment.value.computer_id; await app.refresh() }
    }
  } catch (e) { if (adding.value) action.error.value = errorText(e) }
  finally { polling = false }
}, 3000)
async function rename(id: string, name: string) {
  await saves.run(`computer:${id}`, 'name', {
    write: async () => {
      const current = (await request(listComputers())).find(c => c.id === id)
      if (!current) throw { code: 'not_found', message: t('noComputers') }
      if (current.name !== name) saves.confirm(listComputersQueryKey(), await request(updateComputer({ path: { id }, body: { name, revision: current.revision } })))
      await app.refresh().catch(() => undefined)
    },
    reconcile: async () => {
      const current = (await request(listComputers())).find(c => c.id === id)
      if (current) saves.confirm(listComputersQueryKey(), current)
      await app.refresh().catch(() => undefined)
      return current?.name === name ? 'applied' : 'not-applied'
    },
  })
}
async function revoke() {
  await action.run(async () => { if (selected.value) await request(revokeComputer({ path: { id: selected.value.id } })); deleting.value = false; await app.refresh() })
}
function openAssignment() { assign.value = true }
async function copyCommand() {
  if (!enrollmentResult.value) return
  await action.run(async () => { await navigator.clipboard.writeText(enrollmentResult.value!.command); copied.value = true })
}
const assigned = computed(() => app.agents.filter(a => a.allowed_computers.includes(selectedID.value)))
</script>
<template>
  <div class="stack">
    <p
      v-if="action.error.value && !adding && !assign"
      class="error-box"
    >
      {{ action.error.value }}
    </p><p
      v-if="action.success.value && !selected"
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
            :placeholder="t('settingsSearchComputers')"
            :aria-label="t('computers')"
          >
        </div><Button
          size="small"
          @click="add"
        >
          <Plus />{{ t('newComputer') }}
        </Button>
      </div>
      <div
        v-if="list.data.value?.length"
        class="resource-list"
      >
        <button
          v-for="c in visibleComputers"
          :key="c.id"
          class="resource-row"
          @click="edit(c)"
        >
          <span class="resource-symbol"><Monitor v-if="c.kind !== 'container'" /><Server v-else /></span><span><strong>{{ c.name }}</strong><small>{{ operatingSystem(c.os) }} · {{ t('accessCount', { count: app.agents.filter(a => a.allowed_computers.includes(c.id)).length }) }}</small></span><span
            class="badge"
            :class="{ good: c.online, bad: c.revoked }"
          >{{ t(c.revoked ? 'revoked' : c.online ? 'online' : 'offline') }}</span><ChevronRight />
        </button><p
          v-if="!visibleComputers.length"
          class="empty"
        >
          {{ t('computersNoMatch') }}
        </p>
      </div>
      <div
        v-else
        class="empty-state"
      >
        <Monitor /><strong>{{ t('noComputers') }}</strong><p>{{ t('noComputersHint') }}</p><Button
          variant="primary"
          @click="add"
        >
          {{ t('newComputer') }}
        </Button>
      </div>
    </template>
    <template v-else>
      <button
        type="button"
        class="provider-back"
        @click="selectedID = ''"
      >
        <ArrowLeft />{{ t('allComputers') }}
      </button>
      <div class="settings-detail-heading">
        <span class="resource-symbol"><Monitor v-if="selected.kind !== 'container'" /><Server v-else /></span>
        <div class="settings-detail-identity">
          <h2>{{ selected.name }}</h2><p>{{ [selected.name !== selected.hostname ? selected.hostname : '', operatingSystem(selected.os)].filter(Boolean).join(' · ') }}</p>
        </div><span
          class="badge"
          :class="{ good: selected.online, bad: selected.revoked }"
        >{{ t(selected.revoked ? 'revoked' : selected.online ? 'online' : 'offline') }}</span>
      </div>
      <p
        v-if="selected.last_error"
        class="error-box"
      >
        {{ selected.last_error }}
      </p>
      <SettingsSection :title="t('basicInfo')">
        <SettingsRow :label="t('name')">
          <SettingsText
            :value="selected.name"
            :draft-key="`computer:${selected.id}/name`"
            :label="t('name')"
            required
            @commit="rename(selected!.id, $event)"
          />
        </SettingsRow>
        <SettingsFeedback :scope="`computer:${selected.id}/name`" />
      </SettingsSection>
      <SettingsSection :title="t('computerAccess')">
        <template #actions>
          <Button
            size="small"
            :disabled="selected.revoked"
            @click="openAssignment"
          >
            {{ t('chooseBotAccess') }}
          </Button>
        </template>
        <RouterLink
          v-for="a in assigned"
          :key="a.id"
          :to="{ path: '/settings/agents', query: { agent: a.id, tab: 'computers' } }"
          class="resource-row"
        >
          <span class="agent-avatar">{{ a.avatar || a.name.slice(0, 1) }}</span><span><strong>{{ a.name }}</strong><small v-if="a.default_computer_id === selected.id">{{ t('defaultComputer') }}</small></span><ChevronRight />
        </RouterLink><p
          v-if="!assigned.length"
          class="empty"
        >
          {{ t('noComputerAssignments') }}
        </p>
      </SettingsSection>
      <SettingsSection v-if="!selected.revoked">
        <SettingsRow
          :label="t('revokeConnection')"
          :description="t('revokeComputerHint')"
        >
          <Button
            variant="danger"
            size="small"
            @click="action.error.value = ''; deleting = true"
          >
            {{ t('revokeConnection') }}
          </Button>
        </SettingsRow>
      </SettingsSection>
      <ResourceDelete
        :id="selected.id"
        :open="deleting"
        kind="computers"
        :name="selected.name"
        :action-label="t('revokeConnection')"
        :busy="action.busy.value"
        :error-message="action.error.value"
        @update:open="deleting = $event"
        @confirm="revoke"
      />
    </template>
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
          @click="mode = 'remote'"
        >
          <Terminal /><span><strong>{{ t('remote') }}</strong><small>{{ t('remoteMachineHint') }}</small></span><ChevronRight />
        </button>
      </template>
      <template v-else-if="!connected">
        <button
          type="button"
          class="provider-back"
          :disabled="action.busy.value"
          @click="mode = 'choose'; action.error.value = ''"
        >
          <ArrowLeft />{{ t('backToComputerOptions') }}
        </button>
        <div
          v-if="mode === 'remote'"
          class="stack"
        >
          <template v-if="!enrollmentResult">
            <p class="muted">
              {{ t('commandHint') }}
            </p><label class="row"><input
              v-model="container"
              type="checkbox"
            >{{ t('useContainer') }}</label><Button
              variant="primary"
              :busy="action.busy.value"
              @click="remote"
            >
              {{ t('generateCommand') }}
            </Button>
          </template>
          <template v-else>
            <pre class="connection-command">{{ enrollmentResult.command }}</pre><Button
              :busy="action.busy.value"
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
              {{ t('expires') }}: {{ new Date(enrollmentResult.enrollment.expires_at).toLocaleString() }}
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
          {{ operatingSystem(connected.os) }} · {{ connected.arch }}
        </p><Button
          variant="primary"
          @click="adding = false; openAssignment()"
        >
          {{ t('chooseBotAccess') }}
        </Button>
      </div>
    </Modal>
    <Modal
      v-model:open="assign"
      :title="t('chooseBotAccess')"
    >
      <div
        v-if="app.agents.length"
        class="resource-list"
      >
        <RouterLink
          v-for="bot in app.agents"
          :key="bot.id"
          :to="{ path: '/settings/agents', query: { agent: bot.id, tab: 'computers' } }"
          class="resource-row"
          @click="assign = false"
        >
          <span class="agent-avatar">{{ bot.avatar || bot.name.slice(0, 1) }}</span><span><strong>{{ bot.name }}</strong></span><ChevronRight />
        </RouterLink>
      </div>
      <RouterLink
        v-else
        to="/settings/agents"
        class="button button-primary"
        @click="assign = false"
      >
        {{ t('newAgent') }}
      </RouterLink>
    </Modal>
  </div>
</template>

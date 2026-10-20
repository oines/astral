<script setup lang="ts">
import { computed, ref } from 'vue'
import { useQuery } from '@pinia/colada'
import { useIntervalFn } from '@vueuse/core'
import { listComputersQuery } from '@astral/sdk/colada'
import { createLocalContainer, enrollComputer, getComputerOperation, getEnrollment, revokeComputer, saveAgent, updateComputer, useLocalComputer, type AstralInternalDomainProvisioning, type AstralInternalDomainEnrollment, type AstralInternalComputersEnrollmentResult } from '@astral/sdk'
import { Button, Modal } from '@astral/ui'
import { ArrowLeft, Check, ChevronRight, Copy, Monitor, Plus, Server, Terminal } from '@astral/icons'
import { useI18n } from 'vue-i18n'
import type { Computer } from '../../lib/types'
import { clone, errorText, request } from '../../lib/api'
import { useAction } from '../../composables/useAction'
import { useAppStore } from '../../stores/app'
import ResourceDelete from './ResourceDelete.vue'
const { t } = useI18n()
const app = useAppStore()
const action = useAction()
const list = useQuery(listComputersQuery())
const selectedID = ref('')
const selected = computed(() => list.data.value?.find(c => c.id === selectedID.value))
const name = ref('')
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
const agentID = ref('')
const isDefault = ref(false)
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
function edit(computer: Computer) { selectedID.value = computer.id; name.value = computer.name }
function add() {
  mode.value = 'choose'
  operation.value = undefined
  enrollment.value = undefined
  enrollmentResult.value = undefined
  computerID.value = ''
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
async function rename() {
  await action.run(async () => { if (selected.value) await request(updateComputer({ path: { id: selected.value.id }, body: { name: name.value, revision: selected.value.revision } })); await app.refresh() }, t('saved'))
}
async function revoke() {
  await action.run(async () => { if (selected.value) await request(revokeComputer({ path: { id: selected.value.id } })); deleting.value = false; await app.refresh() })
}
async function assignComputer() {
  await action.run(async () => {
    const agent = app.agents.find(a => a.id === agentID.value)
    if (!agent) return
    const target = computerID.value || selectedID.value
    const updated = clone(agent)
    if (!updated.allowed_computers.includes(target)) updated.allowed_computers.push(target)
    if (isDefault.value) updated.default_computer_id = target
    await request(saveAgent({ body: updated }))
    assign.value = false
    await app.refresh()
  }, t('saved'))
}
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
      v-if="action.success.value"
      class="success-note"
    >
      {{ action.success.value }}
    </p>
    <template v-if="!selected">
      <div class="row between">
        <p class="muted">
          {{ t('computersHint') }}
        </p><Button
          size="small"
          @click="add"
        >
          <Plus />{{ t('newComputer') }}
        </Button>
      </div>
      <div class="resource-list">
        <button
          v-for="c in list.data.value"
          :key="c.id"
          class="resource-row"
          @click="edit(c)"
        >
          <span class="resource-symbol"><Monitor v-if="c.kind !== 'container'" /><Server v-else /></span><span><strong>{{ c.name }}</strong><small>{{ c.os }} · {{ c.arch }} · {{ c.kind }}</small></span><span
            class="badge"
            :class="{ good: c.online, bad: c.revoked }"
          >{{ t(c.revoked ? 'revoked' : c.online ? 'online' : 'offline') }}</span><ChevronRight />
        </button><p
          v-if="!list.data.value?.length"
          class="empty"
        >
          {{ t('empty') }}
        </p>
      </div>
    </template>
    <template v-else>
      <div class="row between">
        <Button
          variant="ghost"
          size="small"
          @click="selectedID = ''"
        >
          <ArrowLeft />{{ t('computers') }}
        </Button><span
          class="badge"
          :class="{ good: selected.online, bad: selected.revoked }"
        >{{ t(selected.revoked ? 'revoked' : selected.online ? 'online' : 'offline') }}</span>
      </div>
      <label class="label">{{ t('name') }}<input v-model="name"></label>
      <div class="field-grid">
        <div class="detail-block">
          <h3>{{ t('host') }}</h3><p class="muted">
            {{ selected.hostname }}<br>{{ selected.os }} / {{ selected.arch }}<br>{{ selected.shell }}
          </p>
        </div><div class="detail-block">
          <h3>{{ t('home') }}</h3><p class="muted mono">
            {{ selected.home }}
          </p>
        </div>
      </div>
      <p
        v-if="selected.last_error"
        class="error-box"
      >
        {{ selected.last_error }}
      </p>
      <div class="detail-block stack">
        <h3>{{ t('agents') }}</h3><RouterLink
          v-for="a in assigned"
          :key="a.id"
          to="/settings/agents"
        >
          {{ a.name }}<span
            v-if="a.default_computer_id === selected.id"
            class="badge"
          >{{ t('default') }}</span>
        </RouterLink><p
          v-if="!assigned.length"
          class="muted"
        >
          {{ t('none') }}
        </p><Button
          class="align-start"
          :disabled="selected.revoked"
          @click="computerID = selected.id; assign = true"
        >
          {{ t('assign') }}
        </Button>
      </div>
      <div class="form-footer">
        <Button
          v-if="!selected.revoked"
          variant="danger"
          @click="deleting = true"
        >
          {{ t('revokeConnection') }}
        </Button><Button
          variant="primary"
          :busy="action.busy.value"
          @click="rename"
        >
          {{ t('save') }}
        </Button>
      </div>
      <ResourceDelete
        :id="selected.id"
        :open="deleting"
        kind="computers"
        :name="selected.name"
        :action-label="t('revokeConnection')"
        :busy="action.busy.value"
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
          {{ app.data?.host.hostname }} · {{ app.data?.host.os }} / {{ app.data?.host.arch }}
        </p>
        <button
          class="computer-option"
          @click="local"
        >
          <Monitor /><span><strong>{{ t('useLocal') }}</strong><small>{{ app.data?.host.hostname }} · {{ app.data?.host.os }}</small></span><ChevronRight />
        </button>
        <button
          class="computer-option"
          @click="localContainer"
        >
          <Server /><span><strong>{{ t('localContainer') }}</strong><small>{{ app.data?.host.hostname }} · {{ app.data?.host.container_runtime || 'Docker' }}</small></span><ChevronRight />
        </button>
        <button
          class="computer-option"
          @click="mode = 'remote'"
        >
          <Terminal /><span><strong>{{ t('remote') }}</strong><small>macOS / Linux</small></span><ChevronRight />
        </button>
      </template>
      <template v-else-if="!connected">
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
            <p class="row muted">
              <span class="spinner" />{{ t('waitingComputer') }}
            </p><p class="muted small">
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
          /><span>{{ t('waitingComputer') }}</span><Button
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
          {{ connected.os }} · {{ connected.arch }}
        </p><Button
          variant="primary"
          @click="adding = false; assign = true"
        >
          {{ t('assign') }}
        </Button>
      </div>
    </Modal>
    <Modal
      v-model:open="assign"
      :title="t('assign')"
    >
      <p
        v-if="action.error.value"
        class="error-box"
      >
        {{ action.error.value }}
      </p>
      <label class="label">{{ t('agent') }}<select v-model="agentID"><option
        value=""
        disabled
      >{{ t('select') }}</option><option
        v-for="a in app.agents"
        :key="a.id"
        :value="a.id"
      >{{ a.name }}</option></select></label>
      <label class="row"><input
        v-model="isDefault"
        type="checkbox"
      >{{ t('defaultComputer') }}</label>
      <RouterLink
        v-if="!app.agents.length"
        to="/settings/agents"
        @click="assign = false"
      >
        {{ t('setupAgent') }}
      </RouterLink>
      <Button
        variant="primary"
        :busy="action.busy.value"
        :disabled="!agentID"
        @click="assignComputer"
      >
        {{ t('save') }}
      </Button>
    </Modal>
  </div>
</template>

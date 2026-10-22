<script setup lang="ts">
import { computed, ref } from 'vue'
import { useQuery } from '@pinia/colada'
import { listComputersQuery, listComputersQueryKey } from '@astral/sdk/colada'
import { revokeComputer, listComputers, updateComputer } from '@astral/sdk'
import { Button, Modal, SettingsSection, SettingsRow } from '@astral/ui'
import { ArrowLeft, ChevronRight, Monitor, Plus, Server, Search } from '@astral/icons'
import { useI18n } from 'vue-i18n'
import type { Computer } from '../../lib/types'
import { operatingSystem, request } from '../../lib/api'
import { useAction } from '../../composables/useAction'
import { useSettingsSave } from '../../composables/useSettingsSave'
import SettingsText from './SettingsText.vue'
import SettingsFeedback from './SettingsFeedback.vue'
import { useAppStore } from '../../stores/app'
import ResourceDelete from './ResourceDelete.vue'
import AddComputerDialog from './AddComputerDialog.vue'
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
const assign = ref(false)
const deleting = ref(false)
function edit(computer: Computer) { selectedID.value = computer.id; action.success.value = ''; action.error.value = '' }
function add() { adding.value = true }
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
    <AddComputerDialog
      v-model:open="adding"
      @connected="openAssignment"
    />
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

<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { useQuery } from '@pinia/colada'
import { listSearchProvidersQuery, listSearchProvidersQueryKey } from '@astral/sdk/colada'
import { deleteSearchProvider, listSearchProviders, saveSearchProvider, testSearchProvider, type AstralInternalDomainSearchProvider, type AstralInternalSearchResponse } from '@astral/sdk'
import { Button, Modal, Select, SettingsSection, SettingsRow } from '@astral/ui'
import { ArrowLeft, ChevronRight, Globe, Plus } from '@astral/icons'
import { useI18n } from 'vue-i18n'
import { clone, newMeta, request } from '../../lib/api'
import { useAction } from '../../composables/useAction'
import { useSettingsSave } from '../../composables/useSettingsSave'
import SettingsFeedback from './SettingsFeedback.vue'
import { useAppStore } from '../../stores/app'
import ResourceDelete from './ResourceDelete.vue'
const { t } = useI18n()
const app = useAppStore()
const action = useAction()
const saves = useSettingsSave()
const editorKey = ref('')
const drafts = new Map<string, { provider: AstralInternalDomainSearchProvider; key: string; baseline: string }>()
const list = useQuery(listSearchProvidersQuery())
const services = [{ id: 'tavily', name: 'Tavily', url: 'tavily.com' }, { id: 'exa', name: 'Exa', url: 'exa.ai' }]
const selected = ref<AstralInternalDomainSearchProvider>()
const saved = ref('')
const key = ref('')
const query = ref('')
const result = ref<AstralInternalSearchResponse>()
const deleting = ref(false)
const adding = ref(false)
function configuration(provider: AstralInternalDomainSearchProvider) {
  return JSON.stringify({ name: provider.name, adapter: provider.adapter, base_url: provider.base_url || '' })
}
const dirty = computed(() => !!key.value || !!selected.value && configuration(selected.value) !== saved.value)
watch(dirty, value => { if (value) action.success.value = '' })
function keepDraft() { if (selected.value) drafts.set(selected.value.id || editorKey.value, { provider: selected.value, key: key.value, baseline: saved.value }) }
const assigned = computed(() => app.agents.filter(a => a.search_provider_id === selected.value?.id))
function usage(id: string) {
  const count = app.agents.filter(a => a.search_provider_id === id).length
  return t(count ? 'searchUsageCount' : 'searchNotAssigned', { count })
}
function edit(item?: AstralInternalDomainSearchProvider, adapter = 'tavily') {
  keepDraft()
  action.error.value = ''; action.success.value = ''
  editorKey.value = item?.id || `new:${adapter}`
  const draft = drafts.get(editorKey.value)
  selected.value = draft?.provider || (item ? clone(item) : { ...newMeta(), name: adapter === 'exa' ? 'Exa' : 'Tavily', adapter, base_url: '' })
  saved.value = draft?.baseline || configuration(selected.value)
  key.value = draft?.key || ''; query.value = ''; result.value = undefined; adding.value = false
}
async function save() {
  if (!selected.value) return
  const snapshot = clone(selected.value)
  const editing = selected.value
  const apiKey = key.value
  const resource = editorKey.value
  let beforeCredential: string | undefined
  const accept = async (value: AstralInternalDomainSearchProvider) => {
    const changes = { name: editing.name, adapter: editing.adapter, base_url: editing.base_url }
    Object.assign(editing, value)
    for (const field of ['name', 'adapter', 'base_url'] as const) {
      if (changes[field] !== snapshot[field]) Object.assign(editing, { [field]: changes[field] })
    }
    const baseline = configuration(value)
    const active = editorKey.value === resource && selected.value === editing
    const remainingKey = active ? key.value : drafts.get(resource)?.key ?? apiKey
    const entry = { provider: editing, key: remainingKey === apiKey ? '' : remainingKey, baseline }
    drafts.set(value.id, entry)
    if (!snapshot.id) drafts.delete(resource)
    if (active) {
      key.value = entry.key
      saved.value = baseline
    }
    saves.confirm(listSearchProvidersQueryKey(), value)
    await app.refresh().catch(() => undefined)
  }
  await saves.run(`search:${resource}`, 'config', {
    write: async () => {
      const current = snapshot.id ? (await request(listSearchProviders())).find(p => p.id === snapshot.id) : undefined
      if (snapshot.id && !current) throw { code: 'not_found', message: t('noResults') }
      beforeCredential = current?.credential_id
      const value = await request(saveSearchProvider({ body: { ...(current || newMeta()), name: snapshot.name.trim(), adapter: snapshot.adapter, base_url: (snapshot.base_url || '').trim(), ...(apiKey ? { api_key: apiKey } : {}) } }))
      await accept(value)
    },
    reconcile: async () => {
      if (!snapshot.id) return 'unknown'
      const current = (await request(listSearchProviders())).find(p => p.id === snapshot.id)
      const matches = current && current.name === snapshot.name.trim() && current.adapter === snapshot.adapter && current.base_url === (snapshot.base_url || '').trim()
      if (matches && (!apiKey || current.credential_id !== beforeCredential)) { await accept(current); return 'applied' }
      return apiKey && matches ? 'unknown' : 'not-applied'
    },
  })
}
async function test() {
  if (!selected.value?.id || dirty.value) return
  const id = selected.value.id
  await action.run(async () => { result.value = await request(testSearchProvider({ path: { id }, body: { query: query.value.trim() } })) })
}
async function remove() {
  await action.run(async () => {
    if (!selected.value) return
    await request(deleteSearchProvider({ path: { id: selected.value.id }, query: { revision: selected.value.revision } }))
    selected.value = undefined; deleting.value = false
    await app.refresh()
  })
}
</script>
<template>
  <div class="stack">
    <p
      v-if="action.error.value"
      class="error-box"
      role="alert"
    >
      {{ action.error.value }}
    </p>
    <p
      v-if="action.success.value && !selected"
      class="success-note"
      role="status"
    >
      {{ action.success.value }}
    </p>
    <template v-if="!selected">
      <div class="settings-toolbar">
        <p class="muted">
          {{ t('searchSetupHint') }}
        </p>
        <Button
          v-if="list.data.value?.length"
          size="small"
          @click="adding = true"
        >
          <Plus />{{ t('addSearchService') }}
        </Button>
      </div>
      <div
        v-if="list.data.value?.length"
        class="resource-list"
      >
        <button
          v-for="item in list.data.value"
          :key="item.id"
          class="resource-row"
          @click="edit(item)"
        >
          <span class="resource-symbol"><Globe /></span><span><strong>{{ item.name }}</strong><small>{{ item.adapter === 'exa' ? 'Exa' : 'Tavily' }} · {{ usage(item.id) }}</small></span><ChevronRight />
        </button>
      </div>
      <div
        v-else
        class="resource-grid"
      >
        <button
          v-for="service in services"
          :key="service.id"
          class="resource-card"
          @click="edit(undefined, service.id)"
        >
          <span class="resource-symbol"><Globe /></span><span class="resource-card-copy"><strong>{{ service.name }}</strong><small>{{ service.url }}</small></span><Plus />
        </button>
      </div>
    </template>
    <form
      v-else
      class="stack settings-form config-form"
      @submit.prevent="save"
    >
      <button
        type="button"
        class="provider-back"
        @click="keepDraft(); selected = undefined"
      >
        <ArrowLeft />{{ t('allSearchServices') }}
      </button>
      <div class="settings-detail-heading">
        <span class="resource-symbol"><Globe /></span><h2>{{ selected.name || t('webSearch') }}</h2>
      </div>
      <SettingsSection :title="t('connectionSettings')">
        <SettingsRow :label="t('name')">
          <input
            v-model="selected.name"
            :aria-label="t('name')"
            required
          >
        </SettingsRow>
        <SettingsRow :label="t('searchServices')">
          <Select
            v-model="selected.adapter"
            :label="t('searchServices')"
            :options="services.map(service => ({ value: service.id, label: service.name }))"
          />
        </SettingsRow>
        <SettingsRow :label="t('apiKey')">
          <input
            v-model="key"
            :aria-label="t('apiKey')"
            type="password"
            autocomplete="new-password"
            :required="!selected.credential_id"
            :placeholder="selected.credential_id ? t('storedSecret') : ''"
          >
        </SettingsRow>
        <SettingsRow
          :label="t('endpoint')"
          :description="t('officialEndpointHint')"
        >
          <input
            v-model="selected.base_url"
            :aria-label="t('endpoint')"
            type="url"
            :placeholder="selected.adapter === 'exa' ? 'https://api.exa.ai' : 'https://api.tavily.com'"
          >
        </SettingsRow>
        <div class="settings-submit-row">
          <p>{{ t('connectionDraftHint') }}</p><Button
            type="submit"
            variant="primary"
            :busy="saves.state(`search:${editorKey}/config`).status === 'saving'"
            :disabled="!!selected.id && !dirty"
          >
            {{ t('saveConnectionSettings') }}
          </Button>
        </div>
        <SettingsFeedback :scope="`search:${editorKey}/config`" />
      </SettingsSection>
      <template v-if="selected.id">
        <SettingsSection
          :title="t('testSearch')"
          :description="t('testSavedConnectionHint')"
        >
          <SettingsRow :label="t('testQuery')">
            <div class="row search-test-input">
              <input
                v-model="query"
                :aria-label="t('testQuery')"
                :placeholder="t('testQuery')"
              ><Button
                :busy="action.busy.value"
                :disabled="!query.trim() || dirty"
                @click="test"
              >
                {{ t('testSearch') }}
              </Button>
            </div>
          </SettingsRow>
          <p
            v-if="dirty"
            class="settings-context-note"
          >
            {{ t('connectionDraftPending') }}
          </p>
          <div
            v-if="result"
            class="search-results"
            role="status"
          >
            <p class="muted">
              {{ t('searchResults', { count: result.results?.length || 0 }) }}
            </p>
            <a
              v-for="item in result.results"
              :key="item.url"
              :href="item.url"
              target="_blank"
              rel="noopener noreferrer"
            ><strong>{{ item.title }}</strong><p class="muted">{{ item.summary }}</p></a>
          </div>
        </SettingsSection>
        <SettingsSection :title="t('searchInAgents')">
          <RouterLink
            v-for="agent in assigned"
            :key="agent.id"
            :to="{ path: '/settings/agents', query: { agent: agent.id } }"
            class="resource-row"
          >
            <span class="agent-avatar">{{ agent.avatar || agent.name.slice(0, 1) }}</span><span><strong>{{ agent.name }}</strong></span><ChevronRight />
          </RouterLink>
          <div
            v-if="!assigned.length"
            class="settings-next-step"
          >
            <p class="muted">
              {{ t('searchAssignHint') }}
            </p><RouterLink
              to="/settings/agents"
              class="button button-secondary button-small"
            >
              {{ t('manageAgents') }}<ChevronRight />
            </RouterLink>
          </div>
        </SettingsSection>
        <SettingsSection>
          <SettingsRow :label="`${t('delete')} ${selected.name}`">
            <Button
              variant="danger"
              size="small"
              @click="action.error.value = ''; deleting = true"
            >
              {{ t('delete') }}
            </Button>
          </SettingsRow>
        </SettingsSection>
      </template>
      <ResourceDelete
        :id="selected.id"
        :open="deleting"
        kind="search"
        :name="selected.name"
        :busy="action.busy.value"
        :error-message="action.error.value"
        @update:open="deleting = $event"
        @confirm="remove"
      />
    </form>
    <Modal
      v-model:open="adding"
      :title="t('selectSearchService')"
    >
      <button
        v-for="service in services"
        :key="service.id"
        class="computer-option"
        @click="edit(undefined, service.id)"
      >
        <Globe /><span><strong>{{ service.name }}</strong><small>{{ service.url }}</small></span><ChevronRight />
      </button>
    </Modal>
  </div>
</template>

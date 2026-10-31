<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { useQuery } from '@pinia/colada'
import { listSearchAdaptersQuery, listSearchProvidersQuery, listSearchProvidersQueryKey } from '@astral/sdk/colada'
import { deleteSearchProvider, listSearchProviders, saveSearchProvider, testSearchProvider, type AstralInternalDomainSearchProvider, type AstralInternalSearchField, type AstralInternalSearchResponse } from '@astral/sdk'
import { Button, Modal, Select, SettingsSection, SettingsRow } from '@astral/ui'
import { ArrowLeft, ChevronRight, Globe, Plus } from '@astral/icons'
import { useI18n } from 'vue-i18n'
import { clone, newMeta, request } from '../../lib/api'
import { useAction } from '../../composables/useAction'
import { useSettingsSave } from '../../composables/useSettingsSave'
import SettingsFeedback from './SettingsFeedback.vue'
import { useAppStore } from '../../stores/app'
import ResourceDelete from './ResourceDelete.vue'

type Provider = AstralInternalDomainSearchProvider & { config: Record<string, string> }
type Draft = { provider: Provider; credentials: Record<string, string>; baseline: string }
const { t, te } = useI18n()
const app = useAppStore()
const action = useAction()
const saves = useSettingsSave()
const editorKey = ref('')
const drafts = new Map<string, Draft>()
const list = useQuery(listSearchProvidersQuery())
const catalog = useQuery(listSearchAdaptersQuery())
const services = computed(() => catalog.data.value || [])
const selected = ref<Provider>()
const activeService = computed(() => services.value.find(item => item.id === selected.value?.adapter))
const saved = ref('')
const credentials = ref<Record<string, string>>({})
const query = ref('')
const result = ref<AstralInternalSearchResponse>()
const deleting = ref(false)
const adding = ref(false)
const configValues = (config: Record<string, string> = {}) => Object.fromEntries(Object.entries(config).filter(([, value]) => value.trim()).sort(([a], [b]) => a.localeCompare(b)).map(([name, value]) => [name, value.trim()]))
function configuration(provider: AstralInternalDomainSearchProvider) {
  return JSON.stringify({ name: provider.name, adapter: provider.adapter, base_url: provider.base_url || '', config: configValues(provider.config) })
}
const secretUpdates = computed(() => Object.fromEntries(Object.entries(credentials.value).filter(([, value]) => value !== '')))
const dirty = computed(() => Object.keys(secretUpdates.value).length > 0 || !!selected.value && configuration(selected.value) !== saved.value)
watch(dirty, value => { if (value) { action.success.value = ''; result.value = undefined } })
watch(query, () => { result.value = undefined })
function keepDraft() { if (selected.value) drafts.set(selected.value.id || editorKey.value, { provider: selected.value, credentials: credentials.value, baseline: saved.value }) }
const assigned = computed(() => app.agents.filter(a => a.search_provider_id === selected.value?.id))
function usage(id: string) {
  const count = app.agents.filter(a => a.search_provider_id === id).length
  return t(count ? 'searchUsageCount' : 'searchNotAssigned', { count })
}
function serviceHost(website: string) { return new URL(website).hostname }
function serviceName(adapter: string) { return services.value.find(item => item.id === adapter)?.name || adapter }
function fieldLabel(field: AstralInternalSearchField) {
  const key = `searchField_${field.name}`
  return te(key) ? t(key) : field.label
}
function hasStoredSecret(name: string) {
  if (!selected.value?.credential_id || !saved.value || JSON.parse(saved.value).adapter !== selected.value.adapter) return false
  return selected.value.credential_fields?.includes(name) ?? name === 'api_key'
}
function changeAdapter(adapter: string) {
  if (!selected.value || adapter === selected.value.adapter) return
  if (selected.value.name === serviceName(selected.value.adapter)) selected.value.name = serviceName(adapter)
  selected.value.adapter = adapter
  selected.value.config = {}
  selected.value.base_url = ''
  credentials.value = {}
  result.value = undefined
}
function edit(item?: AstralInternalDomainSearchProvider, adapter = 'tavily') {
  keepDraft()
  action.error.value = ''; action.success.value = ''
  editorKey.value = item?.id || `new:${adapter}`
  const draft = drafts.get(editorKey.value)
  selected.value = draft?.provider || (item ? { ...clone(item), config: clone(item.config || {}) } : { ...newMeta(), name: serviceName(adapter), adapter, base_url: '', config: {} })
  saved.value = draft?.baseline || configuration(selected.value)
  credentials.value = draft?.credentials || {}; query.value = ''; result.value = undefined; adding.value = false
}
async function save() {
  if (!selected.value) return
  const snapshot = clone(selected.value)
  const editing = selected.value
  const updates = clone(secretUpdates.value)
  const resource = editorKey.value
  const desired = { name: snapshot.name.trim(), adapter: snapshot.adapter, base_url: (snapshot.base_url || '').trim(), config: configValues(snapshot.config) }
  let beforeCredential: string | undefined
  const accept = async (value: AstralInternalDomainSearchProvider) => {
    const changes = { name: editing.name, adapter: editing.adapter, base_url: editing.base_url, config: clone(editing.config) }
    Object.assign(editing, value, { config: value.config || {} })
    for (const field of ['name', 'adapter', 'base_url', 'config'] as const) {
      if (JSON.stringify(changes[field]) !== JSON.stringify(snapshot[field])) Object.assign(editing, { [field]: changes[field] })
    }
    const baseline = configuration(value)
    const active = editorKey.value === resource && selected.value === editing
    const remaining = { ...(active ? credentials.value : drafts.get(resource)?.credentials ?? updates) }
    if (editing.adapter === snapshot.adapter) {
      for (const [name, value] of Object.entries(updates)) if (remaining[name] === value) delete remaining[name]
    }
    const entry = { provider: editing, credentials: remaining, baseline }
    drafts.set(value.id, entry)
    if (!snapshot.id) drafts.delete(resource)
    if (active) {
      credentials.value = remaining
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
      const value = await request(saveSearchProvider({ body: { ...(current || newMeta()), ...desired, ...(Object.keys(updates).length ? { credentials: updates } : {}) } }))
      await accept(value)
    },
    reconcile: async () => {
      if (!snapshot.id) return 'unknown'
      const current = (await request(listSearchProviders())).find(p => p.id === snapshot.id)
      const matches = current && configuration(current) === configuration({ ...snapshot, ...desired })
      if (matches && (!Object.keys(updates).length || current.credential_id !== beforeCredential)) { await accept(current); return 'applied' }
      return Object.keys(updates).length && matches ? 'unknown' : 'not-applied'
    },
  })
}
async function test() {
  if (!selected.value?.id || dirty.value) return
  const editing = selected.value
  const submittedQuery = query.value.trim()
  const testedRevision = editing.revision
  await action.run(async () => {
    const value = await request(testSearchProvider({ path: { id: editing.id }, body: { query: submittedQuery } }))
    if (selected.value === editing && editing.revision === testedRevision && !dirty.value && query.value.trim() === submittedQuery) result.value = value
  })
}
async function remove() {
  await action.run(async () => {
    if (!selected.value) return
    await request(deleteSearchProvider({ path: { id: selected.value.id }, query: { revision: selected.value.revision } }))
    drafts.delete(selected.value.id)
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
          <span class="resource-symbol"><Globe /></span><span><strong>{{ item.name }}</strong><small>{{ serviceName(item.adapter) }} · {{ usage(item.id) }}</small></span><ChevronRight />
        </button>
      </div>
      <p
        v-else-if="catalog.isLoading.value"
        class="muted"
      >
        {{ t('loading') }}
      </p>
      <p
        v-else-if="catalog.error.value"
        class="error-box"
        role="alert"
      >
        {{ t('searchCatalogFailed') }}
      </p>
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
          <span class="resource-symbol"><Globe /></span><span class="resource-card-copy"><strong>{{ service.name }}</strong><small>{{ serviceHost(service.website) }}</small></span><Plus />
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
            :model-value="selected.adapter"
            :label="t('searchServices')"
            :options="services.map(service => ({ value: service.id, label: service.name }))"
            @update:model-value="changeAdapter"
          />
        </SettingsRow>
        <p
          v-if="te(`searchNotice_${selected.adapter}`)"
          class="settings-context-note"
        >
          {{ t(`searchNotice_${selected.adapter}`) }}
        </p>
        <SettingsRow
          v-for="field in activeService?.fields || []"
          :key="`${selected.adapter}:${field.name}`"
          :label="fieldLabel(field)"
          :description="!field.required && field.secret ? t('searchOptionalCredential') : undefined"
        >
          <input
            v-if="field.secret"
            v-model="credentials[field.name]"
            :aria-label="fieldLabel(field)"
            type="password"
            autocomplete="new-password"
            :required="field.required && !hasStoredSecret(field.name)"
            :placeholder="hasStoredSecret(field.name) ? t('storedSecret') : ''"
          >
          <Select
            v-else-if="field.options?.length"
            :model-value="selected.config[field.name] || field.default || ''"
            :label="fieldLabel(field)"
            :options="field.options.map(value => ({ value, label: te(`searchOption_${value}`) ? t(`searchOption_${value}`) : value }))"
            @update:model-value="selected.config[field.name] = $event"
          />
          <input
            v-else
            v-model="selected.config[field.name]"
            :aria-label="fieldLabel(field)"
            :required="field.required"
            :placeholder="field.default || ''"
          >
        </SettingsRow>
        <SettingsRow
          :label="t('endpoint')"
          :description="activeService?.base_url_required ? t('searchInstanceHint') : t('officialEndpointHint')"
        >
          <input
            v-model="selected.base_url"
            :aria-label="t('endpoint')"
            type="url"
            :required="activeService?.base_url_required"
            :placeholder="activeService?.base_url || 'https://search.example.com'"
          >
        </SettingsRow>
        <div class="settings-submit-row">
          <p>{{ t('connectionDraftHint') }}</p><Button
            type="submit"
            variant="primary"
            :busy="saves.state(`search:${editorKey}/config`).status === 'saving'"
            :disabled="!activeService || !!selected.id && !dirty"
          >
            {{ t('saveConnectionSettings') }}
          </Button>
        </div>
        <SettingsFeedback :scope="`search:${editorKey}/config`" />
      </SettingsSection>
      <template v-if="selected.id">
        <SettingsSection
          :title="t('testSearch')"
          :description="dirty ? t('connectionDraftPending') : undefined"
        >
          <SettingsRow :label="t('testQuery')">
            <div class="row search-test-input">
              <input
                v-model="query"
                :aria-label="t('testQuery')"
              ><Button
                :busy="action.busy.value"
                :disabled="!query.trim() || dirty"
                @click="test"
              >
                {{ t('testSearch') }}
              </Button>
            </div>
          </SettingsRow>
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
      <div class="resource-list">
        <button
          v-for="service in services"
          :key="service.id"
          class="resource-row"
          @click="edit(undefined, service.id)"
        >
          <span class="resource-symbol"><Globe /></span><span><strong>{{ service.name }}</strong><small>{{ serviceHost(service.website) }}</small></span><ChevronRight />
        </button>
      </div>
    </Modal>
  </div>
</template>

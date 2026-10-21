<script setup lang="ts">
import { computed, nextTick, onDeactivated, onUnmounted, ref, watch } from 'vue'
import { useQuery } from '@pinia/colada'
import { listModelsQuery, listProvidersQuery } from '@astral/sdk/colada'
import { authorizeProvider, deleteModel, deleteProvider, importModels, listProviderPresets, modelCatalog, presetModels, providerAuthorization, saveModel, saveProvider, type AstralInternalModelbridgeAuthorization as Authorization, type AstralInternalModelbridgeDiscovery as Discovery, type AstralInternalModelbridgeProviderPreset as Preset } from '@astral/sdk'
import { Button, Modal, SettingsSection, Switch } from '@astral/ui'
import { ArrowLeft, Brain, ChevronRight, Copy, Eye, Pencil, Plus, ProviderIcon, RefreshCw, Search, Trash2, Wrench } from '@astral/icons'
import { useI18n } from 'vue-i18n'
import type { Profile, Provider } from '../../lib/types'
import { clone, errorText, object, request } from '../../lib/api'
import { useAction } from '../../composables/useAction'
import { useAppStore } from '../../stores/app'
import ModelProfileEditor from './ModelProfileEditor.vue'
import ProviderForm from './ProviderForm.vue'
import ProviderPresetPicker from './ProviderPresetPicker.vue'
import ResourceDelete from './ResourceDelete.vue'
const { t } = useI18n()
const app = useAppStore()
const action = useAction()
const providers = useQuery(listProvidersQuery())
const profiles = useQuery(listModelsQuery())
const presets = useQuery({ key: ['provider-presets'], query: () => request(listProviderPresets()) })
const search = ref('')
const detail = ref(false)
const providerID = ref('')
const presetID = ref('')
const root = ref<HTMLElement>()
watch([detail, providerID, presetID], async () => { await nextTick(); root.value?.closest<HTMLElement>('.settings-main')?.scrollTo({ top: 0 }) })
const provider = computed(() => providers.data.value?.find(p => p.id === providerID.value))
const preset = computed(() => presets.data.value?.find(p => p.id === (provider.value?.preset_id || presetID.value)))
const cards = computed(() => {
  const saved = (providers.data.value || []).map(p => ({ id: p.id, name: p.name, base_url: p.base_url, provider: p, preset: presets.data.value?.find(s => s.id === p.preset_id) }))
  const templates = (presets.data.value || []).filter(s => !saved.some(p => p.provider.preset_id === s.id)).map(s => ({ id: s.id, name: s.name, base_url: s.base_url, provider: undefined, preset: s }))
  return [...saved, ...templates].filter(p => `${p.name} ${p.base_url} ${p.preset?.id || ''}`.toLowerCase().includes(search.value.trim().toLowerCase()))
})
const addOpen = ref(false)
const addPresetID = ref('openai')
const addPreset = computed(() => presets.data.value?.find(p => p.id === addPresetID.value))
const suggestions = ref<Discovery[]>([])
const catalogLoading = ref(false)
const catalogError = ref('')
let catalogEpoch = 0
const modelSearch = ref('')
const modelPage = ref(1)
const localModels = computed(() => profiles.data.value?.filter(p => p.provider_id === providerID.value) || [])
const modelRows = computed(() => {
  const rows = localModels.value.length ? localModels.value.map(profile => ({ id: profile.model_id, name: profile.name, values: { ...profile.recommendations, ...profile.overrides }, profile, suggestion: undefined as Discovery | undefined })) : suggestions.value.map(suggestion => ({ id: suggestion.id, name: suggestion.name, values: suggestion.recommendations, profile: undefined as Profile | undefined, suggestion }))
  return rows.filter(p => `${p.id} ${p.name}`.toLowerCase().includes(modelSearch.value.trim().toLowerCase()))
})
const pageCount = computed(() => Math.max(1, Math.ceil(modelRows.value.length / 8)))
const visibleModels = computed(() => modelRows.value.slice((modelPage.value - 1) * 8, modelPage.value * 8))
watch([modelSearch, providerID, presetID], () => { modelPage.value = 1 })
watch(pageCount, count => { modelPage.value = Math.min(modelPage.value, count) })
watch(() => [preset.value?.id, providerID.value], async () => {
  const epoch = ++catalogEpoch
  suggestions.value = []; catalogError.value = ''; catalogLoading.value = true
  try {
    const items = preset.value ? await request(presetModels({ path: { id: preset.value.id } })) : []
    if (epoch === catalogEpoch) suggestions.value = items
  } catch (error) { if (epoch === catalogEpoch) catalogError.value = errorText(error) }
  finally { if (epoch === catalogEpoch) catalogLoading.value = false }
})
function openProvider(value?: Provider, template?: Preset) {
  providerID.value = value?.id || ''; presetID.value = template?.id || value?.preset_id || ''; detail.value = true
  modelSearch.value = ''; action.error.value = ''; action.success.value = ''; stopAuthorization()
}
function host(url: string) { return url.replace(/^https?:\/\//, '').replace(/\/$/, '') }
function contextLabel(value: unknown) { const n = Number(value); return n > 0 ? n >= 1000000 ? `${Math.round(n / 10000) / 100}M` : `${Math.round(n / 1000)}K` : '' }
async function savedProvider(saved: Provider, autoImport: boolean) {
  addOpen.value = false
  await app.refresh()
  openProvider(saved, presets.data.value?.find(p => p.id === saved.preset_id))
  action.success.value = t('configurationSaved')
  if (autoImport) await importCatalog(true)
}
async function importCatalog(refresh: boolean) {
  await action.run(async () => {
    if (!provider.value) return
    const result = await request(importModels({ path: { id: provider.value.id }, body: { refresh } }))
    await app.refresh()
    action.success.value = t('modelsImported', { count: result.imported.length })
    if (result.catalog_error) action.success.value += ` · ${t('usingBuiltInCatalog')}`
    if (result.skipped.length && !result.imported.length && !localModels.value.length) action.success.value = t('catalogNeedsLimits')
  })
}
async function fetchCatalog() {
  await action.run(async () => {
    if (!provider.value) return
    suggestions.value = await request(modelCatalog({ path: { id: provider.value.id } }))
  })
}
async function toggleProvider(enabled: boolean) {
  await action.run(async () => { if (provider.value) { await request(saveProvider({ body: { ...provider.value, enabled } })); await app.refresh() } })
}
async function toggleModel(p: Profile, enabled: boolean) {
  await action.run(async () => { await request(saveModel({ body: { ...clone(p), enabled } })); await app.refresh() })
}
const modelOpen = ref(false)
const editModel = ref<Profile>()
const newSuggestion = ref<Discovery>()
function openModel(p?: Profile, suggestion?: Discovery) { editModel.value = p; newSuggestion.value = suggestion; modelOpen.value = true }
async function modelSaved() { await app.refresh(); action.success.value = t('saved') }
const deleteTarget = ref<{ kind: 'providers' | 'models'; id: string; name: string; revision: number }>()
async function remove() {
  await action.run(async () => {
    const target = deleteTarget.value
    if (!target) return
    await request(target.kind === 'models' ? deleteModel({ path: { id: target.id }, query: { revision: target.revision } }) : deleteProvider({ path: { id: target.id }, query: { revision: target.revision } }))
    deleteTarget.value = undefined
    if (target.kind === 'providers') { detail.value = false; providerID.value = '' }
    await app.refresh()
  })
}
const authorization = ref<Authorization>()
let authTimer: ReturnType<typeof setTimeout> | undefined
let authEpoch = 0
function stopAuthorization() { clearTimeout(authTimer); authorization.value = undefined; authEpoch++ }
onDeactivated(stopAuthorization); onUnmounted(stopAuthorization)
async function startAuthorization() {
  await action.run(async () => {
    if (!provider.value) return
    stopAuthorization()
    authorization.value = await request(authorizeProvider({ path: { id: provider.value.id } }))
    scheduleAuthorization(authEpoch)
  })
}
function scheduleAuthorization(epoch: number) {
  authTimer = setTimeout(async () => {
    const flow = authorization.value
    if (!flow || epoch !== authEpoch) return
    try {
      const next = await request(providerAuthorization({ path: { id: flow.id } }))
      if (epoch !== authEpoch) return
      authorization.value = next
      if (next.status === 'connected') { await app.refresh(); await importCatalog(true) }
      else if (next.status === 'pending') scheduleAuthorization(epoch)
    } catch (error) { if (epoch === authEpoch) action.error.value = errorText(error) }
  }, Math.max(5, authorization.value?.interval || 5) * 1000)
}
async function copyCode() { if (authorization.value?.user_code) await action.run(() => navigator.clipboard.writeText(authorization.value!.user_code!)) }
</script>
<template>
  <div
    ref="root"
    class="stack provider-settings"
  >
    <p
      v-if="action.error.value"
      class="error-box"
      role="alert"
    >
      {{ action.error.value }}
    </p>
    <p
      v-if="action.success.value"
      class="success-note"
      role="status"
    >
      {{ action.success.value }}
    </p>
    <template v-if="!detail">
      <div class="settings-title">
        <h1>{{ t('providerPageTitle') }}</h1>
      </div>
      <div class="settings-toolbar">
        <div class="search-input">
          <Search /><input
            v-model="search"
            :placeholder="t('providerSearch')"
            :aria-label="t('providerSearch')"
          >
        </div><Button
          size="small"
          @click="addOpen = true"
        >
          <Plus />{{ t('add') }}
        </Button>
      </div>
      <p
        v-if="presets.error.value"
        class="error-box"
      >
        {{ errorText(presets.error.value) }}
      </p>
      <div class="resource-grid provider-grid">
        <button
          v-for="card in cards"
          :key="card.id"
          class="resource-card provider-card"
          @click="openProvider(card.provider, card.preset)"
        >
          <span class="provider-icon-slot"><ProviderIcon :name="card.preset?.icon" /></span>
          <span class="resource-card-copy"><strong>{{ card.name }}</strong><small>{{ host(card.base_url) || t('customEndpoint') }}</small></span>
          <span
            v-if="card.provider"
            class="provider-count"
            :class="{ disabled: card.provider.enabled === false }"
          >{{ profiles.data.value?.filter(m => m.provider_id === card.provider?.id).length || 0 }}</span><ChevronRight />
        </button>
      </div>
      <p
        v-if="!cards.length"
        class="muted"
      >
        {{ presets.asyncStatus.value === 'loading' ? t('loading') : t('providerNoMatch') }}
      </p>
    </template>
    <template v-else>
      <button
        class="provider-back"
        @click="detail = false; stopAuthorization(); action.error.value = ''; action.success.value = ''"
      >
        <ArrowLeft />{{ t('allProviders') }}
      </button>
      <div class="provider-detail-heading">
        <span class="provider-icon-slot"><ProviderIcon :name="preset?.icon" /></span><h2>{{ provider?.name || preset?.name || t('customProvider') }}</h2>
        <Button
          v-if="provider"
          variant="ghost"
          size="icon"
          :aria-label="t('deleteProvider')"
          @click="deleteTarget = { kind: 'providers', ...provider }"
        >
          <Trash2 />
        </Button>
        <Switch
          :model-value="!!provider && provider.enabled !== false"
          :disabled="!provider || action.busy.value"
          :label="t('enableProvider')"
          @update:model-value="toggleProvider"
        />
      </div>
      <ProviderForm
        :provider="provider"
        :preset="preset"
        @saved="savedProvider"
      />
      <SettingsSection
        v-if="preset?.auth === 'oauth' && provider"
        :title="t('accountAuthorization')"
      >
        <div class="authorization-panel">
          <p class="muted small">
            {{ provider.credential_id ? t('accountConnected') : t('authorizationHint') }}
          </p>
          <template v-if="authorization?.status === 'pending'">
            <div class="authorization-code">
              <code>{{ authorization.user_code }}</code><Button
                size="icon"
                :aria-label="t('copy')"
                @click="copyCode"
              >
                <Copy />
              </Button>
            </div>
            <a
              :href="authorization.verification_url"
              target="_blank"
              rel="noopener noreferrer"
              class="button button-primary"
            >{{ t('openAuthorization') }}</a><p class="muted small">
              {{ t('waitingAuthorization') }}
            </p>
          </template>
          <template v-else>
            <p
              v-if="authorization?.status === 'expired'"
              class="error-box"
            >
              {{ t('authorizationExpired') }}
            </p><Button
              :busy="action.busy.value"
              @click="startAuthorization"
            >
              {{ provider.credential_id ? t('reconnectAccount') : t('connectAccount') }}
            </Button>
          </template>
        </div>
      </SettingsSection>
      <SettingsSection
        :title="t('providerModels')"
        class="provider-models"
      >
        <div class="models-toolbar">
          <div class="search-input">
            <Search /><input
              v-model="modelSearch"
              :placeholder="t('searchModels')"
              :aria-label="t('searchModels')"
            >
          </div>
          <Button
            v-if="provider"
            size="small"
            :busy="action.busy.value"
            @click="importCatalog(true)"
          >
            <RefreshCw />{{ t('refreshModels') }}
          </Button>
          <Button
            v-if="provider"
            variant="primary"
            size="small"
            @click="openModel()"
          >
            {{ t('addModel') }}
          </Button>
        </div>
        <p
          v-if="catalogError"
          class="error-box"
        >
          {{ catalogError }}
        </p>
        <div
          v-for="row in visibleModels"
          :key="row.profile?.id || row.id"
          class="provider-model-row"
          :class="{ preview: !provider }"
        >
          <button
            v-if="provider"
            class="model-row-identity"
            :aria-label="t('editModelNamed', { name: row.name })"
            @click="openModel(row.profile, row.suggestion)"
          >
            <strong>{{ row.name }}</strong><small>{{ row.id }}</small>
          </button>
          <div
            v-else
            class="model-row-identity"
          >
            <strong>{{ row.name }}</strong><small>{{ row.id }}</small>
          </div>
          <div class="model-row-capabilities">
            <span
              class="model-context"
              :title="t('contextWindow')"
            >{{ contextLabel(row.values.context_window) }}</span>
            <span :title="row.values.images === 'supported' ? t('vision') : undefined"><Eye
              v-if="row.values.images === 'supported'"
              :aria-label="t('vision')"
            /></span>
            <span :title="row.values.tool_calling === 'supported' ? t('toolCalling') : undefined"><Wrench
              v-if="row.values.tool_calling === 'supported'"
              :aria-label="t('toolCalling')"
            /></span>
            <span :title="Object.keys(object(row.values.reasoning)).length ? t('reasoning') : undefined"><Brain
              v-if="Object.keys(object(row.values.reasoning)).length"
              :aria-label="t('reasoning')"
            /></span>
          </div>
          <div
            v-if="row.profile"
            class="model-row-actions"
          >
            <Switch
              :model-value="row.profile.enabled !== false"
              :label="t('enableModel', { name: row.name })"
              :disabled="action.busy.value"
              @update:model-value="toggleModel(row.profile!, $event)"
            />
            <Button
              variant="ghost"
              size="icon"
              :aria-label="t('editModelNamed', { name: row.name })"
              @click="openModel(row.profile)"
            >
              <Pencil />
            </Button>
            <Button
              variant="ghost"
              size="icon"
              :aria-label="t('deleteModelNamed', { name: row.name })"
              @click="deleteTarget = { kind: 'models', ...row.profile }"
            >
              <Trash2 />
            </Button>
          </div>
          <Button
            v-else-if="provider"
            variant="ghost"
            size="icon"
            class="model-row-add"
            :aria-label="t('addModelNamed', { name: row.name })"
            @click="openModel(undefined, row.suggestion)"
          >
            <Plus />
          </Button>
        </div>
        <p
          v-if="!modelRows.length"
          class="model-empty muted"
        >
          {{ catalogLoading ? t('loading') : modelSearch ? t('modelNoMatch') : t('noModels') }}
        </p>
        <div
          v-if="pageCount > 1"
          class="model-pagination"
        >
          <span>{{ t('modelPage', { page: modelPage, pages: pageCount, count: modelRows.length }) }}</span><Button
            size="icon"
            variant="ghost"
            :disabled="modelPage <= 1"
            :aria-label="t('previousPage')"
            @click="modelPage--"
          >
            <ArrowLeft />
          </Button><Button
            size="icon"
            variant="ghost"
            :disabled="modelPage >= pageCount"
            :aria-label="t('nextPage')"
            @click="modelPage++"
          >
            <ChevronRight />
          </Button>
        </div>
        <p
          v-if="!localModels.length && suggestions.length"
          class="model-catalog-note muted small"
        >
          {{ provider ? t('catalogPreviewHint') : t('saveProviderToUseModels') }}
        </p>
        <Button
          v-if="provider && !suggestions.length && !localModels.length"
          size="small"
          :busy="action.busy.value"
          @click="fetchCatalog"
        >
          {{ t('catalog') }}
        </Button>
      </SettingsSection>
    </template>
    <Modal
      v-model:open="addOpen"
      :title="t('addProvider')"
      class="provider-add-modal"
    >
      <label class="label">{{ t('chooseProvider') }}<ProviderPresetPicker
        v-model="addPresetID"
        :presets="presets.data.value || []"
      /></label><ProviderForm
        :preset="addPreset"
        compact
        @saved="savedProvider"
        @cancel="addOpen = false"
      />
    </Modal>
    <ModelProfileEditor
      v-model:open="modelOpen"
      :provider="provider"
      :profile="editModel"
      :suggestion="newSuggestion"
      :catalog="suggestions"
      @saved="modelSaved"
    />
    <ResourceDelete
      :id="deleteTarget?.id || ''"
      :open="!!deleteTarget"
      :kind="deleteTarget?.kind || ''"
      :name="deleteTarget?.name || ''"
      :busy="action.busy.value"
      @update:open="value => { if (!value) deleteTarget = undefined }"
      @confirm="remove"
    />
  </div>
</template>

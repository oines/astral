<script setup lang="ts">
import { computed, ref } from 'vue'
import { useQuery } from '@pinia/colada'
import { listModelsQuery, listProvidersQuery } from '@astral/sdk/colada'
import { deleteModel, deleteProvider, modelCatalog, refreshModel, saveModel, verifyModel, type AstralInternalModelbridgeDiscovery } from '@astral/sdk'
import { Button } from '@astral/ui'
import { ArrowLeft, ChevronRight, Plus, SlidersHorizontal, Server } from '@astral/icons'
import { useI18n } from 'vue-i18n'
import type { Profile, Provider } from '../../lib/types'
import { clone, newMeta, object, request } from '../../lib/api'
import { useAction } from '../../composables/useAction'
import { useAppStore } from '../../stores/app'
import ProviderForm from './ProviderForm.vue'
import ResourceDelete from './ResourceDelete.vue'
const { t } = useI18n()
const app = useAppStore()
const action = useAction()
const providers = useQuery(listProvidersQuery())
const profiles = useQuery(listModelsQuery())
const page = ref<'list' | 'provider' | 'profile'>('list')
const provider = ref<Provider>()
const profile = ref<Profile>()
const catalog = ref<AstralInternalModelbridgeDiscovery[]>([])
const catalogSelection = ref('')
const defaultsJSON = ref('{}')
const reasoningJSON = ref('{}')
const savedForm = ref('')
const formValue = computed(() => JSON.stringify([profile.value, defaultsJSON.value, reasoningJSON.value]))
const dirty = computed(() => savedForm.value !== formValue.value)
const deleteTarget = ref<{ kind: 'providers' | 'models'; id: string; name: string; revision: number }>()
const effective = computed(() => ({ ...profile.value?.recommendations, ...profile.value?.overrides }))
function newProvider() { provider.value = undefined; page.value = 'provider' }
function editProfile(value?: Profile) {
  profile.value = value ? clone(value) : { ...newMeta(), name: '', model_id: '', provider_id: providers.data.value?.[0]?.id || '', recommendations: {}, overrides: {} }
  defaultsJSON.value = JSON.stringify(effective.value.defaults || {}, null, 2)
  reasoningJSON.value = JSON.stringify(effective.value.reasoning || {}, null, 2)
  catalog.value = []
  catalogSelection.value = ''
  savedForm.value = formValue.value
  page.value = 'profile'
}
function override(key: string, value: unknown) { if (profile.value) profile.value.overrides[key] = value }
function reset(key: string) { if (profile.value) delete profile.value.overrides[key] }
async function savedProvider() { await app.refresh(); page.value = 'list' }
async function fetchCatalog() {
  await action.run(async () => {
    if (profile.value) catalog.value = await request(modelCatalog({ path: { id: profile.value.provider_id } }))
    catalogSelection.value = ''
  })
}
function chooseModel(id: string) {
  const match = catalog.value.find(x => x.id === id)
  if (!match || !profile.value) return
  profile.value.model_id = id
  if (!profile.value.name) profile.value.name = match.name
  profile.value.recommendations = match.recommendations
  defaultsJSON.value = JSON.stringify(effective.value.defaults || {}, null, 2)
  reasoningJSON.value = JSON.stringify(effective.value.reasoning || {}, null, 2)
}
async function saveProfile() {
  await action.run(async () => {
    if (!profile.value) return
    const copy = clone(profile.value)
    const defaults: unknown = JSON.parse(defaultsJSON.value)
    const reasoning: unknown = JSON.parse(reasoningJSON.value)
    if (!defaults || typeof defaults !== 'object' || Array.isArray(defaults)) throw new Error('Model defaults must be a JSON object.')
    if (!reasoning || typeof reasoning !== 'object' || Array.isArray(reasoning)) throw new Error('Reasoning capabilities must be a JSON object.')
    if (JSON.stringify(defaults) !== JSON.stringify(object(effective.value.defaults))) copy.overrides.defaults = defaults
    if (JSON.stringify(reasoning) !== JSON.stringify(object(effective.value.reasoning))) copy.overrides.reasoning = reasoning
    profile.value = await request(saveModel({ body: copy }))
    savedForm.value = formValue.value
    await app.refresh()
  }, t('saved'))
}
async function verify() {
  await action.run(async () => {
    if (!profile.value?.id) return
    const id = profile.value.id
    let failure: unknown
    try { await request(verifyModel({ path: { id } })) } catch (e) { failure = e }
    await app.refresh()
    const fresh = profiles.data.value?.find(p => p.id === id)
    if (fresh) profile.value = clone(fresh)
    savedForm.value = formValue.value
    if (failure) throw failure
  })
}
async function refresh() {
  await action.run(async () => {
    if (!profile.value?.id) return
    profile.value = await request(refreshModel({ path: { id: profile.value.id } }))
    defaultsJSON.value = JSON.stringify(effective.value.defaults || {}, null, 2)
    reasoningJSON.value = JSON.stringify(effective.value.reasoning || {}, null, 2)
    savedForm.value = formValue.value
    await app.refresh()
  }, t('saved'))
}
async function remove() {
  await action.run(async () => {
    const target = deleteTarget.value
    if (!target) return
    await request(target.kind === 'models' ? deleteModel({ path: { id: target.id }, query: { revision: target.revision } }) : deleteProvider({ path: { id: target.id }, query: { revision: target.revision } }))
    deleteTarget.value = undefined
    page.value = 'list'
    await app.refresh()
  })
}
const numericFields = [{ key: 'context_window', label: 'contextWindow', required: true }, { key: 'max_output_tokens', label: 'maxOutput', required: true }, { key: 'max_input_tokens', label: 'maxInput', required: false }, { key: 'image_token_budget', label: 'imageBudget', required: false }, { key: 'input_price', label: 'inputPrice', required: false }, { key: 'output_price', label: 'outputPrice', required: false }]
</script>
<template>
  <div class="stack">
    <p
      v-if="action.error.value"
      class="error-box"
    >
      {{ action.error.value }}
    </p><p
      v-if="action.success.value"
      class="success-note"
    >
      {{ action.success.value }}
    </p>
    <template v-if="page === 'list'">
      <p class="muted">
        {{ t('modelsHint') }}
      </p>
      <div class="row between">
        <h2>{{ t('providers') }}</h2><Button
          size="small"
          @click="newProvider"
        >
          <Plus />{{ t('add') }}
        </Button>
      </div>
      <div class="resource-list">
        <button
          v-for="p in providers.data.value"
          :key="p.id"
          class="resource-row"
          @click="provider = p; page = 'provider'"
        >
          <span class="resource-symbol"><Server /></span><span><strong>{{ p.name }}</strong><small>{{ p.dialect }} · {{ p.protocol === 'openai-responses' ? 'Responses' : 'Chat Completions' }}</small></span><ChevronRight />
        </button>
        <p
          v-if="!providers.data.value?.length"
          class="empty"
        >
          {{ t('empty') }}
        </p>
      </div>
      <div class="row between">
        <h2>{{ t('profiles') }}</h2><Button
          size="small"
          :disabled="!providers.data.value?.length"
          @click="editProfile()"
        >
          <Plus />{{ t('add') }}
        </Button>
      </div>
      <div class="resource-list">
        <button
          v-for="p in profiles.data.value"
          :key="p.id"
          class="resource-row"
          @click="editProfile(p)"
        >
          <span class="resource-symbol"><SlidersHorizontal /></span><span><strong>{{ p.name }}</strong><small>{{ p.model_id }} · {{ providers.data.value?.find(v => v.id === p.provider_id)?.name || t('missing') }}</small></span><span
            class="badge"
            :class="{ good: p.verification?.tool_calling === 'supported' }"
          >{{ p.verification?.tool_calling || 'Not verified' }}</span><ChevronRight />
        </button>
        <p
          v-if="!profiles.data.value?.length"
          class="empty"
        >
          {{ t('empty') }}
        </p>
      </div>
    </template>
    <template v-else-if="page === 'provider'">
      <div class="row between">
        <Button
          variant="ghost"
          size="small"
          @click="page = 'list'"
        >
          <ArrowLeft />{{ t('providers') }}
        </Button><Button
          v-if="provider"
          variant="ghost"
          size="small"
          @click="deleteTarget = { kind: 'providers', ...provider }"
        >
          {{ t('delete') }}
        </Button>
      </div>
      <h2>{{ provider?.name || t('add') + ' · ' + t('providers') }}</h2><ProviderForm
        :provider="provider"
        @saved="savedProvider"
        @cancel="page = 'list'"
      />
    </template>
    <form
      v-else-if="profile"
      class="stack"
      @submit.prevent="saveProfile"
    >
      <div class="row between">
        <Button
          variant="ghost"
          size="small"
          @click="page = 'list'"
        >
          <ArrowLeft />{{ t('profiles') }}
        </Button><Button
          v-if="profile.id"
          variant="ghost"
          size="small"
          @click="deleteTarget = { kind: 'models', ...profile }"
        >
          {{ t('delete') }}
        </Button>
      </div>
      <div class="field-grid">
        <label class="label">{{ t('name') }}<input
          v-model="profile.name"
          required
        ></label><label class="label">{{ t('providers') }}<select
          v-model="profile.provider_id"
          required
        ><option
          v-for="p in providers.data.value"
          :key="p.id"
          :value="p.id"
        >{{ p.name }}</option></select></label>
      </div>
      <div class="row end">
        <label class="label grow">{{ t('modelID') }}<input
          v-model="profile.model_id"
          list="model-catalog"
          required
          @change="chooseModel(profile.model_id)"
        ><datalist id="model-catalog"><option
          v-for="item in catalog"
          :key="item.id"
          :value="item.id"
        >{{ item.name }}</option></datalist></label><Button
          :busy="action.busy.value"
          @click="fetchCatalog"
        >
          {{ t('loadCatalog') }}
        </Button>
      </div>
      <div class="field-grid">
        <label
          v-if="catalog.length"
          class="label"
        >
          {{ t('availableModels') }} · {{ catalog.length }}
          <select
            v-model="catalogSelection"
            @change="chooseModel(catalogSelection)"
          >
            <option
              value=""
              disabled
            >{{ t('select') }}</option>
            <option
              v-for="item in catalog"
              :key="item.id"
              :value="item.id"
            >{{ item.name }} · {{ item.id }}</option>
          </select>
        </label>
        <label
          v-for="field in numericFields"
          :key="field.key"
          class="label"
        >
          <span class="row between">{{ t(field.label) }}<span
            v-if="field.key in profile.overrides"
            class="badge"
          >{{ t('overrides') }}</span></span>
          <input
            type="number"
            :min="field.required ? 1 : 0"
            step="any"
            :required="field.required"
            :value="effective[field.key] ?? ''"
            @input="($event.target as HTMLInputElement).value === '' ? reset(field.key) : override(field.key, Number(($event.target as HTMLInputElement).value))"
          >
          <span
            v-if="field.key in profile.recommendations"
            class="row between muted small"
          >{{ t('recommendations') }}: {{ profile.recommendations[field.key] }}<button
            v-if="field.key in profile.overrides"
            type="button"
            class="text-button"
            @click="reset(field.key)"
          >{{ t('reset') }}</button></span>
        </label>
      </div>
      <div class="field-grid">
        <label
          v-for="capability in ['tool_calling', 'images']"
          :key="capability"
          class="label"
        >{{ t(capability === 'images' ? 'imageSupport' : 'toolCalling') }}<select
          :value="effective[capability] || 'unknown'"
          @change="override(capability, ($event.target as HTMLSelectElement).value)"
        ><option value="unknown">{{ t('unverified') }}</option><option value="supported">{{ t('supported') }}</option><option value="unsupported">{{ t('unsupported') }}</option></select><button
          v-if="capability in profile.overrides"
          type="button"
          class="text-button"
          @click="reset(capability)"
        >{{ t('reset') }}</button></label>
      </div>
      <label class="preference-row"><span><strong>{{ t('streaming') }}</strong><small>{{ t('streamingHint') }}</small></span><select
        :value="effective.streaming === false ? 'false' : effective.streaming === true ? 'true' : 'default'"
        @change="($event.target as HTMLSelectElement).value === 'default' ? reset('streaming') : override('streaming', ($event.target as HTMLSelectElement).value === 'true')"
      ><option value="default">{{ t('default') }}</option><option value="true">{{ t('on') }}</option><option value="false">{{ t('off') }}</option></select></label>
      <details class="form-disclosure">
        <summary>{{ t('generationParameters') }}</summary><div class="stack">
          <label class="label">{{ t('reasoningCapabilities') }}<textarea
            v-model="reasoningJSON"
            class="mono"
            rows="5"
          /><span class="muted small">{{ t('nativeEffortsHint') }} <code>{"efforts":["low","high"],"toggle":true,"disable":true}</code></span></label>
          <label class="label">{{ t('generationDefaults') }}<textarea
            v-model="defaultsJSON"
            class="mono"
            rows="6"
          /><span class="muted small">{{ t('generationHint') }}</span></label>
          <div class="row">
            <Button
              size="small"
              @click="reset('reasoning'); reasoningJSON = JSON.stringify(effective.reasoning || {}, null, 2)"
            >
              {{ t('reset') }} · {{ t('reasoning') }}
            </Button><Button
              size="small"
              @click="reset('defaults'); defaultsJSON = JSON.stringify(effective.defaults || {}, null, 2)"
            >
              {{ t('reset') }} · {{ t('generationDefaults') }}
            </Button>
          </div>
        </div>
      </details>
      <div
        v-if="profile.verification"
        class="notice"
      >
        <strong>{{ t('toolCalling') }}: {{ t(profile.verification.tool_calling === 'unknown' ? 'unverified' : profile.verification.tool_calling) }} · {{ t('toolReplay') }}: {{ t(profile.verification.tool_replay === 'unknown' ? 'unverified' : profile.verification.tool_replay) }}</strong><p>{{ profile.verification.message }}</p><p class="muted small">
          {{ new Date(profile.verification.tested_at).toLocaleString() }} · {{ t('modelVerificationRevision', { profile: profile.verification.profile_revision, provider: profile.verification.provider_revision }) }}
        </p>
      </div>
      <div class="row wrap">
        <Button
          v-if="profile.id"
          :busy="action.busy.value"
          :disabled="dirty"
          @click="verify"
        >
          {{ t('verify') }}
        </Button><Button
          v-if="profile.id"
          :busy="action.busy.value"
          :disabled="dirty"
          @click="refresh"
        >
          {{ t('refresh') }} · {{ t('catalog') }}
        </Button><span class="muted small">{{ t('verificationCost') }}</span>
      </div>
      <p
        v-if="profile.id && dirty"
        class="muted small"
      >
        {{ t('savedBeforeVerify') }}
      </p>
      <div class="form-footer">
        <Button @click="page = 'list'">
          {{ t('cancel') }}
        </Button><Button
          type="submit"
          variant="primary"
          :busy="action.busy.value"
        >
          {{ t('save') }}
        </Button>
      </div>
    </form>
    <ResourceDelete
      v-if="deleteTarget"
      :open="Boolean(deleteTarget)"
      v-bind="deleteTarget"
      :busy="action.busy.value"
      @update:open="v => { if (!v) deleteTarget = undefined }"
      @confirm="remove"
    />
  </div>
</template>

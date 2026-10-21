<script setup lang="ts">
import { ref } from 'vue'
import { useQuery } from '@pinia/colada'
import { listSearchProvidersQuery } from '@astral/sdk/colada'
import { deleteSearchProvider, saveSearchProvider, testSearchProvider, type AstralInternalDomainSearchProvider, type AstralInternalSearchResponse } from '@astral/sdk'
import { Button, Select, SettingsSection, SettingsRow } from '@astral/ui'
import { ArrowLeft, ChevronRight, Globe, Plus } from '@astral/icons'
import { useI18n } from 'vue-i18n'
import { clone, newMeta, request } from '../../lib/api'
import { useAction } from '../../composables/useAction'
import { useAppStore } from '../../stores/app'
import ResourceDelete from './ResourceDelete.vue'
const { t } = useI18n()
const app = useAppStore()
const action = useAction()
const list = useQuery(listSearchProvidersQuery())
const selected = ref<AstralInternalDomainSearchProvider>()
const key = ref('')
const query = ref('')
const result = ref<AstralInternalSearchResponse>()
const deleting = ref(false)
function edit(item?: AstralInternalDomainSearchProvider, adapter = 'tavily') {
  action.error.value = ''; action.success.value = ''
  selected.value = item ? clone(item) : { ...newMeta(), name: adapter === 'exa' ? 'Exa' : 'Tavily', adapter, base_url: '' }
  key.value = ''
  result.value = undefined
}
async function save() {
  await action.run(async () => {
    if (!selected.value) return
    selected.value = await request(saveSearchProvider({ body: { ...selected.value, ...(key.value ? { api_key: key.value } : {}) } }))
    key.value = ''
    await app.refresh()
  }, t('saved'))
}
async function test() {
  await action.run(async () => {
    if (selected.value) result.value = await request(testSearchProvider({ path: { id: selected.value.id }, body: { query: query.value } }))
  })
}
async function remove() {
  await action.run(async () => {
    if (!selected.value) return
    await request(deleteSearchProvider({ path: { id: selected.value.id }, query: { revision: selected.value.revision } }))
    selected.value = undefined
    deleting.value = false
    await app.refresh()
  })
}
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
      role="status"
    >
      {{ action.success.value }}
    </p>
    <template v-if="!selected">
      <div class="settings-toolbar">
        <p class="muted">
          {{ t('searchSetupHint') }}
        </p><Button
          size="small"
          @click="edit()"
        >
          <Plus />{{ t('add') }}
        </Button>
      </div>
      <div class="resource-grid">
        <button
          v-for="service in [{ id: 'tavily', name: 'Tavily', url: 'tavily.com' }, { id: 'exa', name: 'Exa', url: 'exa.ai' }]"
          :key="service.id"
          class="resource-card"
          @click="edit(list.data.value?.find(item => item.adapter === service.id), service.id)"
        >
          <span class="resource-symbol"><Globe /></span><span class="resource-card-copy"><strong>{{ service.name }}</strong><small>{{ service.url }}</small><small>{{ t(list.data.value?.some(item => item.adapter === service.id) ? 'configured' : 'notConfigured') }}</small></span><ChevronRight />
        </button>
      </div>
      <SettingsSection
        v-if="list.data.value?.length"
        :title="t('configured')"
      >
        <button
          v-for="item in list.data.value"
          :key="item.id"
          class="resource-row"
          @click="edit(item)"
        >
          <span class="resource-symbol"><Globe /></span><span><strong>{{ item.name }}</strong><small>{{ item.adapter === 'exa' ? 'Exa' : 'Tavily' }}</small></span><ChevronRight />
        </button>
      </SettingsSection>
    </template>
    <form
      v-else
      class="stack settings-form"
      @submit.prevent="save"
    >
      <div class="object-heading">
        <Button
          variant="ghost"
          size="icon"
          :aria-label="t('webSearch')"
          @click="selected = undefined"
        >
          <ArrowLeft />
        </Button><h2>{{ selected.name || t('webSearch') }}</h2>
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
            :options="[{ value: 'tavily', label: 'Tavily' }, { value: 'exa', label: 'Exa' }]"
          />
        </SettingsRow>
        <SettingsRow :label="t('apiKey')">
          <input
            v-model="key"
            :aria-label="t('apiKey')"
            type="password"
            autocomplete="new-password"
            :placeholder="selected.credential_id ? t('storedSecret') : ''"
          >
        </SettingsRow>
        <details class="form-disclosure">
          <summary>{{ t('advancedOptions') }}</summary><label class="label">{{ t('endpoint') }}<input
            v-model="selected.base_url"
            type="url"
            :placeholder="selected.adapter === 'exa' ? 'https://api.exa.ai' : 'https://api.tavily.com'"
          ><span class="muted small">{{ t('officialEndpointHint') }}</span></label>
        </details>
        <div class="form-footer">
          <Button @click="selected = undefined">
            {{ t('cancel') }}
          </Button><Button
            type="submit"
            variant="primary"
            :busy="action.busy.value"
          >
            {{ t('save') }}
          </Button>
        </div>
      </SettingsSection>
      <SettingsSection
        v-if="selected.id"
        :title="t('testSearch')"
      >
        <SettingsRow :label="t('testQuery')">
          <div class="row">
            <input
              v-model="query"
              :aria-label="t('testQuery')"
              :placeholder="t('testQuery')"
            ><Button
              :busy="action.busy.value"
              :disabled="!query.trim()"
              @click="test"
            >
              {{ t('testSearch') }}
            </Button>
          </div>
        </SettingsRow>
        <div
          v-if="result"
          class="search-results"
        >
          <p class="muted">
            {{ t('searchResults', { count: result.results?.length || 0 }) }}
          </p><a
            v-for="item in result.results"
            :key="item.url"
            :href="item.url"
            target="_blank"
            rel="noopener noreferrer"
          ><strong>{{ item.title }}</strong><p class="muted">{{ item.summary }}</p></a>
        </div>
      </SettingsSection>
      <div
        v-if="selected.id"
        class="danger-row"
      >
        <strong>{{ t('delete') }} {{ selected.name }}</strong><Button
          variant="danger"
          size="small"
          @click="deleting = true"
        >
          {{ t('delete') }}
        </Button>
      </div>
      <ResourceDelete
        :id="selected.id"
        :open="deleting"
        kind="search"
        :name="selected.name"
        :busy="action.busy.value"
        @update:open="deleting = $event"
        @confirm="remove"
      />
    </form>
  </div>
</template>

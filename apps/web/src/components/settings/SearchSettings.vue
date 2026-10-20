<script setup lang="ts">
import { ref } from 'vue'
import { useQuery } from '@pinia/colada'
import { listSearchProvidersQuery } from '@astral/sdk/colada'
import { deleteSearchProvider, saveSearchProvider, testSearchProvider, type AstralInternalDomainSearchProvider, type AstralInternalSearchResponse } from '@astral/sdk'
import { Button } from '@astral/ui'
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
function edit(item?: AstralInternalDomainSearchProvider) {
  selected.value = item ? clone(item) : { ...newMeta(), name: '', adapter: 'tavily', base_url: '' }
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
    >
      {{ action.success.value }}
    </p>
    <template v-if="!selected">
      <div class="row between">
        <p class="muted">
          {{ t('searchHint') }}
        </p><Button
          size="small"
          @click="edit()"
        >
          <Plus />{{ t('add') }}
        </Button>
      </div>
      <div class="resource-list">
        <button
          v-for="item in list.data.value"
          :key="item.id"
          class="resource-row"
          @click="edit(item)"
        >
          <span class="resource-symbol"><Globe /></span><span><strong>{{ item.name }}</strong><small>{{ item.adapter }}</small></span><ChevronRight />
        </button><p
          v-if="!list.data.value?.length"
          class="empty"
        >
          {{ t('empty') }}
        </p>
      </div>
    </template>
    <form
      v-else
      class="stack"
      @submit.prevent="save"
    >
      <div class="row between">
        <Button
          variant="ghost"
          size="small"
          @click="selected = undefined"
        >
          <ArrowLeft />{{ t('webSearch') }}
        </Button><Button
          v-if="selected.id"
          variant="ghost"
          size="small"
          @click="deleting = true"
        >
          {{ t('delete') }}
        </Button>
      </div>
      <div class="field-grid">
        <label class="label">{{ t('name') }}<input
          v-model="selected.name"
          required
        ></label><label class="label">{{ t('adapter') }}<select v-model="selected.adapter"><option value="tavily">Tavily</option><option value="exa">Exa</option></select></label>
      </div>
      <label class="label">{{ t('endpoint') }}<input
        v-model="selected.base_url"
        type="url"
        :placeholder="selected.adapter === 'exa' ? 'https://api.exa.ai' : 'https://api.tavily.com'"
      ><span class="muted small">{{ t('officialEndpointHint') }}</span></label>
      <label class="label">{{ t('apiKey') }}<input
        v-model="key"
        type="password"
        autocomplete="new-password"
        :placeholder="selected.credential_id ? t('storedSecret') : ''"
      ></label>
      <div class="form-footer">
        <Button
          type="submit"
          variant="primary"
          :busy="action.busy.value"
        >
          {{ t('save') }}
        </Button>
      </div>
      <div
        v-if="selected.id"
        class="detail-block stack"
      >
        <h2>{{ t('testSearch') }}</h2><label class="label">{{ t('search') }}<input v-model="query"></label><Button
          class="align-start"
          :busy="action.busy.value"
          :disabled="!query.trim()"
          @click="test"
        >
          {{ t('search') }}
        </Button>
        <div
          v-if="result"
          class="stack"
        >
          <p class="muted">
            {{ result.results?.length || 0 }} results
          </p><a
            v-for="item in result.results"
            :key="item.url"
            :href="item.url"
            target="_blank"
            rel="noopener noreferrer"
          ><strong>{{ item.title }}</strong><p class="muted">{{ item.summary }}</p></a>
        </div>
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

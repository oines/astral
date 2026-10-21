<script setup lang="ts">
import { computed, ref } from 'vue'
import type { AstralInternalModelbridgeProviderPreset as Preset } from '@astral/sdk'
import { Popover } from '@astral/ui'
import { Check, ChevronDown, Plus, ProviderIcon, Search } from '@astral/icons'
import { useI18n } from 'vue-i18n'
const props = defineProps<{ presets: Preset[] }>()
const value = defineModel<string>({ default: '' })
const { t } = useI18n()
const open = ref(false)
const search = ref('')
const selected = computed(() => props.presets.find(p => p.id === value.value))
const filtered = computed(() => props.presets.filter(p => `${p.name} ${p.id}`.toLowerCase().includes(search.value.trim().toLowerCase())))
function choose(id: string) { value.value = id; open.value = false; search.value = '' }
</script>
<template>
  <Popover
    v-model:open="open"
    :label="t('chooseProvider')"
  >
    <template #trigger>
      <button
        type="button"
        class="provider-picker-trigger"
        :aria-label="t('chooseProvider')"
      >
        <ProviderIcon
          v-if="selected"
          :name="selected.icon"
        /><Plus v-else />
        <span>{{ selected?.name || t('customProvider') }}</span><ChevronDown />
      </button>
    </template>
    <div class="provider-picker">
      <div class="search-input">
        <Search /><input
          v-model="search"
          :placeholder="t('providerSearch')"
          :aria-label="t('providerSearch')"
        >
      </div>
      <div class="provider-picker-list">
        <button
          v-if="!search"
          type="button"
          @click="choose('')"
        >
          <Plus /><span>{{ t('customProvider') }}</span><Check v-if="!value" />
        </button>
        <button
          v-for="p in filtered"
          :key="p.id"
          type="button"
          @click="choose(p.id)"
        >
          <ProviderIcon :name="p.icon" /><span>{{ p.name }}</span><Check v-if="value === p.id" />
        </button>
        <p
          v-if="!filtered.length && search"
          class="muted small"
        >
          {{ t('providerNoMatch') }}
        </p>
      </div>
    </div>
  </Popover>
</template>

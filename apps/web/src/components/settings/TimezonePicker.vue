<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { Check, ChevronDown } from '@astral/icons'
import { Menu, MenuFilter, MenuItemIndicator, MenuRadioGroup, MenuRadioItem } from '@astral/ui'
import { fixedOffsetSeconds, timezoneSupported } from '../../lib/timezone'

const props = defineProps<{ modelValue: string; system: string; disabled?: boolean }>()
const emit = defineEmits<{ 'update:modelValue': [value: string] }>()
const { t } = useI18n()
const open = ref(false)
const search = ref('')
const systemValue = '__astral_system_timezone__'
const supported = typeof Intl.supportedValuesOf === 'function' ? Intl.supportedValuesOf('timeZone') : []
function namedTimezone(value: string) {
  return !!value && fixedOffsetSeconds(value) === undefined && timezoneSupported(value)
}
const options = computed(() => {
  const items = [{ value: systemValue, label: t('timezoneSystem', { timezone: props.system }) }, ...[...new Set(['UTC', props.system, props.modelValue, ...supported].filter(namedTimezone))].sort().map(zone => ({ value: zone, label: zone }))]
  const query = search.value.trim().toLocaleLowerCase().replaceAll(' ', '_')
  const result = items.filter(item => item.label.toLocaleLowerCase().replaceAll(' ', '_').includes(query))
  if (query && !items.some(item => item.value.toLocaleLowerCase() === query)) {
    try {
      const canonical = new Intl.DateTimeFormat('en', { timeZone: search.value.trim() }).resolvedOptions().timeZone
      if (namedTimezone(canonical) && !result.some(item => item.value === canonical)) result.push({ value: canonical, label: canonical })
    } catch { /* Search text does not have to be a valid timezone. */ }
  }
  return result
})
const label = computed(() => props.modelValue || t('timezoneSystem', { timezone: props.system }))
watch(open, value => { if (!value) search.value = '' })
function choose(value: string) {
  emit('update:modelValue', value === systemValue ? '' : value)
  open.value = false
}
</script>
<template>
  <Menu
    v-model:open="open"
    class="model-choice-menu model-options-menu"
    :label="t('timezone')"
    align="end"
  >
    <template #trigger>
      <button
        type="button"
        class="select-trigger"
        :disabled="disabled"
        :aria-label="t('timezone')"
      >
        <span>{{ label }}</span><ChevronDown aria-hidden="true" />
      </button>
    </template>
    <div class="model-choice-search">
      <MenuFilter
        v-model="search"
        :aria-label="t('timezoneSearch')"
        :placeholder="t('timezoneSearch')"
        auto-focus
      />
    </div>
    <div class="model-choice-list">
      <MenuRadioGroup
        :model-value="modelValue || systemValue"
        @update:model-value="choose(String($event))"
      >
        <MenuRadioItem
          v-for="option in options"
          :key="option.value"
          :value="option.value"
          :text-value="option.label"
          class="model-choice-row"
          :disabled="disabled"
        >
          <span class="model-choice-name">{{ option.label }}</span><MenuItemIndicator><Check /></MenuItemIndicator>
        </MenuRadioItem>
      </MenuRadioGroup>
      <p
        v-if="!options.length"
        class="model-choice-empty"
      >
        {{ t('timezoneNoMatch') }}
      </p>
    </div>
  </Menu>
</template>

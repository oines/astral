<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { useSettingsSave } from '../../composables/useSettingsSave'

const props = defineProps<{ value: string; draftKey: string; label: string; required?: boolean; maxlength?: number; placeholder?: string }>()
const emit = defineEmits<{ commit: [value: string] }>()
const { t } = useI18n()
const saves = useSettingsSave()
const touched = ref(false)
const value = computed({ get: () => saves.drafts[props.draftKey] ?? props.value, set: value => { saves.drafts[props.draftKey] = value } })
const invalid = computed(() => touched.value && props.required && !value.value.trim())
watch(() => [props.draftKey, props.value], () => {
  if (saves.drafts[props.draftKey] === props.value) delete saves.drafts[props.draftKey]
  touched.value = false
})
function commit(event?: KeyboardEvent) {
  if (event?.isComposing) return
  touched.value = true
  if (props.required && !value.value.trim()) return
  const next = value.value.trim()
  if (next !== props.value) emit('commit', next)
}
</script>
<template>
  <div class="settings-text">
    <input
      v-model="value"
      :aria-label="label"
      :aria-invalid="invalid || undefined"
      :required="required"
      :maxlength="maxlength"
      :placeholder="placeholder"
      autocomplete="off"
      @blur="commit()"
      @keydown.enter.prevent="commit($event)"
    >
    <span
      v-if="invalid"
      class="field-error"
    >{{ t('settingsValueRequired') }}</span>
  </div>
</template>

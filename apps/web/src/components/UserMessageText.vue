<script setup lang="ts">
import { nextTick, onMounted, ref, watch } from 'vue'
import { useResizeObserver } from '@vueuse/core'
import { useI18n } from 'vue-i18n'

const MAX_COLLAPSED_HEIGHT = 320
const props = defineProps<{ text: string }>()
const emit = defineEmits<{ expand: [] }>()
const { t } = useI18n()
const body = ref<HTMLElement>()
const expanded = ref(false)
const collapsible = ref(false)

function measure() {
  const el = body.value
  if (!el) return
  collapsible.value = el.scrollHeight > MAX_COLLAPSED_HEIGHT + 36
}

function toggle() {
  emit('expand')
  expanded.value = !expanded.value
}

useResizeObserver(body, measure)
onMounted(measure)
watch(() => props.text, () => { void nextTick(measure) })
</script>
<template>
  <div
    class="user-text-shell"
    :class="{ collapsible, collapsed: collapsible && !expanded }"
  >
    <div
      ref="body"
      class="user-text"
    >
      {{ text }}
    </div>
    <button
      v-if="collapsible"
      type="button"
      class="user-text-toggle"
      :aria-expanded="expanded"
      @click="toggle"
    >
      {{ t(expanded ? 'showLess' : 'showMore') }}
    </button>
  </div>
</template>

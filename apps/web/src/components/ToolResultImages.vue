<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import type { Entry } from '../lib/types'
import { object } from '../lib/api'
import { imageDimensions, toolResultImages } from '../lib/tool-presentation'
import { useImagePreviewStore } from '../stores/image-preview'

const props = defineProps<{ entry: Entry; sessionID: string }>()
const { t } = useI18n()
const preview = useImagePreviewStore()
const images = computed(() => toolResultImages(props.entry, props.sessionID))
const name = computed(() => String(object(props.entry.tool?.invocation.call.arguments).path || t('imagePreviewTitle')))
function open(index: number) {
  preview.show(images.value.map(image => ({ src: image.src, downloadUrl: image.src, name: name.value })), index)
}
</script>
<template>
  <div
    v-if="images.length"
    class="message-images"
    :class="{ 'message-images-multiple': images.length > 1 }"
  >
    <div
      v-for="image in images"
      :key="image.index"
      class="message-image"
    >
      <button
        type="button"
        class="message-image-trigger"
        :aria-label="t('previewImage', { name })"
        @click="open(image.index)"
      >
        <img
          :src="image.src"
          :alt="name"
          :width="imageDimensions(image.content).width"
          :height="imageDimensions(image.content).height"
          decoding="async"
          loading="lazy"
        >
      </button>
    </div>
  </div>
</template>

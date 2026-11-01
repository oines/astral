<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import { ImageViewer, type ImageViewerLabels } from '@astral/ui'
import { useImagePreviewStore } from '../stores/image-preview'

const images = useImagePreviewStore()
const { t } = useI18n()
const labels = computed<ImageViewerLabels>(() => ({
  title: t('imagePreviewTitle'), close: t('close'), previous: t('previousImage'), next: t('nextImage'),
  zoomIn: t('zoomInImage'), zoomOut: t('zoomOutImage'), fit: t('fitImage'),
  download: t('downloadImage'), downloadPreview: t('downloadImagePreview'), loading: t('loadingImage'),
  failed: t('imageLoadFailed'), originalUnavailable: t('originalImageUnavailable'), retry: t('retry'),
}))
</script>
<template>
  <ImageViewer
    :open="images.open"
    :items="images.items"
    :index="images.index"
    :labels="labels"
    @update:open="!$event && images.close()"
    @update:index="images.index = $event"
  />
</template>

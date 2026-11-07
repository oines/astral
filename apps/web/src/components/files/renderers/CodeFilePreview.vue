<script setup lang="ts">
import CodeViewer from '../../CodeViewer.vue'
import type { FilePreviewResource } from '../../../composables/useFileService'
import { codeLanguage } from '../preview-language'
defineProps<{ preview: FilePreviewResource }>()
</script>
<template>
  <CodeViewer
    :content="preview.content || ''"
    :language="codeLanguage(preview.name)"
  />
</template>

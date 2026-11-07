<script setup lang="ts">
import { useI18n } from 'vue-i18n'
const { t } = useI18n()
withDefaults(defineProps<{ message?: string }>(), { message: '' })
</script>
<template>
  <p class="muted">
    {{ message || t('previewUnavailable') }}
  </p>
</template>

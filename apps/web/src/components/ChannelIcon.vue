<script setup lang="ts">
defineProps<{ platform: string }>()
</script>
<template>
  <svg
    v-if="platform === 'telegram'"
    width="16"
    height="16"
    viewBox="0 0 24 24"
    role="img"
    aria-label="Telegram"
    style="flex-shrink: 0"
  >
    <circle
      cx="12"
      cy="12"
      r="12"
      fill="#229ed9"
    />
    <path
      d="m5 11 13-5c.6-.2 1 .2.8 1l-2.2 11c-.1.6-.5.8-1 .4l-3.4-2.5-1.7 1.7c-.2.2-.3.3-.6.3l.3-3.5 6.3-5.7c.3-.2 0-.4-.3-.2l-7.8 4.9-3.3-1c-.7-.2-.7-.6-.1-.9Z"
      fill="white"
    />
  </svg>
</template>

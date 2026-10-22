<script setup lang="ts">
import mark from '../assets/astral-mark.svg'
import wordmark from '../assets/astral-lockup.svg'
import wordmarkInverse from '../assets/astral-lockup-inverse.svg'

withDefaults(defineProps<{ lockup?: boolean; decorative?: boolean }>(), {
  lockup: false,
  decorative: false,
})
</script>

<template>
  <span
    v-if="lockup"
    class="astral-logo astral-logo-lockup"
    :role="decorative ? undefined : 'img'"
    :aria-label="decorative ? undefined : 'Astral'"
    :aria-hidden="decorative || undefined"
  >
    <img
      class="astral-logo-light"
      :src="wordmark"
      alt=""
      width="260"
      height="80"
      draggable="false"
    >
    <img
      class="astral-logo-dark"
      :src="wordmarkInverse"
      alt=""
      width="260"
      height="80"
      draggable="false"
    >
  </span>
  <img
    v-else
    class="astral-logo"
    :src="mark"
    :alt="decorative ? '' : 'Astral'"
    width="256"
    height="256"
    draggable="false"
  >
</template>

<style scoped>
.astral-logo { display: inline-block; width: 28px; height: 28px; flex-shrink: 0; vertical-align: middle; object-fit: contain; user-select: none; }
.astral-logo-lockup { width: 156px; height: 48px; }
/* The 130 × 40 artwork starts at x=2.5 and is centered at y=17.701.
   Align its visible left edge and vertical center, preserving the source SVG. */
.astral-logo-lockup img { display: block; width: 100%; height: 100%; object-fit: contain; transform: translate(-1.9231%, 5.7475%); }
.astral-logo-lockup .astral-logo-dark { display: none; }
:global(:root[data-theme='dark'] .astral-logo-lockup .astral-logo-light) { display: none; }
:global(:root[data-theme='dark'] .astral-logo-lockup .astral-logo-dark) { display: block; }
</style>

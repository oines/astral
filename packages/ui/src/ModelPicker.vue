<script setup lang="ts">
import { computed, onDeactivated, ref, watch } from 'vue'
import { Check, ChevronDown } from '@astral/icons'
import { DropdownMenuRoot, DropdownMenuTrigger, DropdownMenuPortal, DropdownMenuContent, DropdownMenuRadioGroup, DropdownMenuRadioItem, DropdownMenuItemIndicator, DropdownMenuLabel, DropdownMenuFilter } from 'reka-ui'

const props = defineProps<{
  modelValue: string
  modelLabel: string
  models: Array<{ value: string; label: string; group: string; groupLabel: string; keywords: string }>
  reasoningValue: string
  reasoningLabel: string
  reasoningOptions: Array<{ value: string; label: string }>
  labels: { model: string; reasoning: string; search: string; empty: string }
  busy?: boolean
}>()
const emit = defineEmits<{ 'update:modelValue': [value: string]; 'update:reasoningValue': [value: string] }>()
const open = ref(false)
const reasoningOpen = ref(false)
const search = ref('')
const showSearch = computed(() => props.models.length >= 20)
const showGroups = computed(() => new Set(props.models.map(model => model.group)).size > 1)
const groups = computed(() => {
  const query = search.value.trim().toLocaleLowerCase()
  const result = new Map<string, { label: string; models: typeof props.models }>()
  for (const model of props.models) {
    if (query && !`${model.label} ${model.keywords} ${model.groupLabel}`.toLocaleLowerCase().includes(query)) continue
    if (!result.has(model.group)) result.set(model.group, { label: model.groupLabel, models: [] })
    result.get(model.group)!.models.push(model)
  }
  return [...result.entries()]
})
watch(open, value => { if (!value) search.value = ''; else reasoningOpen.value = false })
watch(reasoningOpen, value => { if (value) open.value = false })
onDeactivated(() => { open.value = false; reasoningOpen.value = false })
</script>
<template>
  <div class="composer-model-options">
    <DropdownMenuRoot v-model:open="open">
      <DropdownMenuTrigger as-child>
        <slot />
      </DropdownMenuTrigger>
      <DropdownMenuPortal>
        <DropdownMenuContent
          class="model-choice-menu model-options-menu"
          side="top"
          align="end"
          :side-offset="8"
          :collision-padding="12"
        >
          <div
            v-if="showSearch"
            class="model-choice-search"
          >
            <DropdownMenuFilter
              v-model="search"
              :aria-label="labels.search"
              :placeholder="labels.search"
              auto-focus
            />
          </div>
          <DropdownMenuLabel
            v-if="!showGroups"
            class="model-choice-label"
          >
            {{ labels.model }}
          </DropdownMenuLabel>
          <DropdownMenuRadioGroup
            :model-value="modelValue"
            @update:model-value="emit('update:modelValue', String($event))"
          >
            <template
              v-for="[key, group] in groups"
              :key="key"
            >
              <DropdownMenuLabel
                v-if="showGroups"
                class="model-choice-label"
              >
                {{ group.label }}
              </DropdownMenuLabel>
              <DropdownMenuRadioItem
                v-for="model in group.models"
                :key="model.value"
                :value="model.value"
                :disabled="busy"
                :text-value="model.label"
                :title="model.label"
                class="model-choice-row"
              >
                <span class="model-choice-name">{{ model.label }}</span><DropdownMenuItemIndicator><Check /></DropdownMenuItemIndicator>
              </DropdownMenuRadioItem>
            </template>
          </DropdownMenuRadioGroup>
          <p
            v-if="!groups.length"
            class="model-choice-empty"
          >
            {{ labels.empty }}
          </p>
        </DropdownMenuContent>
      </DropdownMenuPortal>
    </DropdownMenuRoot>
    <DropdownMenuRoot
      v-if="reasoningOptions.length"
      v-model:open="reasoningOpen"
    >
      <DropdownMenuTrigger as-child>
        <button
          type="button"
          class="composer-picker reasoning-picker"
          :aria-label="labels.reasoning"
          :title="`${labels.reasoning}: ${reasoningLabel}`"
        >
          <span>{{ reasoningLabel }}</span><ChevronDown />
        </button>
      </DropdownMenuTrigger>
      <DropdownMenuPortal>
        <DropdownMenuContent
          class="model-choice-menu reasoning-options-menu"
          side="top"
          align="end"
          :side-offset="8"
          :collision-padding="12"
        >
          <DropdownMenuLabel class="model-choice-label">
            {{ labels.reasoning }}
          </DropdownMenuLabel>
          <DropdownMenuRadioGroup
            :model-value="reasoningValue"
            @update:model-value="emit('update:reasoningValue', String($event))"
          >
            <DropdownMenuRadioItem
              v-for="option in reasoningOptions"
              :key="option.value"
              :value="option.value"
              :disabled="busy"
              class="model-choice-row"
            >
              <span class="model-choice-name">{{ option.label }}</span><DropdownMenuItemIndicator><Check /></DropdownMenuItemIndicator>
            </DropdownMenuRadioItem>
          </DropdownMenuRadioGroup>
        </DropdownMenuContent>
      </DropdownMenuPortal>
    </DropdownMenuRoot>
  </div>
</template>

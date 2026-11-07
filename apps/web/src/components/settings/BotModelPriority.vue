<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, onMounted, ref, useId, watch } from 'vue'
import { applyAgentModelPriority, listAgents, saveAgent, type AstralCoreModelSelection as Priority } from '@astral/sdk'
import { Button, ConfirmPopover, SettingsRow, Switch } from '@astral/ui'
import { GripVertical, Plus, X } from '@astral/icons'
import { useI18n } from 'vue-i18n'
import type { Agent } from '../../lib/types'
import type { ModelSelection as Selection } from '../../lib/model-reasoning'
import { clone, errorText, jsonEqual, request } from '../../lib/api'
import { useSettingsSave } from '../../composables/useSettingsSave'
import { useAppStore } from '../../stores/app'
import ModelSelection from '../ModelSelection.vue'
import SettingsFeedback from './SettingsFeedback.vue'

type Policy = { models: Priority[]; autoRestore: boolean }
const props = defineProps<{ agent: Agent }>()
const { t } = useI18n()
const app = useAppStore()
const saves = useSettingsSave()
const resource = computed(() => `agent:${props.agent.id}`)
const scope = computed(() => `${resource.value}/model-priority`)
function policyFor(agent: Agent): Policy {
  return { models: agent.model_priority ?? (agent.default_model_id ? [{ id: agent.default_model_id, reasoning: agent.default_reasoning }] : []), autoRestore: agent.model_auto_restore ?? false }
}
const policy = computed(() => saves.value(scope.value, policyFor(props.agent)))
const saving = computed(() => saves.state(scope.value).status === 'saving')
const ready = computed(() => !['saving', 'error', 'uncertain'].includes(saves.state(scope.value).status))
const selectedIDs = computed(() => policy.value.models.map(model => model.id))
const canAdd = computed(() => app.availableModels.some(model => !selectedIDs.value.includes(model.id)))
const confirm = ref(false)
const applying = ref(false)
const applyError = ref('')
const listElement = ref<HTMLOListElement>()
const dragging = ref('')
const pointerActive = ref(false)
const dropBefore = ref<string | null>(null)
const sortHintID = useId()
const announcement = ref('')
const editingDisabled = computed(() => saving.value || applying.value || pointerActive.value)
type PointerSort = { id: string; pointerID: number; x: number; y: number; handle: HTMLElement; agentID: string; revision: number; order: string[] }
let pointerSort: PointerSort | undefined
const rowKeys = new Map<string, number>()
let nextRowKey = 0
function rowKey(id: string) {
  if (!rowKeys.has(id)) rowKeys.set(id, ++nextRowKey)
  return rowKeys.get(id)!
}
function name(id: string) {
  const model = app.models.find(model => model.id === id)
  return [model?.name || t('model'), model?.provider_name].filter(Boolean).join(' · ')
}
function samePolicy(a: Policy, b: Policy) {
  return a.autoRestore === b.autoRestore && a.models.length === b.models.length && a.models.every((model, index) => model.id === b.models[index]?.id && jsonEqual(model.reasoning, b.models[index]?.reasoning))
}
async function save(next: Policy) {
  const id = props.agent.id
  const desired = clone(next)
  announcement.value = ''
  applyError.value = ''
  async function latest() {
    const current = (await request(listAgents())).find(agent => agent.id === id)
    if (!current) throw { code: 'not_found', message: t('botMissing') }
    return current
  }
  return saves.run(`agent:${id}`, 'model-priority', {
    write: async () => {
      const current = await latest()
      if (!samePolicy(policyFor(current), desired)) {
        const first = desired.models[0]
        const saved = await request(saveAgent({ body: { ...current, model_priority: desired.models, model_auto_restore: desired.autoRestore, default_model_id: first?.id || '', default_reasoning: first?.reasoning } }))
        saves.confirmAgent(saved)
      }
      await app.refresh().catch(() => undefined)
    },
    reconcile: async () => {
      const current = await latest()
      saves.confirmAgent(current)
      await app.refresh().catch(() => undefined)
      return samePolicy(policyFor(current), desired) ? 'applied' : 'not-applied'
    },
  }, desired)
}
function select(index: number, selection: Selection) {
  if (editingDisabled.value || !selection.profile_id) return
  if (policy.value.models.some((model, position) => position !== index && model.id === selection.profile_id)) return
  const models = [...policy.value.models]
  const previous = models[index]
  if (previous && previous.id !== selection.profile_id) {
    rowKeys.set(selection.profile_id, rowKey(previous.id))
    rowKeys.delete(previous.id)
  }
  models[index] = { ...(previous?.id === selection.profile_id ? previous : {}), id: selection.profile_id, reasoning: selection.reasoning }
  void save({ ...policy.value, models })
}
async function commitOrder(models: Priority[], movedID: string) {
  if (editingDisabled.value || models.every((model, index) => model.id === policy.value.models[index]?.id)) return
  const applied = await save({ ...policy.value, models })
  await nextTick()
  focusHandle(movedID)
  if (applied) announcement.value = t('botModelMoved', { name: name(movedID), number: models.findIndex(model => model.id === movedID) + 1 })
}
function move(index: number, delta: number) {
  const target = index + delta
  if (editingDisabled.value || target < 0 || target >= policy.value.models.length) return
  const models = [...policy.value.models]
  const [moved] = models.splice(index, 1)
  if (moved) {
    models.splice(target, 0, moved)
    void commitOrder(models, moved.id)
  }
}
function remove(index: number) {
  if (!editingDisabled.value) void save({ ...policy.value, models: policy.value.models.filter((_, position) => position !== index) })
}
function rows() { return Array.from(listElement.value?.querySelectorAll<HTMLElement>('[data-model-id]') || []) }
function focusHandle(id: string) {
  rows().find(row => row.dataset.modelId === id)?.querySelector<HTMLButtonElement>('[data-reorder-handle]')?.focus()
}
function cancelDrag() {
  const previous = pointerSort
  pointerSort = undefined
  pointerActive.value = false
  dragging.value = ''
  dropBefore.value = null
  if (previous?.handle.hasPointerCapture(previous.pointerID)) previous.handle.releasePointerCapture(previous.pointerID)
}
function startDrag(event: PointerEvent, id: string) {
  if (editingDisabled.value || policy.value.models.length < 2 || event.isPrimary === false || event.button !== 0) return
  const handle = event.currentTarget as HTMLElement
  pointerSort = { id, pointerID: event.pointerId, x: event.clientX, y: event.clientY, handle, agentID: props.agent.id, revision: props.agent.revision, order: [...selectedIDs.value] }
  handle.setPointerCapture(event.pointerId)
  handle.focus({ preventScroll: true })
  pointerActive.value = true
  event.preventDefault()
}
function dragMove(event: PointerEvent) {
  const current = pointerSort
  if (!current || current.pointerID !== event.pointerId) return
  if (!dragging.value && Math.hypot(event.clientX - current.x, event.clientY - current.y) < 6) return
  dragging.value = current.id
  event.preventDefault()
  const bounds = listElement.value?.getBoundingClientRect()
  if (!bounds || event.clientX < bounds.left || event.clientX > bounds.right || event.clientY < bounds.top - 24 || event.clientY > bounds.bottom + 24) {
    dropBefore.value = null
    return
  }
  dropBefore.value = rows().filter(row => row.dataset.modelId !== current.id).find(row => {
    const rect = row.getBoundingClientRect()
    return event.clientY < rect.top + rect.height / 2
  })?.dataset.modelId ?? ''
}
function endDrag(event: PointerEvent) {
  const current = pointerSort
  if (!current || current.pointerID !== event.pointerId) return
  const before = dropBefore.value
  const changed = dragging.value && before !== null && current.agentID === props.agent.id && current.revision === props.agent.revision && jsonEqual(current.order, selectedIDs.value)
  cancelDrag()
  if (!changed) return
  const models = policy.value.models.filter(model => model.id !== current.id)
  const moved = policy.value.models.find(model => model.id === current.id)
  if (!moved) return
  const at = before ? models.findIndex(model => model.id === before) : models.length
  if (at < 0) return
  models.splice(at, 0, moved)
  void commitOrder(models, current.id)
}
function cancelPointer(event: PointerEvent) {
  if (pointerSort?.pointerID === event.pointerId) cancelDrag()
}
function sortKey(event: KeyboardEvent, id: string) {
  if (event.key === 'Escape') { cancelDrag(); return }
  if (editingDisabled.value || !['ArrowUp', 'ArrowDown', 'Home', 'End'].includes(event.key)) return
  event.preventDefault()
  const index = selectedIDs.value.indexOf(id)
  const target = event.key === 'Home' ? 0 : event.key === 'End' ? policy.value.models.length - 1 : index + (event.key === 'ArrowUp' ? -1 : 1)
  move(index, target - index)
}
watch(() => [props.agent.id, props.agent.revision, policy.value], cancelDrag, { deep: true, flush: 'sync' })
function escapeDrag(event: KeyboardEvent) { if (event.key === 'Escape') cancelDrag() }
onMounted(() => {
  if (typeof window !== 'undefined') {
    window.addEventListener('blur', cancelDrag)
    window.addEventListener('keydown', escapeDrag)
  }
})
onBeforeUnmount(() => {
  cancelDrag()
  if (typeof window !== 'undefined') {
    window.removeEventListener('blur', cancelDrag)
    window.removeEventListener('keydown', escapeDrag)
  }
})
async function apply() {
  if (editingDisabled.value || !ready.value || !policy.value.models.length) return
  applying.value = true
  applyError.value = ''
  try {
    await request(applyAgentModelPriority({ path: { id: props.agent.id } }))
    await app.refresh().catch(() => undefined)
    confirm.value = false
  } catch (error) {
    await app.refresh().catch(() => undefined)
    applyError.value = errorText(error)
  } finally { applying.value = false }
}
</script>
<template>
  <div class="bot-model-priority">
    <div class="bot-model-priority-heading">
      <strong>{{ t('botModelPriority') }}</strong>
      <p>{{ t('botModelPriorityHint') }}</p>
    </div>
    <p
      :id="sortHintID"
      class="sr-only"
    >
      {{ t('botModelSortHint') }}
    </p>
    <ol
      v-if="policy.models.length"
      ref="listElement"
      class="bot-model-priority-list"
      :class="{ 'is-drop-end': dragging && dropBefore === '' }"
      :aria-label="t('botModelPriority')"
    >
      <li
        v-for="(model, index) in policy.models"
        :key="rowKey(model.id)"
        :data-model-id="model.id"
        class="bot-model-priority-row"
        :class="{ 'is-dragging': dragging === model.id, 'is-drop-before': dragging && dropBefore === model.id }"
      >
        <Button
          variant="ghost"
          size="icon"
          class="bot-model-drag-handle"
          data-reorder-handle
          :aria-label="t('botDragPriorityModel', { name: name(model.id) })"
          :aria-describedby="sortHintID"
          aria-keyshortcuts="ArrowUp ArrowDown Home End"
          :disabled="saving || applying || policy.models.length < 2 || pointerActive && dragging !== model.id && pointerSort?.id !== model.id"
          @pointerdown="startDrag($event, model.id)"
          @pointermove="dragMove"
          @pointerup="endDrag"
          @pointercancel="cancelPointer"
          @lostpointercapture="cancelPointer"
          @keydown="sortKey($event, model.id)"
        >
          <GripVertical />
        </Button>
        <ModelSelection
          :model-value="model.id"
          :reasoning="model.reasoning"
          :label="t('botPriorityModelNumber', { number: index + 1 })"
          :exclude="selectedIDs"
          show-provider
          :busy="editingDisabled"
          :disabled="editingDisabled"
          @select="select(index, $event)"
        />
        <Button
          variant="ghost"
          size="icon"
          class="bot-model-remove"
          :aria-label="t('botRemovePriorityModel', { name: name(model.id) })"
          :disabled="editingDisabled"
          @click="remove(index)"
        >
          <X />
        </Button>
      </li>
    </ol>
    <div class="bot-model-priority-toolbar">
      <ModelSelection
        model-value=""
        :label="t('botAddPriorityModel')"
        :allow-empty="false"
        close-on-select
        :exclude="selectedIDs"
        :busy="editingDisabled"
        :disabled="!canAdd || editingDisabled"
        @select="select(policy.models.length, $event)"
      >
        <template #trigger>
          <Button
            size="small"
            :disabled="!canAdd || editingDisabled"
          >
            <Plus />{{ t('botAddPriorityModel') }}
          </Button>
        </template>
      </ModelSelection>
      <ConfirmPopover
        v-model:open="confirm"
        :title="t('botApplyModelsConfirm', { name: agent.name, model: name(policy.models[0]?.id || '') })"
        :description="t('botApplyModelsHint')"
        :confirm-label="t('botApplyModelsAction')"
        :cancel-label="t('cancel')"
        :busy="applying"
        :error="applyError"
        variant="primary"
        @confirm="apply"
      >
        <template #trigger>
          <Button
            size="small"
            :disabled="!ready || !policy.models.length || editingDisabled"
            @click="applyError = ''"
          >
            {{ t('botApplyModelsToAll') }}
          </Button>
        </template>
      </ConfirmPopover>
    </div>
    <span
      class="sr-only"
      role="status"
      aria-live="polite"
    >{{ announcement }}</span>
    <SettingsFeedback :scope="scope" />
  </div>
  <SettingsRow
    :label="t('botAutoRestoreModel')"
    :description="t('botAutoRestoreModelHint')"
  >
    <Switch
      :model-value="policy.autoRestore"
      :label="t('botAutoRestoreModel')"
      :disabled="editingDisabled"
      @update:model-value="save({ ...policy, autoRestore: $event })"
    />
  </SettingsRow>
</template>
<style scoped>
.bot-model-priority { padding-top: var(--settings-row-padding); }
.bot-model-priority-heading strong { font-size: 14px; font-weight: 500; line-height: 20px; }
.bot-model-priority-heading p { margin-top: 2px; color: var(--muted); font-size: 12px; line-height: 16px; }
.bot-model-priority-list { position: relative; list-style: none; padding: 0; margin: 8px 0 0; }
.bot-model-priority-row { position: relative; display: grid; grid-template-columns: auto minmax(0, 1fr) auto; align-items: center; gap: 8px; min-height: var(--settings-compact-row-min-height); padding: calc((var(--settings-compact-row-min-height) - var(--control-height)) / 2) 0; }
.bot-model-priority-row + .bot-model-priority-row::after { content: ''; position: absolute; top: 0; left: 0; right: 0; height: 1px; background: var(--panel-line); pointer-events: none; }
.bot-model-priority-row > .model-selection { min-width: 0; }
.bot-model-drag-handle, .bot-model-remove { color: var(--muted); }
.bot-model-drag-handle { touch-action: none; cursor: grab; }
.bot-model-drag-handle:disabled { cursor: default; }
.bot-model-drag-handle:active { transform: none; }
.bot-model-drag-handle svg, .bot-model-remove svg { width: 16px; height: 16px; }
.is-dragging { opacity: .5; }
.is-dragging .bot-model-drag-handle { cursor: grabbing; }
.is-drop-before::before, .is-drop-end::after { content: ''; position: absolute; z-index: 1; left: 0; right: 0; height: 2px; background: var(--focus-ring); pointer-events: none; }
.is-drop-before::before { top: -1px; }
.is-drop-end::after { bottom: -1px; }
.bot-model-priority-toolbar { display: flex; align-items: center; flex-wrap: wrap; gap: 8px; padding: var(--settings-row-padding) calc(var(--control-icon-size) + 8px); }
.bot-model-priority-toolbar .button { white-space: nowrap; }
.bot-model-priority-toolbar svg { width: 16px; height: 16px; }
.bot-model-priority > .settings-feedback { margin-bottom: var(--settings-row-padding); }
.bot-model-priority + .preference-row { border-top: 1px solid var(--panel-line); }
</style>

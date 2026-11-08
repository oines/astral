import { computed, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { useI18n } from 'vue-i18n'
import { useAppStore } from '../stores/app'
import { automationTarget, type AutomationTarget } from '../lib/automation-links'

// Resolve one explicit navigation intent, never repeat it on SSE refreshes.
export function useAutomationEditorLink<T extends { id: string; agent_id: string }>(kind: AutomationTarget['kind'], options: { items: () => T[]; ready: () => boolean; busy: () => boolean; open: (item: T) => void }) {
  const app = useAppStore()
  const route = useRoute()
  const router = useRouter()
  const { t } = useI18n()
  const error = ref('')
  const target = computed(() => route.path === '/schedules' ? automationTarget(route.query) : undefined)
  let handled = ''
  watch([target, options.items, options.ready, options.busy, () => app.activeAgentID, () => app.agents], () => {
    const requested = target.value
    if (!requested || requested.kind !== kind) { handled = ''; error.value = ''; return }
    const key = `${requested.agentID}:${requested.kind}:${requested.id}`
    if (handled === key) return
    if (!app.agents.some(agent => agent.id === requested.agentID)) {
      if (app.data) { handled = key; error.value = t('automationNotFound') }
      return
    }
    if (app.activeAgentID !== requested.agentID) app.activeAgentID = requested.agentID
    if (!options.ready() || options.busy()) return
    const item = options.items().find(item => item.id === requested.id && item.agent_id === requested.agentID)
    handled = key
    error.value = item ? '' : t('automationNotFound')
    if (item) options.open(item)
  }, { immediate: true })
  function clear(id?: string) {
    if (!id || target.value?.kind !== kind || target.value.id !== id) return
    const query = { ...route.query }
    delete query[kind === 'schedule' ? 'schedule' : 'endpoint']
    void router.replace({ path: '/schedules', query })
  }
  return { error, clear }
}

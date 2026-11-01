import { computed } from 'vue'
import { useAppStore } from '../stores/app'
import { hostnameSettingsIssue, localizeHostnameError, sameHostnames } from '../lib/network-access'
import { useSettingsSave } from './useSettingsSave'

type HostnameDraft = { restricted: boolean; hostnames: string[] }
type Message = (key: string, values?: Record<string, string>) => string

export function useNetworkSettings(message: Message) {
  const app = useAppStore()
  const saves = useSettingsSave()
  const network = computed(() => app.data?.network)
  const hostnameKey = 'network/hostnames'
  const editor = saves.form<{ draft?: HostnameDraft }>(hostnameKey, () => ({}))
  const confirmed = computed<HostnameDraft>(() => ({ restricted: network.value?.restrict_hostnames.value ?? false, hostnames: network.value?.allowed_hostnames.value ?? [] }))
  const hostnames = computed(() => editor.draft?.hostnames ?? confirmed.value.hostnames)
  const restrictHostnames = computed(() => editor.draft?.restricted ?? confirmed.value.restricted)
  const showHostnames = computed(() => restrictHostnames.value || editor.draft !== undefined)
  const hasHostnameDraft = computed(() => editor.draft !== undefined)
  const hostnameSaveState = computed(() => saves.state(hostnameKey))
  const canEditHostnames = computed(() => Boolean(network.value?.restrict_hostnames.editable || network.value?.allowed_hostnames.editable))
  const allowRemote = computed(() => saves.value('network/allow_remote_management', network.value?.allow_remote_management.value ?? false))
  function draft() {
    return editor.draft ??= { restricted: confirmed.value.restricted, hostnames: [...confirmed.value.hostnames] }
  }
  function setHostname(index: number, value: string) {
    if (network.value?.allowed_hostnames.editable) draft().hostnames[index] = value
  }
  function addHostname() { if (network.value?.allowed_hostnames.editable) draft().hostnames.push('') }
  function removeHostname(index: number) {
    if (network.value?.allowed_hostnames.editable) draft().hostnames.splice(index, 1)
  }
  function setRestrictHostnames(value: boolean) {
    if (!network.value?.restrict_hostnames.editable) return
    const next = draft()
    next.restricted = value
    if (value && !next.hostnames.some(hostname => hostname.trim()) && network.value.allowed_hostnames.editable && typeof window !== 'undefined' && window.location.hostname) {
      next.hostnames = [window.location.hostname.replace(/^\[|\]$/g, '')]
    }
  }
  async function saveHostnameSettings() {
    if (!network.value || !canEditHostnames.value || !editor.draft || hostnameSaveState.value.status === 'saving' || hostnameSaveState.value.status === 'uncertain') return
    const submitted = { restricted: editor.draft.restricted, hostnames: editor.draft.hostnames.map(hostname => hostname.trim()).filter(Boolean) }
    const snapshot = JSON.stringify(editor.draft)
    const patch: { restrict_hostnames?: boolean; allowed_hostnames?: string[] } = {}
    if (network.value.restrict_hostnames.editable) patch.restrict_hostnames = submitted.restricted
    if (network.value.allowed_hostnames.editable) patch.allowed_hostnames = submitted.hostnames
    const currentHostname = typeof window === 'undefined' ? '' : window.location.hostname
    const complete = () => { if (JSON.stringify(editor.draft) === snapshot) delete editor.draft }
    await saves.run('network', 'hostnames', {
      write: async () => {
        const issue = hostnameSettingsIssue(submitted.hostnames, submitted.restricted, currentHostname)
        if (issue) throw { code: 'validation', message: message(issue.key, issue.values) }
        try { await app.networkSettings(patch) }
        catch (error) { throw localizeHostnameError(error, message, currentHostname) }
        complete()
      },
      reconcile: async () => {
        const current = await app.refreshNetwork()
        if (current.restrict_hostnames.value !== submitted.restricted || !sameHostnames(current.allowed_hostnames.value, submitted.hostnames)) return 'not-applied'
        complete()
        return 'applied'
      },
    })
  }
  async function setAllowRemote(value: boolean) {
    if (!network.value?.allow_remote_management.editable) return
    await saves.run('network', 'allow_remote_management', {
      write: async () => { await app.networkSettings({ allow_remote_management: value }) },
      reconcile: async () => (await app.refreshNetwork()).allow_remote_management.value === value ? 'applied' : 'not-applied',
    }, value)
  }
  return { network, hostnames, allowRemote, restrictHostnames, showHostnames, hasHostnameDraft, hostnameSaveState, canEditHostnames, setHostname, addHostname, removeHostname, saveHostnameSettings, setRestrictHostnames, setAllowRemote }
}

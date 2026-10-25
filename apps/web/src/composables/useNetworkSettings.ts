import { computed } from 'vue'
import { useAppStore } from '../stores/app'
import { useSettingsSave } from './useSettingsSave'

export function useNetworkSettings() {
  const app = useAppStore()
  const saves = useSettingsSave()
  const network = computed(() => app.data?.network)
  const addressKey = 'network/public_url'
  const address = computed({
    get: () => saves.drafts[addressKey] ?? (network.value?.public_url.editable ? network.value.public_url.saved_value : network.value?.public_url.value) ?? '',
    set: value => { saves.drafts[addressKey] = value },
  })
  const allowRemote = computed(() => saves.value('network/allow_remote_management', network.value?.allow_remote_management.value ?? false))
  async function saveAddress() {
    if (!network.value?.public_url.editable) return
    const value = address.value.trim().replace(/\/+$/, '')
    if (value === network.value.public_url.saved_value && saves.state(addressKey).status !== 'error') {
      delete saves.drafts[addressKey]
      return
    }
    saves.drafts[addressKey] = value
    const confirmed = () => { if (saves.drafts[addressKey] === value) delete saves.drafts[addressKey] }
    await saves.run('network', 'public_url', {
      write: async () => { await app.networkSettings({ public_url: value }); confirmed() },
      reconcile: async () => {
        const current = await app.refreshNetwork()
        if (current.public_url.saved_value !== value) return 'not-applied'
        confirmed()
        return 'applied'
      },
    }, value)
  }
  async function setAllowRemote(value: boolean) {
    if (!network.value?.allow_remote_management.editable) return
    await saves.run('network', 'allow_remote_management', {
      write: async () => { await app.networkSettings({ allow_remote_management: value }) },
      reconcile: async () => (await app.refreshNetwork()).allow_remote_management.value === value ? 'applied' : 'not-applied',
    }, value)
  }
  return { network, address, allowRemote, saveAddress, setAllowRemote }
}

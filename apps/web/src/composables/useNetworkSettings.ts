import { computed } from 'vue'
import { useAppStore } from '../stores/app'
import { useSettingsSave } from './useSettingsSave'

export function useNetworkSettings() {
  const app = useAppStore()
  const saves = useSettingsSave()
  const network = computed(() => app.data?.network)
  const addressKey = 'network/public_urls'
  const editor = saves.form<{ draft?: string[] }>(addressKey, () => ({}))
  const addresses = computed(() => editor.draft ?? (network.value?.public_urls.editable ? network.value.public_urls.saved_value : network.value?.public_urls.value) ?? [])
  const allowRemote = computed(() => saves.value('network/allow_remote_management', network.value?.allow_remote_management.value ?? false))
  function setAddress(index: number, value: string) {
    editor.draft = addresses.value.map((address, i) => i === index ? value : address)
  }
  function addAddress() { if (network.value?.public_urls.editable) editor.draft = [...addresses.value, ''] }
  async function saveAddresses() {
    if (!network.value?.public_urls.editable) return
    const value = addresses.value.map(address => address.trim().replace(/\/+$/, '')).filter(Boolean)
    const draft = JSON.stringify(addresses.value)
    const same = (other: string[]) => JSON.stringify(other) === JSON.stringify(value)
    if (same(network.value.public_urls.saved_value) && saves.state(addressKey).status !== 'error') {
      delete editor.draft
      return
    }
    const confirmed = () => { if (JSON.stringify(editor.draft) === draft) delete editor.draft }
    await saves.run('network', 'public_urls', {
      write: async () => { await app.networkSettings({ public_urls: value }); confirmed() },
      reconcile: async () => {
        const current = await app.refreshNetwork()
        if (!same(current.public_urls.saved_value)) return 'not-applied'
        confirmed()
        return 'applied'
      },
    }, value)
  }
  async function removeAddress(index: number) {
    if (!network.value?.public_urls.editable) return
    editor.draft = addresses.value.filter((_, i) => i !== index)
    await saveAddresses()
  }
  async function setAllowRemote(value: boolean) {
    if (!network.value?.allow_remote_management.editable) return
    await saves.run('network', 'allow_remote_management', {
      write: async () => { await app.networkSettings({ allow_remote_management: value }) },
      reconcile: async () => (await app.refreshNetwork()).allow_remote_management.value === value ? 'applied' : 'not-applied',
    }, value)
  }
  return { network, addresses, allowRemote, setAddress, addAddress, removeAddress, saveAddresses, setAllowRemote }
}

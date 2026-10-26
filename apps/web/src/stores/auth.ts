import { computed, ref } from 'vue'
import { defineStore } from 'pinia'
import { authAPI, type AuthStatus } from '../lib/auth'
import { errorText } from '../lib/api'

export const useAuthStore = defineStore('auth', () => {
  const status = ref<AuthStatus>()
  const ready = ref(false)
  const error = ref('')
  const authenticated = computed(() => status.value?.authenticated === true)
  let checking: Promise<void> | undefined
  let events: EventSource | undefined
  let eventTimer: ReturnType<typeof setTimeout> | undefined
  function closeEvents() { events?.close(); events = undefined; clearTimeout(eventTimer) }
  function listen() {
    closeEvents()
    if (!authenticated.value || status.value?.desktop) return
    const stream = new EventSource('/api/v1/auth/events')
    events = stream
    stream.addEventListener('revoked', () => {
      if (events !== stream) return
      void refresh()
    })
    stream.onerror = () => {
      if (events !== stream) return
      closeEvents()
      void refresh().then(() => { if (authenticated.value) eventTimer = setTimeout(listen, 3000) })
    }
  }
  async function refresh() {
    if (checking) return checking
    checking = (async () => {
      try {
        const next = await authAPI.status()
        const startEvents = next.authenticated && !next.desktop && (!status.value?.authenticated || status.value?.desktop)
        status.value = next
        error.value = ''
        ready.value = true
        if (!next.authenticated) closeEvents()
        else if (startEvents) listen()
      } catch (e) { error.value = errorText(e) }
      finally { checking = undefined }
    })()
    return checking
  }
  function invalidate() {
    if (status.value) status.value = { ...status.value, authenticated: false }
    closeEvents()
    window.dispatchEvent(new Event('astral:authentication-changed'))
    void refresh()
  }
  window.addEventListener('astral:unauthorized', invalidate)
  window.addEventListener('focus', () => { if (ready.value) void refresh() })
  return { status, ready, error, authenticated, refresh, invalidate }
})

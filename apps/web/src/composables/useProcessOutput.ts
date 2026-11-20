import { onBeforeUnmount, ref, watch, type Ref } from 'vue'
import { useDelayedPending } from '@astral/ui'

interface ProcessOutputSnapshot {
  execution_id: string
  running: boolean
  exit_code?: number
  error?: string
  output: string
  truncated?: boolean
}

interface ProcessOutputUpdate {
  chunk?: string
  reset?: boolean
  output?: string
  truncated?: boolean
  done?: boolean
  exit_code?: number
  error?: string
}

const MAX_CLIENT_OUTPUT_CHARS = 128_000

export function useProcessOutput(options: {
  sessionID: Ref<string>
  executionID: Ref<string>
  enabled: Ref<boolean>
}) {
  const output = ref<string | undefined>(undefined)
  const running = ref<boolean | undefined>(undefined)
  const exitCode = ref<number | undefined>(undefined)
  const error = ref('')
  const truncated = ref(false)
  const loading = ref(false)
  const waiting = useDelayedPending(loading)
  let source: EventSource | undefined

  function close() {
    if (source) {
      source.close()
      source = undefined
    }
    loading.value = false
  }

  function connect() {
    close()
    if (!options.enabled.value || !options.sessionID.value || !options.executionID.value) return
    if (typeof EventSource === 'undefined') return
    loading.value = true
    const url = `/api/v1/sessions/${encodeURIComponent(options.sessionID.value)}/processes/${encodeURIComponent(options.executionID.value)}/output`
    const stream = new EventSource(url)
    source = stream

    stream.addEventListener('snapshot', event => {
      if (source !== stream) return
      try {
        const snap = JSON.parse((event as MessageEvent<string>).data) as ProcessOutputSnapshot
        output.value = snap.output
        running.value = snap.running
        exitCode.value = snap.exit_code
        error.value = snap.error || ''
        truncated.value = Boolean(snap.truncated)
      } finally {
        loading.value = false
      }
    })

    stream.addEventListener('update', event => {
      if (source !== stream) return
      const update = JSON.parse((event as MessageEvent<string>).data) as ProcessOutputUpdate
      if (update.reset) {
        output.value = update.output ?? ''
      } else if (update.chunk) {
        const combined = (output.value ?? '') + update.chunk
        output.value = combined.length > MAX_CLIENT_OUTPUT_CHARS ? combined.slice(-MAX_CLIENT_OUTPUT_CHARS) : combined
      }
      if (update.truncated) truncated.value = true
    })

    stream.addEventListener('done', event => {
      if (source !== stream) return
      try {
        const done = JSON.parse((event as MessageEvent<string>).data) as ProcessOutputUpdate
        running.value = false
        if (done.exit_code !== undefined) exitCode.value = done.exit_code
        if (done.error) error.value = done.error
        if (done.truncated) truncated.value = true
      } finally {
        close()
      }
    })

    stream.onerror = () => {
      if (source !== stream) return
      close()
    }
  }

  watch([options.enabled, options.sessionID, options.executionID], ([enabled]) => {
    if (enabled) connect()
    else close()
  }, { immediate: true })

  onBeforeUnmount(close)

  return { output, running, exitCode, error, truncated, waiting }
}

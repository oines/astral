import { nextTick } from 'vue'

interface PreviewScrollElement { scrollTop: number; isConnected: boolean }

// KeepAlive detaches scrolling elements; loading can also temporarily empty
// them. Neither zero position should replace the reader's last visible scroll.
export function createPreviewScrollState(element: () => PreviewScrollElement | undefined, afterRender: () => Promise<unknown> = nextTick) {
  let active = false
  let loading = false
  let restoring = false
  let position = 0
  let activation = 0
  function record() {
    const current = element()
    if (active && !loading && !restoring && current?.isConnected) position = current.scrollTop
  }
  async function restore() {
    const current = ++activation
    restoring = true
    await afterRender()
    if (current !== activation) return
    const target = element()
    if (active && !loading && target?.isConnected) target.scrollTop = position
    restoring = false
  }
  function activate() { active = true; return restore() }
  function deactivate() { active = false; activation++; restoring = false }
  function suspend() {
    record()
    loading = true; activation++; restoring = false
  }
  function resume() { loading = false; return restore() }
  function reset() { position = 0; activation++; restoring = true }
  return { record, activate, deactivate, suspend, resume, reset }
}

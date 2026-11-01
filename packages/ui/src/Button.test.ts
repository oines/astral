import { beforeEach, describe, expect, it, vi } from 'vitest'
import { createSSRApp, h } from 'vue'
import { renderToString } from 'vue/server-renderer'
import Button from './Button.vue'

const feedback = vi.hoisted(() => ({ visible: false }))
vi.mock('./useDelayedPending', async () => {
  const { ref } = await import('vue')
  return { useDelayedPending: () => ref(feedback.visible) }
})

beforeEach(() => { feedback.visible = false })

function renderIconButton() {
  return renderToString(createSSRApp({
    render: () => h(Button, { size: 'icon', busy: true, 'aria-label': 'Send' }, {
      default: () => h('svg', { 'data-testid': 'send-icon' }, [h('path', { d: 'M0 4h8' })]),
    }),
  }))
}

describe('Button pending feedback', () => {
  it('keeps the original icon before delayed feedback becomes visible', async () => {
    const html = await renderIconButton()

    expect(html).toContain('<svg')
    expect(html).toContain('data-testid="send-icon"')
    expect(html).not.toContain('class="spinner"')
  })

  it('replaces the icon with a spinner after delayed feedback becomes visible', async () => {
    feedback.visible = true
    const html = await renderIconButton()

    expect(html).toContain('class="spinner"')
    expect(html).not.toContain('<svg')
    expect(html).toContain('aria-label="Send"')
  })

  it('keeps a text button label alongside its pending spinner', async () => {
    feedback.visible = true
    const html = await renderToString(createSSRApp({
      render: () => h(Button, { busy: true }, { default: () => 'Connect' }),
    }))

    expect(html).toContain('class="spinner"')
    expect(html).toContain('Connect')
  })
})

// Imported only by the disposable packaged-App smoke. This inspects the actual
// renderer and native WCO metrics; it does not synthesize a copy of the UI.
import assert from 'node:assert/strict'
import { mkdir, writeFile } from 'node:fs/promises'
import path from 'node:path'
import { setTimeout as delay } from 'node:timers/promises'
import { evaluateRenderer, managedRendererEndpoint, rendererCommand, RendererNavigationError } from './desktop-renderer-auth.mjs'

function windowGeometry(page, origin) {
  const { document, location, getComputedStyle, innerHeight, innerWidth, navigator, window } = globalThis
  const layout = document.querySelector(page === 'onboarding' ? '.onboarding-layout' : '.settings-layout')
  if (location.origin !== origin || document.readyState !== 'complete' || !layout) return { ready: false }
  const rect = element => {
    const bounds = element?.getBoundingClientRect()
    return bounds && { x: bounds.x, y: bounds.y, width: bounds.width, height: bounds.height, top: bounds.top, right: bounds.right, bottom: bounds.bottom, left: bounds.left }
  }
  const visible = element => {
    const bounds = element.getBoundingClientRect()
    const style = getComputedStyle(element)
    return bounds.width > 0 && bounds.height > 0 && bounds.bottom > 0 && bounds.top < innerHeight && style.display !== 'none' && style.visibility === 'visible'
  }
  const native = navigator.windowControlsOverlay
  const toggle = document.querySelector('.chat-layout .sidebar-toggle')
  const controls = [...layout.querySelectorAll('button, a, input, textarea, select, [role="combobox"]')].filter(visible)
  const content = layout.querySelector(page === 'onboarding' ? '.onboarding-brand' : '.settings-back')
  return {
    ready: Boolean(content && visible(content)),
    platform: window.astralDesktop?.platform,
    desktop: document.documentElement.dataset.desktop,
    theme: document.documentElement.dataset.theme,
    titlebarHeight: Number.parseFloat(getComputedStyle(document.documentElement).getPropertyValue('--desktop-titlebar-height')),
    viewport: { width: innerWidth, height: innerHeight },
    overlay: native && { visible: native.visible, ...rect({ getBoundingClientRect: () => native.getTitlebarAreaRect() }) },
    content: rect(content),
    controls: controls.map(element => ({ ...rect(element), drag: getComputedStyle(element).getPropertyValue('-webkit-app-region') })),
    drag: getComputedStyle(layout, '::before').getPropertyValue('-webkit-app-region'),
    toggle: toggle && visible(toggle) ? { ...rect(toggle), drag: getComputedStyle(toggle).getPropertyValue('-webkit-app-region') } : null,
  }
}

function assertGeometry(state, platform) {
  assert.equal(state.platform, platform, 'Window smoke reached the wrong native platform bridge')
  assert.equal(state.desktop, { darwin: 'mac', win32: 'win', linux: 'linux' }[platform], 'The actual renderer did not enable its native titlebar layout')
  assert.equal(state.titlebarHeight, 44, 'The actual renderer did not use the shared 44px titlebar')
  assert.equal(state.drag, 'drag', 'The visible page has no native draggable titlebar')
  assert.ok(state.content.top >= 43, 'Page content overlaps the native titlebar')
  assert.ok(state.controls.length > 0, 'The visible page has no real controls to inspect')
  for (const control of state.controls) {
    assert.ok(control.top >= 43, 'A visible page control overlaps the native titlebar')
    assert.equal(control.drag, 'no-drag', 'A visible interactive control became a native drag target')
  }
  if (platform !== 'darwin') {
    const area = state.overlay
    assert.equal(area?.visible, true, 'Native Window Controls Overlay is not visible')
    assert.ok(area.x >= 0 && area.y >= 0 && area.width > 0, 'Native titlebar safe area is invalid')
    assert.ok(Math.abs(area.height - 44) <= 2, 'Native overlay height does not match the compact titlebar')
    assert.ok(area.right <= state.viewport.width + 2 && area.width < state.viewport.width - 10, 'Native buttons have no reserved titlebar space')
  }
  if (state.toggle) {
    assert.equal(state.toggle.drag, 'no-drag', 'The actual chat sidebar toggle became a drag target')
    assert.ok(Math.abs(state.toggle.top + state.toggle.height / 2 - 22) <= 2, 'The chat sidebar toggle is not centered in the titlebar')
    const expectedLeft = platform === 'darwin' ? 92 : state.overlay.x + 20
    assert.ok(Math.abs(state.toggle.left - expectedLeft) <= 2, 'The chat sidebar toggle is not in its platform position')
  }
}

async function geometry(endpoint, page, origin, theme) {
  const deadline = Date.now() + 30_000
  while (Date.now() < deadline) {
    try {
      const state = await evaluateRenderer(endpoint, `(${windowGeometry})(${JSON.stringify(page)}, ${JSON.stringify(origin)})`)
      if (state.ready && state.theme === theme) return state
    } catch (error) {
      if (!(error instanceof RendererNavigationError)) throw error
    }
    await delay(150)
  }
  throw new Error(`The actual ${page} page did not become visible with the ${theme} theme.`)
}

async function screenshot(endpoint, state, name) {
  if (!process.env.RUNNER_TEMP) return
  // The caller has already authenticated a fresh profile. Only fixed onboarding
  // and Appearance pages are captured, cropped above any connection fields.
  // CDP captures renderer pixels, not OS caption-button pixels or the desktop.
  const capture = await rendererCommand(endpoint, 'Page.captureScreenshot', {
    format: 'jpeg', quality: 65, fromSurface: true, captureBeyondViewport: false,
    clip: { x: 0, y: 0, width: Math.min(state.viewport.width, 1400), height: Math.min(state.viewport.height, 150), scale: 1 },
  })
  assert.equal(typeof capture.data, 'string', 'The managed renderer did not return its screenshot')
  const bytes = Buffer.from(capture.data, 'base64')
  assert.ok(bytes.length > 0 && bytes.length < (1 << 20), 'Titlebar screenshot exceeded its small artifact budget')
  const directory = path.join(process.env.RUNNER_TEMP, 'astral-titlebar')
  await mkdir(directory, { recursive: true, mode: 0o700 })
  await writeFile(path.join(directory, `${process.platform}-${process.arch}-${name}.jpg`), bytes, { mode: 0o600 })
}

export async function checkDesktopWindow(profile, serverURL) {
  assert.ok(process.env.CI, 'Window acceptance requires the disposable CI profile')
  const endpoint = await managedRendererEndpoint(profile, serverURL)
  assert.ok(endpoint, 'The managed Desktop page is unavailable for window acceptance')
  const origin = new URL(serverURL).origin
  // All configuration writes stay inside this smoke's new data directory. They
  // exercise real bootstrap/theme behavior, rather than changing DOM styles.
  const setTheme = async theme => evaluateRenderer(endpoint, `(async () => {
    if (location.origin !== ${JSON.stringify(origin)}) throw new Error('Window acceptance left the managed origin.');
    const current = await fetch('/api/v1/preferences', { cache: 'no-store' });
    if (!current.ok) throw new Error('Window acceptance could not read fixture preferences.');
    const preferences = await current.json();
    const saved = await fetch('/api/v1/preferences', { method: 'PUT', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ ...preferences, theme: ${JSON.stringify(theme)} }) });
    if (!saved.ok) throw new Error('Window acceptance could not save the fixture theme.');
    return true;
  })()`)
  for (const [page, theme] of [['onboarding', 'light'], ['settings', 'light'], ['settings', 'dark']]) {
    await setTheme(theme)
    const url = new URL(page === 'onboarding' ? '/' : '/settings/appearance', origin)
    const navigation = await rendererCommand(endpoint, 'Page.navigate', { url: url.href })
    assert.ok(!navigation.errorText, 'The managed window could not open its fixture page')
    const state = await geometry(endpoint, page, origin, theme)
    await screenshot(endpoint, state, `${page}-${theme}`)
    assertGeometry(state, process.platform)
    // A fulfilled IPC round trip proves both supported appearance values can be
    // applied by the real main process, including under a packaged sandbox.
    await evaluateRenderer(endpoint, `(async () => {
      if (location.origin !== ${JSON.stringify(origin)}) throw new Error('Window acceptance left the managed origin.');
      await window.astralDesktop.appearance(${theme === 'dark'});
      return true;
    })()`)
  }
  console.log('Actual packaged renderer titlebar safe area, onboarding/Settings clearance, non-draggable controls, and light/dark native appearance passed. Cropped screenshots show renderer layout; physical drag/double-click and OS caption pixels require native interaction review.')
}

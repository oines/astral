import assert from 'node:assert/strict'

// Read the existing Owner relay to the running Electron update controller.
// An unmanaged Go Server can report its own version but cannot prove the
// enclosing App's version; manager=desktop makes that distinction explicit.
export async function verifyDesktopVersion(serverURL, cookie, version, { request = globalThis.fetch } = {}) {
  const read = async (route, authenticated = false) => {
    const response = await request(new URL(route, serverURL), {
      headers: authenticated ? { Cookie: cookie } : {},
      redirect: 'error', signal: AbortSignal.timeout(5000),
    })
    assert.ok(response.ok, `Installed version endpoint ${route} failed (HTTP ${response.status}).`)
    return response.json()
  }
  const [health, desktop] = await Promise.all([read('/health'), read('/api/v1/updates', true)])
  assert.equal(health.status, 'ok', 'The installed Go Server is not healthy.')
  assert.equal(health.version, version, 'The installed Go Server version differs from the frozen release version.')
  assert.equal(desktop.manager, 'desktop', 'The installed version must come from the running Electron update bridge.')
  assert.equal(desktop.current_version, version, 'The installed Electron App version differs from the frozen release version.')
  return { electron_version: desktop.current_version, server_version: health.version }
}

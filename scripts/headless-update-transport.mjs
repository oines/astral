import { createReadStream } from 'node:fs'
import { readFile, stat } from 'node:fs/promises'
import { createServer as createHTTPServer } from 'node:http'
import { createServer as createHTTPSServer } from 'node:https'
import { connect } from 'node:net'
import path from 'node:path'

// The Go updater keeps its fixed official URLs. A disposable CI user trusts
// only this process's temporary TLS fixture through standard proxy/CA settings;
// production has no arbitrary update-origin or test-transport option.
export async function headlessUpdateTransport({ temporary, payload, tag, environment, command }) {
  const certificate = path.join(temporary, 'fixture-ca.pem')
  const key = path.join(temporary, 'fixture-ca.key')
  await command('openssl', ['req', '-x509', '-newkey', 'rsa:2048', '-nodes', '-keyout', key, '-out', certificate, '-days', '1', '-subj', '/CN=Astral disposable CI update fixture', '-addext', 'subjectAltName=DNS:api.github.com,DNS:github.com', '-addext', 'basicConstraints=critical,CA:TRUE'])
  const requests = []
  const secure = createHTTPSServer({ key: await readFile(key), cert: await readFile(certificate) }, async (request, response) => {
    const host = request.headers.host
    const address = new URL(request.url, `https://${host}`)
    requests.push(address.href)
    let name
    if (host === 'api.github.com' && address.pathname === '/repos/oines/astral/releases/latest') name = 'release.json'
    else if (host === 'github.com' && address.pathname.startsWith(`/oines/astral/releases/download/${tag()}/`)) name = address.pathname.split('/').at(-1)
    if (!name || !/^[A-Za-z0-9_.-]+$/.test(name)) { response.writeHead(404); response.end(); return }
    try {
      const info = await stat(path.join(payload, name))
      response.writeHead(200, { 'Content-Type': 'application/octet-stream', 'Content-Length': info.size, 'Cache-Control': 'no-store' })
      createReadStream(path.join(payload, name)).pipe(response)
    } catch { response.writeHead(404); response.end() }
  })
  await new Promise(resolve => secure.listen(0, '127.0.0.1', resolve))
  const sockets = new Set()
  const proxy = createHTTPServer((_request, response) => { response.writeHead(405); response.end() })
  proxy.on('connection', socket => { sockets.add(socket); socket.on('close', () => sockets.delete(socket)) })
  proxy.on('connect', (request, socket, head) => {
    if (!['api.github.com:443', 'github.com:443'].includes(request.url)) { socket.destroy(); return }
    const upstream = connect(secure.address().port, '127.0.0.1', () => {
      socket.write('HTTP/1.1 200 Connection Established\r\n\r\n')
      if (head.length) upstream.write(head)
      socket.pipe(upstream); upstream.pipe(socket)
    })
    sockets.add(upstream); upstream.on('close', () => sockets.delete(upstream))
    upstream.on('error', () => socket.destroy()); socket.on('error', () => upstream.destroy()); socket.on('close', () => upstream.destroy())
  })
  await new Promise(resolve => proxy.listen(0, '127.0.0.1', resolve))
  const values = { HTTPS_PROXY: `http://127.0.0.1:${proxy.address().port}`, SSL_CERT_FILE: certificate, NO_PROXY: 'localhost,127.0.0.1,::1', PATH: environment.PATH, ASTRAL_INSTALL_FIXTURE_DIRECTORY: payload }
  const prior = new Map()
  let changed = false
  let closed = false
  async function close() {
    if (closed) return
    closed = true
    const failures = []
    try {
      if (changed) for (const [name] of Object.entries(values)) {
        try {
          if (prior.has(name)) await command('systemctl', ['--user', 'set-environment', `${name}=${prior.get(name)}`])
          else await command('systemctl', ['--user', 'unset-environment', name])
        } catch (error) { failures.push(error) }
      }
    } finally {
      for (const socket of sockets) socket.destroy()
      secure.closeAllConnections()
      await Promise.all([new Promise(resolve => proxy.close(resolve)), new Promise(resolve => secure.close(resolve))])
    }
    if (failures.length) throw new AggregateError(failures, 'The disposable CI user manager environment could not be restored.')
  }
  try {
    // env -0 avoids reparsing shell-escaped systemctl show-environment output.
    // Capture only the selected previous values and never print the environment.
    const priorOutput = (await command('systemd-run', ['--user', '--wait', '--pipe', '--collect', '--quiet', '/usr/bin/env', '-0'])).stdout
    for (const value of priorOutput.split('\0')) {
      const at = value.indexOf('=')
      if (at > 0 && Object.hasOwn(values, value.slice(0, at))) prior.set(value.slice(0, at), value.slice(at + 1))
    }
    changed = true
    await command('systemctl', ['--user', 'set-environment', ...Object.entries(values).map(([name, value]) => `${name}=${value}`)])
    return { requests, proxyURL: values.HTTPS_PROXY, certificate, close }
  } catch (error) {
    await close()
    throw error
  }
}

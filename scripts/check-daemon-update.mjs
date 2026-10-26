// Real native user-service updates, only on disposable GitHub Actions accounts.
// The disposable source copy changes only release routing/TLS fixture trust;
// production sources retain the fixed official release feed and contain no
// acceptance-mode environment switch or management-authentication bypass.
import assert from 'node:assert/strict'
import { execFile, spawn } from 'node:child_process'
import { createHash, randomBytes } from 'node:crypto'
import { createReadStream } from 'node:fs'
import { chmod, copyFile, cp, mkdir, mkdtemp, readFile, rm, writeFile } from 'node:fs/promises'
import { createServer } from 'node:https'
import path from 'node:path'
import { userInfo } from 'node:os'
import { setTimeout as delay } from 'node:timers/promises'
import { fileURLToPath } from 'node:url'
import { promisify } from 'node:util'

if (process.env.CI !== 'true' || process.env.GITHUB_ACTIONS !== 'true' || !process.env.RUNNER_TEMP) throw new Error('Real Daemon update acceptance requires a disposable GitHub Actions user; never run on a developer account.')
if (!['darwin', 'win32', 'linux'].includes(process.platform)) throw new Error('Unsupported native Daemon update platform.')
const execute = promisify(execFile)
const repository = fileURLToPath(new URL('../', import.meta.url))
const system = process.platform === 'win32' ? 'windows' : process.platform
const architecture = process.arch === 'x64' ? 'amd64' : process.arch
const suffix = system === 'windows' ? '.exe' : ''
const temporary = await mkdtemp(path.join(process.env.RUNNER_TEMP, 'astral-daemon-update-'))
const source = path.join(temporary, 'source')
const daemonRoot = path.join(temporary, 'remote computer')
const oldBinary = path.join(temporary, `old-astral${suffix}`)
const newBinary = path.join(temporary, `new-astral${suffix}`)
const fixtureBinary = path.join(temporary, `fixture-server${suffix}`)
const readyFile = path.join(temporary, 'ready.json')
const processFile = path.join(temporary, 'command-processes.json')
const credentialFile = path.join(daemonRoot, 'credentials.json')
const oldVersion = '0.1.0'
const newVersion = '0.1.1'
const owner = randomBytes(32).toString('hex')
const assetName = `astral-${system}-${architecture}${suffix}`
const feedRequests = []
let allowDownload
const downloadGate = new Promise(resolve => { allowDownload = resolve })
let serverURL
let release
let fixture
let fixtureClosed
let serviceID
let created = false
let uninstalled = false
let retain = false
let checksum
let fixtureOutput = ''

const command = (executable, args, options = {}) => execute(executable, args, { cwd: repository, windowsHide: true, timeout: 120_000, maxBuffer: 2 << 20, ...options })
const run = async (executable, ...args) => (await command(executable, args)).stdout.trim()
const alive = pid => { try { process.kill(pid, 0); return true } catch (error) { if (error.code === 'ESRCH') return false; throw error } }
const shellQuote = text => system === 'windows' ? `'${text.replaceAll("'", "''")}'` : `'${text.replaceAll("'", "'\\''")}'`
const state = async () => JSON.parse(await run(oldBinary, 'computer', 'service', 'status', '--root', daemonRoot))
const status = async () => JSON.parse(await run(oldBinary, 'computer', 'status', '--root', daemonRoot))
async function daemonPID() {
  if (system !== 'windows') return Number((await readFile(path.join(daemonRoot, 'daemon.lock'), 'utf8')).trim())
  // Windows byte-range locks are mandatory. Reading daemon.lock from Node can
  // fail while the Daemon owns its first byte; inspect this exact executable
  // and test root instead of trying to read through the live lock.
  const spec = JSON.parse(await readFile(path.join(daemonRoot, 'service/spec.json'), 'utf8'))
  const script = String.raw`$ErrorActionPreference='Stop'; $matches=@(Get-CimInstance Win32_Process | Where-Object { $_.ExecutablePath -eq ${shellQuote(spec.executable)} -and $_.CommandLine -and $_.CommandLine.Contains(${shellQuote(daemonRoot)}) -and $_.CommandLine -match '\bcomputer[^\w]+daemon\b' }); if($matches.Count -ne 1){throw ('Expected one test Daemon, found '+$matches.Count)}; $matches[0].ProcessId`
  return Number(await run('powershell.exe', '-NoLogo', '-NoProfile', '-NonInteractive', '-Command', script))
}
async function waitFor(read, message, timeout = 90_000) {
  const deadline = Date.now() + timeout
  while (Date.now() < deadline) {
    const value = await read()
    if (value) return value
    await delay(200)
  }
  throw new Error(message)
}
async function api(relative, body) {
  const response = await fetch(`${serverURL}/api/v1${relative}`, { method: body === undefined ? 'GET' : 'POST', headers: { Authorization: `Desktop ${owner}`, 'Content-Type': 'application/json' }, body: body === undefined ? undefined : JSON.stringify(body), signal: AbortSignal.timeout(10_000) })
  if (!response.ok) throw new Error(`Daemon update API returned HTTP ${response.status}: ${await response.text()}`)
  return response.json()
}
async function helperPresent() {
  if (!serviceID) return false
  if (system === 'linux') {
    const result = await command('systemctl', ['--user', 'show', `astral-daemon-update-${serviceID}.service`, '--property=LoadState', '--value']).catch(() => undefined)
    return result?.stdout.trim() === 'loaded'
  }
  const args = system === 'darwin'
    ? ['/bin/launchctl', ['print', `gui/${userInfo().uid}/app.astral.daemon.update.${serviceID}`]]
    : ['schtasks.exe', ['/query', '/tn', `\\Astral\\Astral Daemon update (${serviceID})`]]
  return command(...args).then(() => true, () => false)
}
async function diagnostics() {
  console.error(fixtureOutput.slice(-12_000))
  for (const file of ['daemon.log', 'update.log', 'service-launch.log']) {
    try { console.error(`${file}:\n${(await readFile(path.join(daemonRoot, file), 'utf8')).slice(-12_000)}`) } catch { /* Startup may not have created the log yet. */ }
  }
  if (system === 'linux' && serviceID) {
    const result = await command('journalctl', ['--user', '-u', `astral-daemon-update-${serviceID}.service`, '-n', '60', '--no-pager']).catch(error => ({ stdout: error.message }))
    console.error(result.stdout)
  }
}

try {
  if (system === 'linux') await command('systemctl', ['--user', 'show-environment'])
  // Generate an ephemeral CA/leaf with Go, avoiding OS trust-store mutation.
  const certificateProgram = path.join(temporary, 'certificate.go')
  await writeFile(certificateProgram, `package main
import("crypto/ed25519";"crypto/rand";"crypto/x509";"crypto/x509/pkix";"encoding/json";"encoding/pem";"math/big";"net";"os";"time")
func main(){pub,key,e:=ed25519.GenerateKey(rand.Reader);if e!=nil{panic(e)};cert:=&x509.Certificate{SerialNumber:big.NewInt(1),Subject:pkix.Name{CommonName:"Astral disposable update fixture"},NotBefore:time.Now().Add(-time.Hour),NotAfter:time.Now().Add(time.Hour),IPAddresses:[]net.IP{net.ParseIP("127.0.0.1")},IsCA:true,BasicConstraintsValid:true,KeyUsage:x509.KeyUsageCertSign|x509.KeyUsageDigitalSignature,ExtKeyUsage:[]x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}};raw,e:=x509.CreateCertificate(rand.Reader,cert,cert,pub,key);if e!=nil{panic(e)};private,e:=x509.MarshalPKCS8PrivateKey(key);if e!=nil{panic(e)};_ = json.NewEncoder(os.Stdout).Encode(map[string]string{"cert":string(pem.EncodeToMemory(&pem.Block{Type:"CERTIFICATE",Bytes:raw})),"key":string(pem.EncodeToMemory(&pem.Block{Type:"PRIVATE KEY",Bytes:private}))})}
`)
  const tls = JSON.parse((await command('go', ['run', certificateProgram], { env: { ...process.env, GOWORK: 'off' } })).stdout)
  release = createServer(tls, (request, response) => {
    const url = new URL(request.url, 'https://127.0.0.1')
    feedRequests.push(url.pathname)
    if (url.pathname === '/latest') {
      response.writeHead(200, { 'Content-Type': 'application/json' })
      response.end(JSON.stringify({ tag_name: `v${newVersion}`, draft: false, prerelease: false }))
    } else if (url.pathname === `/releases/download/v${newVersion}/checksums.txt`) {
      response.writeHead(200, { 'Content-Type': 'text/plain' })
      response.end(`${checksum}  ${assetName}\n`)
    } else if (url.pathname === `/releases/download/v${newVersion}/${assetName}`) {
      response.writeHead(200, { 'Content-Type': 'application/octet-stream' })
      // Keep the first update active until the second click is acknowledged,
      // so deduplication does not depend on runner or network speed.
      void downloadGate.then(() => {
        if (response.destroyed) return
        const stream = createReadStream(newBinary)
        stream.on('error', () => response.destroy())
        response.once('close', () => stream.destroy())
        stream.pipe(response)
      })
    } else { response.writeHead(404); response.end() }
  })
  await new Promise(resolve => release.listen(0, '127.0.0.1', resolve))
  const releaseOrigin = `https://127.0.0.1:${release.address().port}`
  await mkdir(source)
  for (const file of ['go.mod', 'go.sum']) await copyFile(path.join(repository, file), path.join(source, file))
  await cp(path.join(repository, 'internal'), path.join(source, 'internal'), { recursive: true, filter: name => !name.endsWith('_test.go') })
  await cp(path.join(repository, 'cmd/astral'), path.join(source, 'cmd/astral'), { recursive: true })
  const releaseSource = path.join(source, 'internal/releasefeed/releases.go')
  const original = await readFile(path.join(repository, 'internal/releasefeed/releases.go'), 'utf8')
  let copied = original
  function replaceOnce(before, after) { assert.equal(copied.split(before).length - 1, 1, `Fixture source contract changed: ${before}`); copied = copied.replace(before, after) }
  replaceOnce('func client() *http.Client {', `func client() *http.Client {\n roots:=x509.NewCertPool();if !roots.AppendCertsFromPEM([]byte(${JSON.stringify(tls.cert)})){panic("invalid fixture CA")} `)
  replaceOnce('"crypto/sha256"', '"crypto/sha256"\n"crypto/tls"\n"crypto/x509"')
  replaceOnce('return &http.Client{Timeout:', 'return &http.Client{Transport:&http.Transport{TLSClientConfig:&tls.Config{RootCAs:roots}},Timeout:')
  replaceOnce('https://api.github.com/repos/oines/astral/releases/latest', `${releaseOrigin}/latest`)
  replaceOnce('https://github.com/oines/astral/releases/download/v', `${releaseOrigin}/releases/download/v`)
  await writeFile(releaseSource, copied)
  const buildEnvironment = { ...process.env, GOWORK: 'off', CGO_ENABLED: '0' }
  for (const [version, target] of [[oldVersion, oldBinary], [newVersion, newBinary]]) {
    await command('go', ['build', '-trimpath', '-ldflags', `-s -w -X github.com/oines/astral/internal/cli.Version=${version}`, '-o', target, './cmd/astral'], { cwd: source, env: buildEnvironment, timeout: 300_000 })
    if (system !== 'windows') await chmod(target, 0o700)
  }
  checksum = createHash('sha256').update(await readFile(newBinary)).digest('hex')
  const fixtureDirectory = path.join(source, 'cmd/daemon-update-fixture')
  await mkdir(fixtureDirectory)
  // This test-only Server starts an ordinary bound command through Backend.
  // All update requests below still use the actual Owner HTTP management route.
  await writeFile(path.join(fixtureDirectory, 'main.go'), `package main
import("context";"encoding/json";"fmt";"io";"os";"path/filepath";"time";core "github.com/oines/astral-core";"github.com/oines/astral-core/standardtools";"github.com/oines/astral/internal/computers";"github.com/oines/astral/internal/domain";"github.com/oines/astral/internal/platform")
type config struct{Root,Ready,Owner,Command string}
func main(){if e:=run();e!=nil{fmt.Fprintln(os.Stderr,e);os.Exit(1)}}
func run()error{var c config;raw,e:=os.ReadFile(os.Args[1]);if e!=nil{return e};if e=json.Unmarshal(raw,&c);e!=nil{return e};ctx,cancel:=context.WithTimeout(context.Background(),6*time.Minute);defer cancel();s,e:=platform.NewContext(ctx,platform.Config{Listen:"127.0.0.1:0",DataDirectory:filepath.Join(c.Root,"server"),ComputerDirectory:filepath.Join(c.Root,"computers"),Version:"${oldVersion}",DesktopToken:c.Owner,NoLocal:true});if e!=nil{return e};defer func(){closeCtx,stop:=context.WithTimeout(context.Background(),20*time.Second);defer stop();_ = s.Close(closeCtx)}();if e=s.Start();e!=nil{return e};if e=platform.WriteJSON(c.Ready,map[string]any{"url":s.URL,"pid":os.Getpid()});e!=nil{return e};go func(){_,_ = io.Copy(io.Discard,os.Stdin);cancel()}();go func(){ticker:=time.NewTicker(100*time.Millisecond);defer ticker.Stop();for{select{case<-ctx.Done():return;case<-ticker.C:};records,e:=s.Store.List(ctx,"computers");if e!=nil{continue};for _,record:=range records{var computer domain.Computer;if json.Unmarshal(record.Data,&computer)!=nil||computer.Local||!s.Hub.Status(computer).Online{continue};agent:=domain.Agent{Meta:domain.Meta{ID:"daemon-update-test-agent"},Name:"Update acceptance",AllowedComputers:[]string{computer.ID}};session:=domain.Session{Meta:domain.Meta{ID:"daemon-update-test-session"},AgentID:agent.ID,ComputerID:computer.ID};if e=s.Store.Save(ctx,"agents",&agent.Meta,&agent);e==nil{e=s.Store.Save(ctx,"sessions",&session.Meta,&session)};if e!=nil{fmt.Fprintln(os.Stderr,e);cancel();return};binding,e:=s.Hub.Bind(ctx,agent.ID,session.ID,computer.ID,"");if e!=nil{fmt.Fprintln(os.Stderr,e);cancel();return};backend:=&computers.Backend{Hub:s.Hub};process,e:=backend.StartProcess(ctx,core.Invocation{SessionID:session.ID,ExecutionID:domain.ID("execution"),Binding:computers.BindingJSON(binding)},standardtools.ProcessRequest{Command:c.Command});if e!=nil{fmt.Fprintln(os.Stderr,e);cancel();return};go func(){_,_ = io.Copy(io.Discard,process.Output());_,_ = process.Wait()}();return}}}();<-ctx.Done();return nil}
`)
  await command('go', ['build', '-trimpath', '-o', fixtureBinary, './cmd/daemon-update-fixture'], { cwd: source, env: buildEnvironment, timeout: 300_000 })
  const nodeCode = `const{spawn}=require('node:child_process');const fs=require('node:fs');const child=spawn(process.execPath,['-e','setInterval(()=>{},1000)'],{stdio:'ignore'});fs.writeFileSync(${JSON.stringify(processFile)},JSON.stringify({parent:process.pid,child:child.pid}));setInterval(()=>{},1000)`
  // PowerShell 5 native argument forwarding can strip JavaScript's embedded
  // double quotes. Pass a script filename instead of a source string to -e.
  const taskProgram = path.join(temporary, 'command-tree.cjs')
  await writeFile(taskProgram, nodeCode)
  const taskCommand = `${system === 'windows' ? '& ' : ''}${shellQuote(process.execPath)} ${shellQuote(taskProgram)}`
  const config = path.join(temporary, 'fixture-config.json')
  await writeFile(config, JSON.stringify({ Root: path.join(temporary, 'owner'), Ready: readyFile, Owner: owner, Command: taskCommand }), { mode: 0o600 })
  fixture = spawn(fixtureBinary, [config], { cwd: temporary, stdio: ['pipe', 'pipe', 'pipe'], windowsHide: true })
  fixture.stdin.on('error', () => {})
  fixtureClosed = new Promise(resolve => fixture.once('close', resolve))
  fixture.stdout.on('data', raw => { fixtureOutput = (fixtureOutput + raw.toString()).slice(-16_384) })
  fixture.stderr.on('data', raw => { fixtureOutput = (fixtureOutput + raw.toString()).slice(-16_384) })
  fixture.once('error', error => { fixtureOutput += error.message })
  const ready = await waitFor(() => readFile(readyFile, 'utf8').then(JSON.parse).catch(() => undefined), 'Fixture Server did not become ready', 30_000)
  serverURL = ready.url
  const enrollment = await api('/computers/enrollments', { container: false })
  const enrollmentFile = path.join(temporary, 'enrollment.json')
  await writeFile(enrollmentFile, JSON.stringify({ server_url: serverURL, server_pin: enrollment.server_pin, enrollment_id: enrollment.enrollment.id, token: enrollment.token }), { mode: 0o600 })
  created = true
  await run(oldBinary, 'computer', 'connect', '--enrollment-file', enrollmentFile, '--root', daemonRoot)
  const credentials = await readFile(credentialFile, 'utf8')
  const identity = JSON.parse(credentials).computer_id
  const before = await waitFor(async () => { const value = await status(); return value.online && value.daemon_version === oldVersion ? value : undefined }, 'Old native Daemon did not connect')
  const service = await state()
  serviceID = service.id
  assert.equal(service.state, 'running')
  const oldPID = await daemonPID()
  const commands = await waitFor(() => readFile(processFile, 'utf8').then(JSON.parse).catch(() => undefined), 'Bound command and descendant did not start')
  assert.ok(alive(oldPID) && alive(commands.parent) && alive(commands.child), 'Initial Daemon and bound command tree must be alive')
  const agentFile = path.join(daemonRoot, 'agents/daemon-update-test-agent/home/preserved.txt')
  await writeFile(agentFile, 'Agent files survive a native management update.\n')
  const started = await api(`/computers/maintenance/${identity}/update`, {})
  assert.equal(started.state, 'checking')
  const duplicate = await api(`/computers/maintenance/${identity}/update`, {})
  assert.equal(duplicate.started_at, started.started_at, 'Concurrent clicks must share the same update operation')
  allowDownload()
  const finished = await waitFor(async () => {
    const value = await api(`/computers/maintenance/${identity}`)
    if (value.update.state === 'failed') throw new Error(value.update.error)
    return value.update.state === 'complete' ? value : undefined
  }, 'Native management update did not finish', 150_000)
  assert.equal(finished.update.version, newVersion)
  assert.equal(finished.daemon_newer, true, 'Newer Daemon should recommend updating the Server')
  assert.equal(finished.can_update, true, 'The Server version must not block future Daemon updates')
  const after = await status()
  assert.equal(after.id, before.id)
  assert.equal(after.id, identity)
  assert.equal(after.daemon_version, newVersion)
  assert.notEqual(after.boot_id, before.boot_id)
  const newPID = await daemonPID()
  assert.notEqual(newPID, oldPID)
  assert.ok(alive(newPID))
  await waitFor(() => !alive(oldPID) && !alive(commands.parent) && !alive(commands.child), 'Old Daemon or bound command descendants survived update')
  const spec = JSON.parse(await readFile(path.join(daemonRoot, 'service/spec.json'), 'utf8'))
  assert.equal(createHash('sha256').update(await readFile(spec.executable)).digest('hex'), checksum)
  assert.notEqual(spec.executable, oldBinary)
  assert.equal(await readFile(credentialFile, 'utf8'), credentials)
  assert.equal(await readFile(agentFile, 'utf8'), 'Agent files survive a native management update.\n')
  await waitFor(async () => !await helperPresent(), 'One-shot update job was not unregistered')
  await api(`/computers/maintenance/${identity}/update`, {})
  await waitFor(async () => (await api(`/computers/maintenance/${identity}`)).update.state === 'current', 'Same latest version did not become a no-op')
  assert.equal((await status()).boot_id, after.boot_id)
  assert.equal(feedRequests.filter(url => url === `/releases/download/v${newVersion}/${assetName}`).length, 1, 'Duplicate or current-version request downloaded a second executable')
  assert.ok(feedRequests.includes(`/releases/download/v${newVersion}/checksums.txt`))
  assert.equal(await readFile(path.join(repository, 'internal/releasefeed/releases.go'), 'utf8'), original, 'Acceptance fixture modified production release routing')
  await run(oldBinary, 'computer', 'service', 'uninstall', '--root', daemonRoot)
  uninstalled = true
  assert.equal((await state()).state, 'not-installed')
  console.log(`Native ${system} Owner update RPC → official-release fixture TLS/hash → independent user update job → new boot/version → command-tree cleanup and identity preservation passed.`)
} catch (error) {
  await diagnostics()
  const message = String(error.message).replaceAll('%', '%25').replaceAll('\r', '%0D').replaceAll('\n', '%0A')
  console.error(`::error title=Native Daemon update acceptance::${message}`)
  throw error
} finally {
  allowDownload()
  if (created && !uninstalled) {
    try {
      await waitFor(async () => !await helperPresent(), 'Update helper still owns this test installation', 180_000)
      await run(oldBinary, 'computer', 'service', 'uninstall', '--root', daemonRoot)
    } catch (error) { retain = true; console.error('Native update cleanup failed:', error.message) }
  }
  if (fixture) {
    fixture.stdin.end()
    await Promise.race([fixtureClosed, delay(25_000, undefined, { ref: false })])
    if (fixture.exitCode === null && fixture.signalCode === null) { fixture.kill('SIGKILL'); await fixtureClosed }
  }
  if (release) await new Promise(resolve => release.close(resolve))
  if (retain) console.error(`Test installation retained at ${temporary}; remove only its user service after inspecting the logs.`)
  else await rm(temporary, { recursive: true, force: true, maxRetries: 5, retryDelay: 200 })
}

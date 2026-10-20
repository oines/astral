# Astral

Astral is a self-hosted workspace for persistent agents. The macOS app and Linux server use the same Web interface. Agents run commands and read or write files through explicitly assigned Computers: the server's local machine, a container, or another Mac or Linux machine.

The implementation follows [SPEC.md](SPEC.md). The server owns configuration, credentials, sessions, Telegram connections and delivery receipts. [Astral Core](https://github.com/oines/astral-core) owns the persistent agent loop. [astral-models](https://github.com/oines/astral-models) translates Core requests into provider protocols.

## Build and start

The pinned development tools are in [mise.toml](mise.toml). Install those versions, then:

```sh
pnpm install --frozen-lockfile
pnpm build:distribution
```

The build generates OpenAPI and the TypeScript SDK, builds the shared Web app, and embeds it in four binaries:

```text
dist/bin/astral-darwin-arm64
dist/bin/astral-darwin-amd64
dist/bin/astral-linux-arm64
dist/bin/astral-linux-amd64
dist/bin/checksums.txt
dist/bin/THIRD_PARTY_NOTICES.txt
```

Start the binary for your machine, for example:

```sh
./dist/bin/astral-darwin-arm64 serve --distribution-dir dist/bin
```

Open `http://127.0.0.1:4545`. The server prints the path of its private Owner token file; enter that token at the Web sign-in screen. The token is stored in an HttpOnly cookie after sign-in. The desktop app performs this local handoff in its main process.

The first-use path is **Settings → Models → add Provider → add Profile → verify tool calling**, then **Settings → Agents → select that model and a Computer**. New sessions inherit the Agent defaults. Provider keys and endpoints are global resources, so Agents do not need separate copies.

The model adapters support Chat Completions for OpenAI, DeepSeek, Qwen and OpenRouter, and Responses for OpenAI and DeepSeek. Verification sends two requests to test tool calling and replay against the actual configured endpoint. Native reasoning options are preserved. Search uses one selected Tavily or Exa resource per Agent.

## macOS application

After building the distribution:

```sh
pnpm --filter @astral/desktop package
```

The application, DMG and ZIP for Apple Silicon and Intel are written to `apps/desktop/release/`. The default build is unsigned; signing and notarization require the distributor's Apple credentials and release process.

Electron manages the local Go server, local Daemon, window, menu, tray and login preference. Closing the window follows the General setting; quitting stops the managed service. A server also stops if its desktop parent dies. Renderer code receives neither the Owner token nor a general native filesystem API.

## Computers, files and conversations

**Settings → Computers → Add Computer** offers three paths:

- **Use this machine** reuses the server's local Computer.
- **Use a local container** creates a persistent container through the server's local Docker-compatible runtime.
- **Connect another machine** produces an expiring enrollment command. Its container option creates a separate Computer on the machine where the command runs.

Registration does not automatically add a Computer to an Agent. Assign it explicitly and optionally set it as the default. A session can change its current Computer while idle. Existing process handles and file references remain attached to their original Computer.

AgentHome is shared by sessions of the same Agent on the same Computer. Forking a conversation preserves history and its branch relationship; it does not copy working files or Telegram bindings.

Uploaded originals and generated files live on Computers. `astral-file://` links stream from the source Computer for preview or download. Images actually supplied to the model are also frozen inside the durable rollout, so history and replay remain valid after an original is removed or its source goes offline. That preserved model input does not substitute for an unavailable original-file download.

Telegram is the V1 messaging adapter. Configure a bot Connection, verify a Chat ID and optional Topic ID, then bind it from Agent settings. Ordinary group messages are observations; private messages, mentions and replies can activate the Agent. Only committed assistant text is automatically delivered. `no_reply` ends an activation without sending a placeholder. Explicit file sends and other outbound actions have persisted receipts; uncertain delivery is not blindly retried.

## Development and verification

For separate frontend and backend development:

```sh
pnpm dev:server
pnpm dev
```

The development Web app proxies the API to port 4545. For desktop development, build the native binary first, then run `pnpm dev:desktop`.

```sh
GOWORK=off go test -race ./...
GOWORK=off go vet ./...
pnpm lint:go
pnpm lint
pnpm typecheck
pnpm test
pnpm build:distribution
pnpm test:desktop-service
pnpm test:integration
```

The integration command requires a running local Docker-compatible Linux runtime and the built distributions. It exercises the downloaded installer, real Daemons, container registration, actual shell/file execution and idempotent provisioning, and removes its own containers and volumes.

For repeatable UI checks without an external model account:

```sh
node scripts/dev-provider.mjs
```

Add a Chat Completions Provider with endpoint `http://127.0.0.1:4556`, any test-only API key, and model `astral-fixture`. The fixture supports native image inspection and prompts containing `[file]`, `[slow]`, `[retry]`, and `[silent]`. `[file]` intentionally writes `validation.md` in the selected Computer's AgentHome through the real command tool. Use a disposable Agent and data directory.

See [deployment](docs/deployment.md), [the acceptance evidence](docs/verification.md), and [third-party acknowledgements](THIRD_PARTY.md). Fixture checks, live provider checks, and manual desktop checks are identified separately.

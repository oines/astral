# Contributing to Astral

Astral is pre-release software. Check the [README](README.md) for the platforms currently supported by the main branch. For a substantial change, open an issue describing the problem and intended behavior before starting implementation. Report security vulnerabilities through [SECURITY.md](SECURITY.md), not public issues.

## Development

Install the tool versions pinned in `mise.toml`, then use a standalone checkout:

```sh
pnpm install --frozen-lockfile
export GOWORK=off
pnpm dev:server
```

In another terminal, run `pnpm dev` for the Web app. The Web development server proxies API requests to the local Go server. Use disposable data and test accounts when exercising command execution or messaging. See the [README's development section](README.md#development-and-verification) for the local model fixture, desktop development and Docker integration checks.

`go.mod` and `go.sum` pin public Go dependencies; sibling Core or Models checkouts are optional. An ignored local `go.work` is only for coordinated development. Keep `GOWORK=off` when verifying a standalone change or producing a distribution.

## Before opening a pull request

- Read [AGENTS.md](AGENTS.md) and the relevant parts of [SPEC.md](SPEC.md). UI changes must follow [the UI/UX specification](docs/ui-ux-spec.md); messaging changes must follow [the IM contract](docs/im.md).
- Keep the change focused. Explain the concrete problem, resulting behavior, validation performed and any remaining limitations. Separate source checks from actual runtime or manual verification.
- Run the checks relevant to the change. The baseline commands are `go test -race ./...`, `go vet ./...`, `pnpm lint:go`, `pnpm lint`, `pnpm typecheck` and `pnpm test`, with `GOWORK=off`. Distribution and Docker checks are documented in the README. State explicitly if a required environment was unavailable.
- Regenerate OpenAPI and the SDK for API changes with `pnpm openapi-generate` and `pnpm sdk-generate`. Commit the corresponding files in `spec/openapi/` and `packages/sdk/src/`.
- Preserve third-party copyrights and licenses. Record copied assets' exact sources and terms in `THIRD_PARTY.md` and their local notices; ensure distribution notices include them.
- Do not commit credentials, signing material, real conversation data or screenshots containing private information. Use placeholders and test fixtures.

Contributions are made under Astral's [AGPL-3.0-only license](LICENSE). Third-party components retain their own licenses and notices.

# Third-party acknowledgements

Astral builds on [Astral Core](https://github.com/oines/astral-core) for the agent loop and [astral-models](https://github.com/oines/astral-models) for model protocols.

`astral-models` is a fork of [Twilight AI](https://github.com/felinics/twilight). Its HTTP/SSE support, provider errors, wire structures and reasoning replay build on the original [authors and contributors](https://github.com/felinics/twilight/graphs/contributors). The fork retains the upstream Apache License 2.0 and copyright notices unchanged. It does not claim authorship of that upstream work.

The shared frontend follows the engineering baseline identified in the Astral specification and the Memoh reference project: Vue, TypeScript, Vite, Pinia/Colada, Tailwind, Markstream, and an Electron shell. Astral's domain, API, components and desktop process integration are maintained in this repository.

Runtime dependencies also include coder/websocket, modernc SQLite, Reka UI, Lucide, Inter, Monaco, xterm.js, Mermaid, KaTeX and their transitive dependencies. Their source packages retain their own licenses. Versions and provenance are recorded by `go.mod`, `go.sum`, `pnpm-lock.yaml` and the package manifests. Electron release bundles carry Electron and Chromium's license files.

Distribution builds collect the installed Node and Go packages' license and notice files into `THIRD_PARTY_NOTICES.txt`, embedded with the Web app and copied beside the binaries. This includes the original `astral-models` license text.

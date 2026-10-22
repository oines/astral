# Third-party acknowledgements

Astral's own source is licensed under AGPL-3.0-only; see [LICENSE](LICENSE). The third-party licenses and copyright notices below remain in force for their respective components.

Astral builds on [Astral Core](https://github.com/oines/astral-core) for the agent loop and [astral-models](https://github.com/oines/astral-models) for model protocols.

`astral-models` is a fork of [Twilight AI](https://github.com/felinics/twilight). Its HTTP/SSE support, provider errors, wire structures and reasoning replay build on the original [authors and contributors](https://github.com/felinics/twilight/graphs/contributors). The fork retains the upstream Apache License 2.0 and copyright notices unchanged. It does not claim authorship of that upstream work.

The shared frontend follows the engineering baseline identified in the Astral specification and the Memoh reference project: Vue, TypeScript, Vite, Pinia/Colada, Tailwind, Markstream, and an Electron shell. Astral's domain, API, components and desktop process integration are maintained in this repository.

Runtime dependencies also include coder/websocket, modernc SQLite, go-yaml v3, the Feishu OpenPlatform Server SDK, node-qrcode, Reka UI, Lucide, Inter, Monaco, xterm.js, Mermaid, KaTeX and their transitive dependencies. Their source packages retain their own licenses. Versions and provenance are recorded by `go.mod`, `go.sum`, `pnpm-lock.yaml` and the package manifests. Electron release bundles carry Electron and Chromium's license files.

Distribution builds collect the installed Node and Go packages' license and notice files into `THIRD_PARTY_NOTICES.txt`, embedded with the Web app and copied beside the binaries. This includes the original `astral-models` license text.

Provider brand icons come from [LobeHub Icons](https://github.com/lobehub/lobe-icons), distributed as `@lobehub/icons-static-svg`. Provider and model metadata incorporates a dated snapshot of [models.dev](https://github.com/anomalyco/models.dev); each model also retains its source URL. Command Code model identities and supported endpoints come from its public model catalog. Brand names and logos remain the property of their respective owners.

LobeHub Icons — Copyright (c) 2023 LobeHub.

models.dev — Copyright (c) 2025 models.dev.

Astral uses the [Feishu OpenPlatform Server SDK](https://github.com/larksuite/oapi-sdk-go/tree/v3.12.2), `github.com/larksuite/oapi-sdk-go/v3 v3.12.2`, for application Bot long connections and event dispatch. Its upstream [MIT license](https://github.com/larksuite/oapi-sdk-go/blob/v3.12.2/LICENSE) and source notices remain in force.

Feishu OpenPlatform Server SDK — Copyright (c) 2020 Lark Technologies Pte. Ltd.

The Web app uses [node-qrcode](https://github.com/soldair/node-qrcode), npm package `qrcode` version `1.5.4` as recorded in `pnpm-lock.yaml`, to encode the Weixin authorization payload locally for display. Its upstream [MIT license](https://github.com/soldair/node-qrcode/blob/v1.5.4/license) remains in force.

node-qrcode — Copyright (c) 2012 Ryan Day.

LobeHub Icons, models.dev, the Feishu OpenPlatform Server SDK and node-qrcode are distributed under the MIT License, reproduced here:

Permission is hereby granted, free of charge, to any person obtaining a copy
of this software and associated documentation files (the "Software"), to deal
in the Software without restriction, including without limitation the rights
to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
copies of the Software, and to permit persons to whom the Software is
furnished to do so, subject to the following conditions:

The above copyright notice and this permission notice shall be included in all
copies or substantial portions of the Software.

THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,
OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN THE
SOFTWARE.

# Tool alignment and shared-prefix performance

Measured on Apple M2 / 24 GiB, Go 1.26.3 darwin/arm64, with serialized builds and measurements. No production database, model API or Mac mini was used. The fixed baseline is Astral `38a38e3` with Core `f41b9c0`. The final tested Core is `fa9e4e9354b87b77dc7bdc18808fc466965e062b`, merged as `ecbb022e4eb0e0f8e524ad4d7a32bbe09f63754e`. The Host includes the shared-prefix changes and conditional edit preparation. Per-file source hashes were recorded before and after measurement and were unchanged.

The synthetic corpus contains approximately 900k mixed text/code/log/image tokens and 227 images. The complete Host case uses Submit, context collection, an actual loopback Computer, Core/SDK, full loopback HTTP transfer and durable Run completion. A separate process receives and validates the entire body. Local wall below subtracts that mock RoundTrip interval; it is not total request latency or model response time.

| Supported images, 20 requests | Fixed baseline | Final candidate |
| --- | ---: | ---: |
| Local wall median | 36.048 ms | 33.799 ms |
| Local wall P95 | 41.948 ms | 34.402 ms |
| Local wall maximum | 49.961 ms | 36.145 ms |
| Complete wall median, including mock receiver | 202.317 ms | 190.009 ms |
| Mean cumulative allocation per request | 33.190 MiB | 33.207 MiB |

All 20 final requests passed complete-body and frozen-request reconstruction checks. The nine tool declarations remained exactly equal across the final wrapper adjustment. Dynamic request identities differ between independent runs, so this is not a claim that bodies from different Sessions are byte-identical. Timing remains within previous-run variation; the measured change is not evidence of a proportional speedup in the already-fast visual path.

The non-visual feature initially exposed repeated encoding and hashing of removed historical images. With that feature and the old planner, a real Core+SDK SQLite Session took 1.85–1.87 seconds and cumulatively allocated approximately 1.04 GB. The final planner completed the same successful path in 26.343 ms with 16,951,704 allocated bytes. Its actual request contains zero images, approximately 630k tokens, and a 2,702,649-byte body. This measurement consumes the whole body offline but excludes Astral Host and actual networking. The older SDK rejected this input, so its rejection cost is not a successful performance baseline.

A second non-visual case places 256 KiB of text between two adjacent-image groups. It completed in 26.221 ms; the frozen recipe had 234 changes and 18,775 bytes of values, retained the large text by reference, and passed digest reconstruction. Only edit now advertises argument preparation. A separate private-helper microbenchmark confirms why: a copying no-op with 1 MiB arguments added about 6.34 MB per preflight/dispatch pair; absence of that capability adds no allocation in the helper. This is not complete tool latency.

The real read-tool case performs two model requests. The observed ToolEnd event to the next dispatch was 29.488 ms versus baseline 29.121 ms. This is a single event-observation measurement, not the precise filesystem-return timestamp or a distribution. The two requests together used 73.976 ms local wall; their sum must not be described as a single request's latency.

## Retained memory and storage

An independent 20-request GC/storage run on the final planner before the subsequent capability-only wrapper adjustment measured live Go heap at 25.463–25.637 MiB, compared with baseline 25.442–25.628 MiB. This is not RSS. Over those 20 requests, image payloads remained at 137 unique values / 11,622,718 bytes, payload references stayed 226, and configuration entries stayed 2. No repeated Configure or copied image accumulation was observed.

The same run added 121 entries, 20 attempts, 70,823 bytes of entry envelopes and 112,476 bytes of attempt envelopes. The main Core database grew 335,872 bytes; the 17,110,392-byte WAL was reused without length growth. These are retained file lengths and logical payload sizes, not measured physical disk write volume. They do not prove zero write amplification or zero SSD wear.

An additional 100-small-request Core+SDK case measured local median/P95/max of 2.662/3.402/4.455 ms. It retained 200 real new messages, with live heap increasing from 2.44 to 2.95 MiB; this active-context growth is expected. The configuration count stayed 1. The WAL reached a stable apparent length by step 80. Whole-history disk growth remains expected because Compact preserves original history.

## Reproduction and gates

The existing `TestSuccessScaledContext` fixture in `internal/harness/success_scaled_context_unix_test.go` supplies the public synthetic corpus and complete Host path. Raw results, CPU profiles, exact commands, input hashes and replay sources are retained in the task's performance evidence directory. The seed SHA-256 is `760ff61f76a91ffd8c84559dded23cbc26b74446ebccaaa79631f61cf39dbd60`.

Core regressions cover source immutability, same-role multi-image swaps, retained opaque/tool fields, separated image groups, empty content, and randomized frozen-request reconstruction. Core's final Linux/macOS/Windows race matrix passed, including Linux pure Go fallbacks. The full Host Harness race passed in 191.167 seconds before the capability-only wrapper adjustment; the final wrapper has focused SDK/Host capability, binding and real edit race coverage. The release workflow validates the final pinned source again on all supported native platforms.

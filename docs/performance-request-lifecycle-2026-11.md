# 请求生命周期性能验收（2026-11-05）

本记录补充 `performance-host-2026-11.md` 的现场诊断，区分正常请求、异常 Compact 恢复和长期归档三个工况。累计分配不是同时驻留内存，异常恢复的 42 秒也不是正常请求延迟。

## 原因与改动

不可变图片历史在 Core 投影、估算、校验、冻结和 SDK 编码之间反复经过完整 JSON 复制。Compact 候选只改变少量工具文本，却重新处理整份图片历史；SDK 估算还为计算图片成本构造发送用的 data URL。多种临时表示同时存在，引起大量分配与 GC。

本次改变数据经过这些阶段的方式：

- Core 私有流水线共享已确认的不可变正文，修改时只复制发生变化的容器。旧可变回调仍收到独立副本；只读投影、估算、上下文收集、校验和执行准备需要明确选择接口。
- Session 加载时验证有效历史正文，后续私有 prepare 复用这个边界；每轮仍检查工具时间线、消息协议和预算。公开 Read/Project 保留完整校验。失败回复、control、已压缩或被替代结果维持原可见性，未完成工具批次仍能打开恢复，空工具结果仍投影为空文本。
- 大小预算和上下文来源摘要共享一次遍历。无提前 usage facts 的 generation 并行计算两种独立摘要；有提前 facts 的路径将剩余冻结摘要与只读 SDK 编码并行。所有路径在返回错误或提交前等待完成，没有跨请求摘要缓存。
- SDK 估算验证真实图片后使用占位值计算成本，不构造完整发送 URL，也不重复扫描图片。发送正文只编码一次；普通重试复用本次执行的字节，修复请求重新编码。
- 大文本预分配所需容量，转义使用标准 JSON 语义；图片字符串直接进入最终缓冲区，避免临时完整 data URL。大 Base64 校验使用固定版本的 MIT [moego](https://github.com/MoeForge/moego)，保留纯 Go 及兼容路径。
- 附加 Raw JSON 直接解码到同一标准编码器，去掉“先编码、解码、再次编码”的中间步骤；nil、空和非法数据保留原兼容分支。Compact 复用固定数据，只改变截断的工具文本，没有引入分段压缩。

保留预算校验、既有摘要字节、冻结请求重建、持久化格式及 SDK 独立性。编码失败发生在持久化 attempt 创建前，避免留下无法执行的 pending attempt。

## 正常大上下文：完整 Host 路径

机器为 Apple M2、24 GiB、Go 1.26.3、macOS arm64，默认 Go GC，未启用 race/profile。最终 Core 为 `7c7ea56d4b1780ca2c9462e933a6df711602e2f4`；Host 代码为 `76837ee`，正式复跑使用公开模块 `v0.0.0-20261104193014-7c7ea56d4b17`、`GOWORK=off`，无 replace 或 overlay。284 个发布模块源文件 hash 与干净 Core checkout 逐项一致，命令、module Version/Dir/Sum 和二进制 SHA 均封存。

测试执行实际 `Service.Submit`、环境收集、Computer RPC、Core、SDK 和 SQLite 提交。900k 为真实 SDK fallback 估算约 899,581–900,759 tokens 的综合输入，包含 227 张冻结 PNG/JPEG，图片在历史中段，发送正文约 21.31 MB。文本混合代码、JSON、换行、引号、反斜线、HTML 和中文；模型窗口 1M，输出预留 16k、安全余量 4096。它不是仅修改窗口，也不是上游 API 报告的真实 900k usage。替身回复不提供 usage，完整 SDK 估算实际执行。

本地墙钟从提交到 durable run-end，扣除全部模型 HTTP RoundTrip；包含投影、估算、校验、两种摘要、最终编码、回复解析和落库。`dispatch` 为进入 RoundTrip，不是 socket 首字节。上传、服务端处理和响应等待不属于该本地指标，完整总时间另行保留。

独立子进程接收器完整读取请求、验证 JSON 和 SHA 后才返回。父进程使用真实 loopback HTTP Transport；计时后对每次请求重新构造，验证冻结摘要及公开 SDK 正文 SHA。隔离接收器属于测量修正，不计作产品加速。

| 工况 | 次数 | 本地墙钟最小 / 中位 / 最大 |
| --- | ---: | ---: |
| 900k mixed，正式公开依赖 | 20 | 33.32 / 34.69 / 36.52 ms |
| 900k mixed，独立 workspace 对照 | 20 | 33.16 / 33.98 / 35.45 ms |
| 500k mixed | 20 | 26.36 / 26.93 / 29.19 ms |

正式 900k 每次累计分配约 34.77 MB、进程 CPU 约 59.77 ms，完整 loopback 总时间约 208.21 ms。并行摘要缩短墙钟，并不等于减少同等 CPU。测试进程含构造与校准的峰值 RSS 为 246.79 MB，不是生产稳定常驻值，也不能声称 900k 整进程低于 100 MB。

两次真实 Computer `read` 工具任务各包含两次模型请求。第二次进入发送的本地间隔为 38.04 / 40.30 ms，包含前次回复处理、工具调度、RPC 和持久化；整任务本地合计约 75.2–75.5 ms，不能把两次之和当成单请求。每次实际读取的结果都在后续 API 正文中核验。

以上证明指定 M2 工况达到 50 ms 门槛，不保证任意硬件、并发负载、数据或网络的同一上限。Core 的 ARM32/ARM64、Host 整树 ARM64 及 harness 测试的两架构编译已通过，这是兼容证据，不是树莓派延迟实测。Host 整树 ARM32 仍被飞书 Lark SDK v3.12.2 的 `MaxInt64` 转 `int` 常量溢出阻断；本轮不改第三方 SDK，树莓派整包验证范围为 64 位系统。

## 原始真实 usage 路径与冷加载成本

使用封存数据库的原始成功回复边界，保留原 500k 模型配置和真实 usage：249,943 input + 6 output。原 Generation、配置和重建请求的来源摘要一致；`InspectContext=249949` 且不调用 SDK 估算器，明确证明 anchor 命中。计时 Prompt 仅对空请求和新增尾部估算，历史图片未重新计数。新离线回复省略 usage，未填造统计。

同一 process 接收器、原始 Responses SSE、完整正文及公开 SDK 重建核对：

| Core | 三次本地墙钟范围 | 改动 |
| --- | ---: | --- |
| `cc66dd9` | 59.77–61.19 ms | 对照 |
| `14ea495` | 54.28–57.25 ms | 剩余摘要与 SDK 编码并行 |
| `aa8cdd8` | 51.54–51.87 ms | Raw JSON 去掉中间编码 |
| `7c7ea56` | 46.01–49.56 ms | 加载边界验证正文，省去每步重复验证 |

此工况为 Core＋SDK，不是完整 Host；真实 usage 约 250k，不能称为真实 provider 900k 验收。所有版本发送正文均约 19.33 MB，SHA、streaming、冻结重建、真实用量校准均核对。

独立临时 overlay 只诊断冷加载验证：1546 条记录约 6.91 ms，1061 条约 4.41–4.47 ms。它是 Session 打开／重新加载的一次性成本，未被消除；每轮请求不再重复。正式公开模块二进制未使用 overlay。打开数据库、读取和解码、历史 Fork 等其余冷启动工作仍不属于每次工具结束后的热请求指标。

200 次独立微测进一步归因：原 canonical 平均 19.29 ms、分配 5.93 MB；metadata 归一化 CPU 链约 6.4 ms/次，其中第一遍 Marshal(raw) 约 2.2 ms。直接解码后平均 16.85 ms、4.81 MB，digest SHA 相同。微测无 HTTP/DB，不能代替端到端验收。阶段 overlay、微测和正式测量分开保存，采样调用树的 inclusive 时间不能相加。

## 异常 Compact 与 IM 暂停

原始异常输入约 32 MB、352 张图片。相同输入、摘要目标、待处理 ID 和错误的恢复重放在 `c0f4d42` 测得约 523 ms 墙钟、546 ms CPU、260 MB 累计分配；仍是 51 次 prepare、0 次 HTTP、0 次工具，保留 `634610 + 12800 + 4096 > 500000` 的错误。这是异常恢复路径，且是该历史提交的测量，不冒充最终版正常请求延迟。

Host 同时保留失败后的持久暂停：平台消息继续接收和归档，暂停绑定不再向 Core 累积上下文。模型或连接配置变化、显式恢复触发核对；恢复必须先成功 Compact 并通过完整预算检查，再继续处理。仅上游服务自行恢复不会创建定时重试。摘要输入无法容纳与可退避网络错误仍按各自类别处理。中英文用户文档已说明等待与恢复方式。

## 长期内存验收

相同负载执行 1000 次工具、2000 次普通 SDK 请求、20 次实际 Compact。每步独立文本和 PNG，归档增长而活动窗口有界。最终版使用上述同一公开依赖二进制；热区无强制 GC、无 race/profile，GOGC/GOMEMLIMIT 未设置。

| 指标 | 旧基线 `6793888` | 中间版 `cc66dd9` | 最终公开依赖 `7c7ea56` |
| --- | ---: | ---: | ---: |
| 热区墙钟 | 81.38 s | 68.69 s | 69.88 s |
| 累计分配 | 76.76 GB | 37.75 GB | 37.65 GB |
| 自然 GC | 6854 | 4464 | 4469 |
| 自然峰值 RSS | 129.61 MB | 106.30 MB | 114.26 MB |
| 最终自然 RSS | 129.43 MB | 106.10 MB | 114.08 MB |
| 采样峰值 HeapAlloc | 56.85 MB | 33.20 MB | 40.39 MB |

相对旧基线累计分配减少约 50.95%，最终 RSS 减少约 11.86%。最终单次 RSS 高于中间版约 7.52%，耗时也高约 1.72%；如实保留，不据这些单次结果推断因果或宣称所有指标单调变好。

三版均归档 131.39 MB 独立正文、12,022 条 entry，最终活动上下文约 406 KB、活动峰约 7.16 MB。工具/API/Compact/attempt 次数、SDK 估算次数与峰值、总发送字节一致，entry 大小仅有数 B 的 ID／时间 envelope 差异。旧基线计时含极少量统计工作，差异注明于比较 JSON，不用于声称亚毫秒精度。

最终版最后 250 步归档增加 32.85 MB，RSS 仅从 113.82 MB 到 114.08 MB；旧 900 份文本及图片 backing 在自然 GC 后均无存活弱引用。这支持本负载的历史正文未持续滞留，不外推为所有对象的无限时长无泄漏证明。

100 MB 目标尚未达到。结束后单独强制 GC 的约 3.89 MB 活跃堆不替代自然 RSS。以上范围是 Core＋SDK＋SQLite＋进程内 discard 接收器，不代表全部 Host、IM 连接、桌面界面或独立 900k 工况的内存。

## 重放与证据

测试显式 opt-in；正常 CI 不读取私有 fixture，不发真实 IM 消息或访问外部模型。封存数据库不进入仓库、不原地写入。仅看 Go PASS 不足够，需核对 result 的 success、逐请求重建及 `all_rounds_local_under_50ms`。

```sh
GOWORK=off go test -tags astral_prepared_execution -c ./internal/harness -o /tmp/astral-harness-perf.test
ASTRAL_SUCCESS_SCALED_HOST=900k \
ASTRAL_SUCCESS_SCALED_HOST_MODE=prompt \
ASTRAL_SUCCESS_SCALED_HOST_TEXT_STYLE=mixed \
ASTRAL_SUCCESS_SCALED_HOST_ROUNDS=20 \
ASTRAL_SUCCESS_SCALED_RECEIVER=process \
ASTRAL_SUCCESS_SCALED_SEED=/absolute/path/large-images-request.json \
ASTRAL_SUCCESS_SCALED_HOST_OUTPUT=/absolute/path/new-output \
/tmp/astral-harness-perf.test -test.run '^TestHostSuccessScaledContext$' -test.v
```

工具任务含两次请求，需逐段检查。严格真实 anchor 使用 `scripts/run-success-baseline.py --suite lifecycle --scenario captured --receiver process` 和 `ASTRAL_SUCCESS_REPLAY_REQUIRE_ANCHOR=1`，指定封存 fixture、原 target/head/attempt。长期内存入口为 `TestSQLiteLongRunMemoryReplay`，环境 `ASTRAL_MEMORY_REPLAY=1` 与新的 `ASTRAL_MEMORY_OUTPUT`。

开发机主要证据：

- `~/.local/state/astral-performance/baselines/final-release-pin-20261105-7c7ea56/`：正式模块身份、284 文件源码对照、统一二进制 SHA、官方 900k 结果、最终内存及三版比较、独立冷加载诊断和 45 文件 SHA manifest。
- `/tmp/astral-strict-captured-anchor-20261105-7c7ea56-process/`：三次真实 anchor 重放；对照接收器身份和前序版本在 `/tmp/astral-anchor-process-ab-20261105/`。
- `~/.local/state/astral-performance/baselines/request-digest-micro-20261105-{14ea495,aa8cdd8}/`：独立 CPU／allocs profiles、原始对照、overlay 和源码／二进制 SHA。
- `~/.local/state/astral-performance/baselines/request-host-final-cc66dd9-20261105/`：中间版结果保留作历史对照，不替代最终结果。

## 正确性与平台检查

Core `7c7ea56` 本机全量 race 的 15 个测试包通过，日志 `/tmp/astral-final-review-20261105/core-all-race.log`。Host 最终四包完整 race、vet 和 Core vet 通过，ARM 构建边界如上；检查前后精确源码及 pin 均未变，日志 `/tmp/astral-final7c-checks.mz7RO0/results.json`。Host 工具句柄计数改为 `int64`，保持原 `2^53−1` 上限和 JSON/字符串格式，独立目标 race 与 ARM32 测试编译均通过。前端、网站、安装器与发布工具已有本地检查记录；最终源码仍须通过 PR 与 Release 工作流的原生签名、公证、更新和安装验证，不能以本地测试替代发布成功。

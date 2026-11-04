# Host 性能验证记录（2026-11）

实际诊断显示，主要分配热点是直接触发恢复反复重建冻结请求。最终实现只保留模型不可用时的 IM 暂停／退避、直接触发恢复的持久水位，以及追赶期间跨页复用的轻量 audit。Summary／目录冷读和 Outbox 索引等独立优化已撤出。第一轮开发版安装后问题仍未解决；最新收窄版本 `3478440` 尚未做真实安装后的同工况复测，不能宣称生产 CPU 或 RSS 已恢复。

## 环境和比较方式

离线测量在开发 Mac 上进行：Apple M2、8 个逻辑 CPU、24 GiB 内存，macOS 15.7.5（24G624），`darwin/arm64`，Go 1.26.3。IM 故障与 Outbox 等 fixture 使用临时 SQLite；跨页冻结请求实验使用公开 Core 内存 Store。Core 依赖保持 `v0.0.0-20261102133800-0139c33cb755`，SQLite 驱动为 `modernc.org/sqlite v1.41.0`。本次没有修改 Core 或 `go.mod`。

离线测试的对照方式不同，不能混为同一类前后对照：

| 测量 | 旧路径如何取得 | 可回答的问题 |
| --- | --- | --- |
| 跨页恢复 | 同一测试二进制、同一真实冻结请求，每页清空 audit 对照连续恢复期间复用 audit | 同一不可变请求跨 4 页需要重建几次、产生多少累计分配 |
| 冷查询 | 同一测试二进制、同一 SQLite fixture，先执行保留的 `View/open` 路径，再执行新 `Summary/Store` 路径 | 改变查询入口后，是否还会无故加载、保留执行实例 |
| IM 模型不可用 | 主分支 `eef8a860c6b09293bf2988293b3bee6aca5cc200` 与本改动分别编译测试二进制，使用同一 workload 文件 | 持续到达的事件是否重复触发真实 Core compact 失败 |
| Outbox | 同一测试二进制、同一数据库，比较旧的全量读取和解码子路径与新索引查询 | 调度一次待发送记录时，历史 accepted 记录的读取和解码开销 |

除单独标注的 `-race` 验证外，离线性能数字均来自未启用 race 的测试。它们是短时观测，不是长期统计；不能据此保证每台设备的固定加速倍数。Fixture 不读取生产聊天内容，也不请求外部模型服务。下面的实际实例诊断只列函数归属、计数和内存统计，与合成 fixture 分开记录。

## 实际实例：累计分配的主要来源

诊断 Server 实例（PID 79685）启动约 35 秒时，Go `TotalAlloc` 已约 **56 GB**。该值表示进程启动以来的累计分配，不是当时仍存活的对象大小。

该实例 heap profile 的 `alloc_space` 采样总量为 **53,030.92 MB**；以下 MB 沿用 profile 输出单位，调用链采用累计归属：

| 累计分配归属 | profile 显示量 | 占总采样量 |
| --- | ---: | ---: |
| `handoffDirectObservations` 恢复链 | 51,460.92 MB | 97.04% |
| 其中 `RebuildRequest` | 48,537.04 MB | 91.53% |

`RebuildRequest` 是 handoff 链中的子路径，两行不能相加。现场调用链为 `inbox → handoffDirectObservations → WakeObservations → IMInspector.Handled/readAttempt → AttemptHistory.Request → RebuildRequest → applyRequestChange → encoding/json`。此前每个恢复页都会新建 inspector，跨页反复重建同一个长冻结请求；handoff 水位又只在内存，重启从绑定起点补扫。这直接解释了采样中大量序列化、复制与 GC 工作。

这份 profile 没有证明 Owner 目录查询或 Outbox 是此次持续高分配的主要来源。因此它们的独立实验优化已撤出最终修复，避免把未验证的重构混入已定位热链的修复。

一次显式 GC 后记录的 `HeapAlloc` 为 **219,940,136 B（约 210 MiB）**，同次记录的进程 RSS 为 **970,112 KiB**。累计分配、GC 后活跃 Go 堆和 RSS 是不同指标。上述 56 GB 不表示 56 GB 同时驻留，也不能仅凭累计分配量判定泄漏；单次 GC 后堆较小，同样不能证明长期绝无泄漏。现有证据支持“主要存在反复重建产生的分配周转”，而不是把全部高 RSS 归给某个常驻对象集合。

## 实际安装观察：第一轮开发版仍未解决

第一轮开发版安装前后曾记录以下独立观察点：

| 观察阶段 | 当时记录的进程 CPU | RSS |
| --- | ---: | ---: |
| 安装第一轮开发版前 | 149% | 2.58 GiB |
| 安装后的早期观察 | 114% | 3.38 GiB |
| 后续一次观察 | 31.92% | 2.52 GiB |

这些观察没有组成固定负载、固定采样窗口的 A/B，不能据此计算安装带来的加速或内存下降比例。安装后的早期 CPU 和 RSS 仍然较高，第一轮开发版没有解决现场问题；后续一次下降也不能当成稳定修复的证明。最新收窄版本 `3478440` 的真实安装后结果尚未测量，以下离线结果不能代替这一验收。

## 保留修复：长冻结请求跨页恢复

运行命令：

```sh
ASTRAL_IM_RECOVERY_BENCH=1 ASTRAL_IM_RECOVERY_RESULT=/tmp/astral-im-recovery-cross-page-performance.json go test ./internal/harness -run '^TestIMRecoveryLargeFrozenRequestCrossPageMeasurement$' -count=1 -v
```

原始数字在测量机器的 `/tmp/astral-im-recovery-cross-page-performance.json`。Fixture 用真实 Core 执行一个合成 `read` 工具，结果含约 16 MiB 文本和一张有效 PNG，再冻结包含 **305 条直接观察**的 generation request。序列化请求为 **16,947,519 B**，恢复分为 **4 页**，每页最多 100 条；工具调用、工具结果和观察都通过真实 Core 公共 API 建立。这里的大正文是合成工具读取结果，不是把普通 IM 消息伪造成 16 MiB。

同一测试二进制分别模拟旧策略（每页清 inspector）和新策略（追赶期间跨页复用）。构造被冻结请求共调用离线模型替身 2 次，恢复审计不新增模型请求。测量前后各执行两次 GC，以清除 `encoding/json` 的 `sync.Pool` victim 缓冲；构造排除在测量之外。

| 4 页合计 | 每页重建的旧策略 | 跨页复用的保留策略 |
| --- | ---: | ---: |
| 墙钟时间 | 3.535078625s | 0.857535459s |
| 累计分配字节 | 1,851,244,880 B | 517,124,024 B |
| 同一冻结请求的真实重建次数 | 4 | 1 |
| 两次 GC 后 HeapAlloc 相对基线增量 | 33,976 B | 28,872 B |
| 保留 audit 的 key 长度合计 | 7,962 B | 7,962 B |

Key 长度合计不包含 Go map、字符串头和分配器开销，不能把 7,962 B 当成整个 cache 的内存占用。Audit 仅保留事件身份和 summary-through，不保留 Request、Message 或工具正文；追平已投递归档末水位或更换 Core handle 后释放。305 条覆盖判断均为 `covered`，审计未改变 Session 版本或触发额外运行；新事件、新 attempt 和 Core 换代另有回归验证。

持久 handoff 水位只跨过已有 Core receipt／request 证据确认的连续前缀。条件 wakeup 仅为 accepted/existing 时仍等待再次核对，busy 和错误不跳过未确认触发；保留原 failed/stopped 规则及原始 driving input 的队列归属。水位绑定 Agent、Session、binding、conversation 和起始序号；归档清理读取同一水位，重启不重新补扫已确认前缀。它使用既有 operations 表，不新增数据库格式迁移。

## IM：额度不足时持续到达消息

运行命令：

```sh
go test ./internal/channels -run '^TestIMUnavailableWorkloadMetrics$' -count=1 -v
```

主分支对照在隔离 checkout `/tmp/astral-host-perf-baseline` 中运行：源码为上述 `eef8a860…`，只复制同一份 `internal/channels/inbox_unavailable_workload_test.go`，未加入修复代码。这是分别编译的测试二进制对照，不是拿生产进程或已安装应用直接测量。

Fixture 先通过真实 Core `InjectContext` 建立 40 条约 1.5 KiB 的可读历史文本，再通过公开 Core Store `Commit` 写入 64 条独立、已归档且未应用的旧 pending，模拟上次进程已经接收输入后停止的状态。旧 pending 使用各事件自身的 ID、规范化 Observation 和 digest，没有合并成一个输入。随后将模型窗口从 128,000 调到 32,768，最大输出仍为 4,096，以确保下一次投递确实触发自动 compact。

测量开始后连续归档并投递 3 条新事件。模型 HTTP transport 是进程内替身，返回结构化 HTTP 402 `insufficient_quota`；请求仍经过真实 Core、Host modelbridge 和 SDK 错误解析。测量包含这 3 条事件的归档和投递，排除 fixture 构造；分配统计前执行 GC。测试验证最旧 64 个 pending ID 保持不变，并验证 SDK 结构化错误穿过 Core/Host 后仍可分类。

| 三条到达合计 | 主分支旧实现 | 修复后 |
| --- | ---: | ---: |
| 实际模型／compact 失败调用 | 3 | 1 |
| 墙钟时间 | 347.067ms | 96.140ms |
| 累计分配字节 | 108,726,624 B | 31,902,808 B |
| 分配次数 | 375,909 | 112,039 |
| Core 中保留的 Context pending | 67 | 65 |

“修复后”列来自当时实验工作树的测量快照，并非最终 `3478440` 测试二进制的重新测量，不能据此计算最终版本的改善比例。持续消息不再反复调用失败维护这一行为，在后续收窄后的定向测试中仍有验证。

修复后第一条输入已经被 Core 接收但维护失败，仍保留在 pending；其后两条保留在 IM archive，等待恢复，未再调用 Core。旧实现则把三条都加入 pending 并分别重试失败维护。两者都保留最旧 64 条，数字差异不表示修复丢弃了消息。

持久暂停只阻止该绑定继续投递 Core，不停止平台接收和归档。凭据、权限和额度错误等待显式会话输入或模型／供应商配置变化；429、5xx 和网络错误按照持久化退避时刻重试，遵守 SDK 给出的更长 RetryAfter。新 IM 事件和重启都不会绕过它。已有错误通过原 Session 错误状态显示，逐事件 receipt 在恢复时继续核对。

## IM：恢复和完整积压工况

常规 CI 的恢复测试使用 8 条旧 pending、1 条触发 402 的输入，以及暂停期间归档的 30 条消息。它先验证重启仍暂停，再在**未修改 32k 配置**的情况下模拟充值并显式恢复，确认真实 compact 成功、旧 pending 清空。之后恢复 fixture 原有的 128k 窗口，回放余下归档，以免把刻意紧窗口引起的重复压缩混入普通恢复正确性验证。

39 条事件均恰好处理一次。未改配置的恢复发生 1 次模型调用／compact，之后 30 条回放发生 0 次模型调用。一次未启用 race 的恢复为 676.294ms；最终定向 race 验证中为 10,404.173ms，该项总计 19.22s。race 数字仅用于验证，不与上表未启用 race 的性能数字比较。

原 64 条旧 pending 加 30 条后续归档的完整工况仍可显式执行，不进入普通 CI：

```sh
ASTRAL_IM_RECOVERY_WORKLOAD=1 go test ./internal/channels -run '^TestInboxLargeBacklogRecoveryWorkload$' -count=1 -v
```

这项保持 32k 窗口和 4k 最大输出，不扩大恢复阶段窗口。实际结果：**95 条事件各处理一次，恢复 20,028.172ms**；初始清理 pending 产生 2 次 compact，后续回放产生 13 次，总计 **15 次成功 compact**。全部调用使用离线替身，不含真实模型服务耗时。

该数字是范围收窄前的工况记录，不是最终版本吞吐验收。紧窗口会使这个 fixture 频繁 compact，不能把 20.03s 全部解释成普通消息投递成本，也不能从中推断生产环境每条消息都 compact。先前把整个 95 条恢复放进常规 `-race` 测试时触及 fixture 的 120s 预算；因此将 64 条故障压力、较小的完整恢复正确性和显式大积压 workload 分开，而没有提高全局超时或修改 Core。

## 历史实验：未采用的冷目录／receipt／子代理查询

本节仅保留已经撤出的实验及其原始数字。最终改动已恢复完整 `View` 目录接口、原 receipt／ResumeContext 路径和子代理巡检方式，删除对应冷读实现与专用测试；下列命令是实验提交 `04cfdf4` 的历史复现方式，不属于最终实现的验证命令。

运行命令：

```sh
ASTRAL_COLD_DIRECTORY_BENCH=1 go test ./internal/harness -run '^TestRepresentativeColdDirectory$' -count=1 -v
```

原始汇总保存在测量机器的 `/tmp/astral-host-cold-query-performance.json`，记录时间为 `2026-11-04T05:29:20Z`。临时文件不随仓库分发，下面保留关键数值；测试日志还会给出 CPU profile 路径。

Fixture 包含 82 个 Session：47 个普通根会话和 35 个已结束子代理。每个有效上下文含 3 张合成的 320×240 PNG；大部分窗口约 1 MiB，一个根会话另加入 9 条约 4 MiB 的合成 Context，以构造一个较大窗口。总序列化有效上下文为 **124,632,823 B**，最大窗口 **38,811,884 B**，构造耗时 **89.86679125s**，不计入查询结果。

这是用于比较按字节加载成本的合成 fixture，不表示普通 IM 消息有 4 MiB。构造使用离线模型和工具替身，固定 token 估算并关闭 compact；它不评估模型 token 预算或普通消息的维护吞吐。生产参考的 82 个窗口总量为 338,066,320 B，本 fixture 没有复现其全部体积。

测量执行三轮固定工作：逐 Session 查询目录摘要和一个 input receipt，再检查 35 个已结束子代理。旧 receipt 路径会先 `open`；旧子代理对照只执行其必需的 `open`，省略其他无操作维护，因此不是完整旧 worker 的重演。两种路径各从新建 Harness 开始。

| 三轮合计 | 旧 `View/open` 路径 | 新摘要／直接 Store 路径 |
| --- | ---: | ---: |
| 墙钟时间 | 7.115773083s | 0.436290459s |
| Go CPU profile 采样时间 | 6.19s | 0.44s |
| 新加载并保留的 runtime 数 | 82 | 0 |
| 已加载 runtime 的逻辑 Context 字节 | 124,632,823 B | 0 B |
| 累计分配字节 | 2,543,573,408 B | 162,118,608 B |
| 显式 GC 后 HeapAlloc 增量 | 134,582,832 B | 11,024 B |
| 显式 GC 后 HeapInuse | 143,638,528 B | 5,545,984 B |

CPU 数字来自 10ms Go profile 采样，不是操作系统 CPU 百分比。Context 字节指有效窗口及其必需引用的序列化记录，不是读取整个 Rollout，也不等于 Go 堆或进程 RSS。这项测量不包含 HTTP、SSE、前端渲染或模型网络延迟。

## Outbox：未采用的索引实验

以下是撤回前执行的实验命令；最终源码不保留该专属 benchmark、SQL 查询或索引迁移：

```sh
go test ./internal/persistence -run '^$' -bench '^BenchmarkOutbox1435AcceptedOnePending$' -benchtime=3x -count=1
```

Fixture 为 1,435 条 accepted 和 1 条 pending Outbox，均带普通文本 parts。每条文本约 1.5 KiB。下表是 Go benchmark 输出的每次操作数字，三个采样操作；数据库构造不计入结果。

| 每次调度查询 | 旧全历史读取＋JSON 解码 | 新索引工作集查询＋解码 |
| --- | ---: | ---: |
| 时间 | 20,353,736 ns/op | 516,611 ns/op |
| 分配字节 | 9,722,482 B/op | 7,144 B/op |
| 分配次数 | 34,492 allocs/op | 38 allocs/op |

旧侧是同一二进制内保留的 `Store.List` 加逐条解码子路径，**不包含旧调度器的排序和完整控制流程**，因此是这部分历史读取成本的保守对照。新侧包含 pending 筛选、同组前置片段依赖、同聊天 dispatching 排斥、每个聊天队首选择及返回记录解码。两侧都不发送平台消息，不测量平台网络延迟。

实验曾新增三个表达式索引，并用 `EXPLAIN QUERY PLAN` 验证查询路径。这些查询和索引已经撤出，最终 Host 目标保持格式 6，Outbox 调度恢复原实现。本段只保留实验记录，不作为最终性能收益。

后续小 fixture 审查了写入代价：使用原 `Store.Open` 的 WAL 和 `synchronous=FULL`，每个新库先放入 50 条 accepted，再清空 WAL；随后 20 次发送分别写入 pending、dispatching、accepted，共 60 次真实 `Store.Save`。索引有无的顺序交错，执行三轮，无生产数据或迁移账本变更。

| 每 20 次发送／60 次 Save | 无三个实验索引 | 有三个实验索引 |
| --- | ---: | ---: |
| 墙钟时间中位数 | 12.906ms | 14.769ms |
| WAL 追加字节 | 1,112,432 B | 1,854,032 B |

本 fixture 每次发送额外追加 37,080 B WAL，等于每次 Save 多 12,360 B（3 个页面 frame）。这是逻辑 WAL 追加量，不是设备物理写量；小树、短时墙钟数字不能外推生产写入延迟。原始三轮记录和临时测试源码在测量机器的 `/tmp/astral-outbox-index-write-audit.txt` 与 `/tmp/astral-outbox-index-write-audit_test.go`。已确认的生产主热点是直接触发恢复的冻结请求重建，不应以 Outbox 读基准替代这一根因。

## 尚未测量和保持不变的边界

已经取得第一轮开发版的现场 CPU/RSS 观察和诊断 heap 归属；尚缺最终收窄版本 `3478440` 的真实安装后同工况测量，因此没有最终生产内存改善结论。目录冷读和 Outbox 的实验收益不属于最终修复，不能用其约 16 倍、39 倍的隔离查询时间差包装最终性能。**没有实现已打开空闲 runtime 的 eviction**，既有实例生命周期保持不变。没有删除或缩短历史，没有把有界 Core 窗口说成整个 Rollout，没有改变 Core 的即时接收、预算或 compact 算法。

首次没有持久水位时，新版本仍需核对未确认历史，现场追赶时间需实际测量。本报告的离线 fixture 是已结束的单次运行，不以持续压测推断生产表现，也没有发送真实 IM 消息。

## 已完成的验证

收窄后的验证包括：

- `go test ./internal/persistence -count=1`：通过，0.605s；Host 目标恢复格式 6。
- Channels 的 handoff、暂停、accepted wakeup 崩溃恢复、归档保留及原 Outbox 定向测试：通过，3.738s。
- 同类 Channels／Persistence 加迁移的定向 `-race`：通过，48.546s / 2.930s。覆盖 64 条旧 pending 的失败风暴、重启后保持暂停、配置／显式恢复、SDK 错误分类与 RetryAfter、逐事件回放，以及 durable handoff 的绑定身份和未确认触发保留。
- 跨页 audit、Core 换代、追平后释放、新 attempt 可见性等 Harness 定向 `-race`：通过，14.899s。
- 撤回目录／API 独立优化后的 Harness／HTTP API 定向 `-race`：通过，30.815s / 2.429s；Web/Desktop typecheck、app store 8 项测试通过，OpenAPI/SDK 恢复原契约。
- 显式 16,947,519 B／305 条跨页 workload 已实际执行，重建次数 4→1，数值见本报告保留修复段。
- 本轮中英文恢复说明的 `pnpm check:website` 已通过。

较早的主分支 IM 402 对照、95 条恢复、冷查询和 Outbox 读写实验均已实际执行，原始数字按测量阶段保留。目录／Outbox 专属实验测试随对应实现撤出，不作为最终提交的验证入口。以上通过不等同于最终版本的生产 CPU/RSS 验收。

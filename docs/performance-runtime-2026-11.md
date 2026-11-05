# Astral 运行期开销修复与验收（2026-11-05）

本轮针对完整 Host 的无效后台工作，不修改 Core SDK、数据格式、认证持久性或 GC 参数。请求性能、待命 CPU、长期内存与实际磁盘写入分别验收；不能用其中一项代替另一项。

## 现场事实与定位依据

Mac mini 使用官方签名 v0.1.19，Go 二进制 SHA256 为 `cee198c059a956f973e093134b22ad561a38fe9a189105b6e1b9481da67a34f9`。在没有原生采样器的 80.45 秒窗口内，Go 进程消耗 11.95 秒 CPU，即单核平均 14.85%；RSS 为 372.22 MiB。21 个观测点没有持久化 started Run 或 current attempt，Rollout 版本及 attempt 数未变化。持久状态观测不等于完整 runtime 快照，因此此处称为自然待命观测，不宣称证明所有线程均无任务。

另取 10 秒、10 ms 间隔的 macOS `sample`，取得实际签名二进制并按 Mach-O UUID、加载地址及 Go PC line table 消除 ASLR，188 个程序 PC 全部解析成功。它是线程墙钟栈采样，**不是 Go CPU profile**；等待线程样本不作为 CPU 百分比，展开不完整的孤立 SQLite/JSON 叶节点不强行归属。完整链确实捕获到 `channels.run → dispatchPending → persistence.OutboxWork → SQLite`。

现场有一组 11 月 3 日的分片：前片为 failed，后片为 pending。同组发送契约要求前置终态阻断后续分片，但旧代码在过滤前就因“存在 pending”保留 300 ms 重试。于是它持续扫描约 1,446 条 accepted 历史（约 1.69 MB），即使这条 pending 无法推进。`8a84e1b` 将 retry 判定移到依赖过滤之后；历史和阻断分片保留，后来独立的新组仍能及时发送。

该路径的 8 秒局部 A/B：扫描 26 → 0 次，进程 CPU 234.31 → 22.42 ms，累计分配 320,952 → 110,640 B。三种前置终态的回归在旧代码失败、新代码通过，原发送顺序、临时失败和 unknown 恢复契约保留。这只是局部路径的收益，不能代替完整 Host 验收。

## 完整 Host 为什么持续消耗 CPU

重放真实后台服务，用本地平台与模型替身建立 82 个 Session、35 个已结束子代理、19 个启用绑定、6 个连接和 1,446 条 accepted Outbox。模型和平台均不外连。稳定后才进入热区，并核对没有新 Run、attempt、模型请求或发送。

原代码在 15 秒自然窗口为单核 10.25%，累计分配约 103.5 MB、发生 5 次自然 GC；profile 确认子代理每 500 ms 巡检、每个 Inbox 每 200 ms 查询及相关 SQLite/JSON/调度开销。没有消息也重复查询，才是持续消耗的来源。

修复按工作来源安排唤醒：

- Host 持久化成功后发布按记录种类和 ID 筛选的失效信号。回滚、重复归档及未改变的维护状态不发布；无法分类的原始写入保守地通知全部读者。每个读者拥有自己的信号，退出时注销。
- 子代理仅在相关子会话提交、父会话解除运行占用、关系或配置改变时对账。工作表仅保存父子 ID 和确实等待父会话的子 ID；普通根会话的提交不再扫描 35 个无关子代理。公共 Store 适配器只传递 ID，不订阅并复制完整 Core Event；原始 CoreStore 的维护和删除能力保留。
- Inbox 等待自己的新 Observation、绑定、暂停/恢复、会话或模型配置变化。只有真实失败重试、Probe 静默/最迟截止及尚未完成的交付才保留定时唤醒。
- 连接配置与存储任务等待提交信号；旧连接退出和存储任务结束显式唤醒对账。保留期使用下一次实际到期时间，无法推进的终态日任务不自旋。

这里没有缓存聊天正文、长期保存 View、引入新的数据库索引或修改 Core 与 Host 的边界。

## 防止把待命成本转移到活跃工作

第一版事件通知候选虽然达到 180 秒待命 0.117%，却在普通根会话 20 次请求中将 CPU 增加约 93%、累计分配增加约 70%。原因是任意提交都触发全量子代理扫描。该候选被拒绝。

进一步只按 Core ID 唤醒后，绑定 IM 的根会话仍回归。诊断证实每次显式输入合法更新 `im_inbox_resume`，全局 Host 通知因此又扫描全部子代理。最终实现使用按记录筛选的订阅，保留恢复语义，不删除这项业务写入。

最终后台调度实现的 A/B，二者均保留 35 个已完成子代理，运行真实 Host/Core/SDK/SQLite：

| 工况 | 原调度 CPU → 新调度 CPU | 累计分配 | 墙钟中位数 |
| --- | --- | --- | --- |
| 普通根会话 20 次请求 | 221.33 → 172.92 ms | 44.47 → 38.60 MB | 6.41 → 6.51 ms |
| 绑定 IM 且有 35 个子代理的根，20 次请求 | 310.98 → 286.33 ms | 92.44 → 86.88 MB | 9.00 → 8.94 ms |
| 10 次真实 SDK send_message、20 次模型请求、本地平台接受发送 | 458.16 → 433.97 ms | 67.02 → 58.75 MB | 69.99 → 70.86 ms |

新调度在这些根会话提交时全量子代理扫描及无关子代理检查均为零；实际发送经历 pending、dispatching、accepted，输入与非工作会话状态均经核对。工具发送工况包含真实工具等待，不能当作一次请求准备的时间。以上是无 UI 的活跃路径；UI 路径单独验收。

## 待命结果及界面连接路径

最终后台调度实现完成 180.001 秒无 profiler 的自然窗口：CPU 238.041 ms，即单核 **0.13224%**；累计分配 566,056 B、1 次自然 GC、无强制 GC。RSS 从 122.59 降至 63.54 MB，Go Heap 从 52.66 降至 16.58 MB。拓扑、工作状态、Host revision、attempt、六份 DB/WAL/SHM 的内容和文件元数据均不变。

同样拓扑接入实际会话 SSE、计划任务 SSE 和每 5 秒目录请求后，旧界面查询路径为 **1.34%**，尚不满足本轮单核 0.5% 的首条验收线。15 秒 Go CPU profile 捕获目录 `Summary` 与 SSE `View` 重复调用 `Snapshot`，其中包含执行配置复制；各祖先累计值相互重叠，不能相加。

对此仅调整 Host：SSE 在实际 Core 事件和 Host 状态变化时读 View，15 秒 keepalive 不读状态；新增只保存会话 ID 的逐订阅广播，覆盖 Stop 取消后尚无 Core 事件的窗口，且所有打开的界面都能收到。排队输入、配置、审批、压缩、工具恢复等提交也会刷新；必要状态读取失败时要求重新同步，不静默停留。目录一次读取会话记录，同一请求内复用 Bot 存在性和模型可用性检查；保留热会话 Snapshot 的运行/压缩/停止语义。View 复用判断工具恢复时已取得的快照，只有真正恢复工具后再读一次。

保留上述简单修复后的 15.002 秒自然短测消耗 CPU 115.807 ms，即单核 **0.77194%**，累计分配 13.02 MB、没有 GC；会话 SSE 仅发送一次 15 秒 keepalive，业务与六份数据库文件均未变化。该窗口实际完成 3 个目录周期，早先界面窗口为 2 个，且临时客户端按请求完成后等待约 5 秒；因此不能把两个数算成严格配对的改善百分比，也不能把 15 秒写成长期稳定性证明。结果在 `standby-replay/ui-events-final-001-natural15/`。

用户明确要求平衡维护性、复杂度和性能后，本轮接受这一级开销，不继续追自设的 0.5% 硬线。为进一步省去目录轮询而试写的全局应用事件接口、前端失效合并/失败重试调度器及生成 API 变更均已撤回。现有每 5 秒目录刷新和原计划任务事件接口保留，没有引入新的应用 API 或缓存。实验源码、验证与撤回记录封存在证据目录，不进入最终产品。

## 长期内存：归档增长是否变成常驻增长

完整 Host 重放在相同拓扑执行 1,000 次真实 Submit、20 次真实 Compact 并采用摘要，产生 1,020 次 attempt；每步新增 32 KiB 不同文本和有效 PNG 文件引用，保持原 KeepRecent，模型完整读取请求，未启用 profiler 或强制 GC。实际活动上下文峰值约 3.88 MB，归档独有正文增加约 68.82 MB。此工况用于验证生命周期，不冒充 900k 活动上下文的内存规模。

| 同一工况 | 原调度 | 最终后台调度 |
| --- | --- | --- |
| CPU 时间 | 75.09 s | 72.13 s |
| 累计分配 | 22.14 GB | 21.58 GB（−2.54%） |
| 自然 GC | 1,267 | 1,226 |
| 结束 RSS | 148.06 MB | 149.62 MB |
| 后半段 RSS 中位数 | 157.70 MB | 159.74 MB |
| 后半段 Heap 中位数 | 30.72 MB | 29.31 MB |
| Goroutine 起止 | 46 / 46 | 46 / 46 |

结束时单点 Heap 20.40 → 32.81 MB 处于不同 GC 阶段，不能选取这个单点或 RSS 的约 1–2 MB 差异来宣称严格相等。热区与后半段趋势没有随新增归档正文等量保留；后半段新版 Heap 中位数低于旧版。累计分配仍值得未来独立优化，但它是这 1,020 次操作的总和，不是同时占用 21.58 GB。这是有限重放的证据，不是无限运行无泄漏的证明。

## 大上下文请求未回归

M2、8 逻辑 CPU、24 GiB、macOS 15.7.5、Go 1.26.3，`GOWORK=off`，公开 Core `v0.0.0-20261104193014-7c7ea56d4b17`。每组 20 次真实 Submit，混合文本和历史中段的 227 张冻结图片，使用真实 SDK fallback token 估算；本地独立模型进程完整接收并核对请求。无 race、profile 或强制 GC。

| 综合上下文 | 本地最小 / 中位 / 最大 | 完整 loopback 往返中位数 |
| --- | --- | --- |
| 500k | 26.12 / 27.24 / 28.67 ms | 182.94 ms |
| 900k | 33.97 / 34.68 / 36.93 ms | 205.19 ms |

本地时间从 Submit 到 durable RunEnd，扣除全部模型 HTTP RoundTrip，包含准备、编码、回复解析和落库；不是首次冷打开、网络上传或模型响应时间。全部 40 次低于 50 ms，冻结摘要与实际 SDK wire body 校验通过。900k 的另两次真实 Computer read 场景，后一次请求的本地 dispatch 分段为 40.25 / 39.42 ms；两次模型请求的本地合计为 76.76 / 75.67 ms，不能把这个合计当成一次请求。

## 磁盘 I/O

Mac mini 自然 60.18 秒窗口的进程级 `proc_pid_rusage` 磁盘读增量为 0、写增量为 **32,768 B**，Electron 主进程读写增量均为 0。Host WAL 增长 12,360 B；最近的业务更新时间仅对应网页登录 `LastUsedAt` 的约每分钟刷新。该认证持久性保留，没有证据支持为节省写入而弱化它。

后台重放的六份 DB/WAL/SHM 内容、mtime、inode 与大小均不变，说明没有重复业务写入；没有采集到重放的 OS 物理磁盘计数，不能称物理写入绝对为零。profile 中 SQLite 的 `fcntl` 是 WAL 读锁，不等于 fsync 或 SSD 写放大。本轮未发现明显磁盘空转写入。

## 验证与证据索引

产品修改位于独立 `codex/runtime-idle-performance` 工作树，Mac mini 未安装实验构建，原工作区未覆盖。`8a84e1b` 保存 Outbox 修复，`7b298b9` 保存后台提交唤醒；后续提交保存精简后的会话查询修复及撤回全局事件试验的最终状态。发行使用项目现有的两阶段自动流程，并合入发行加速 PR #75；具体发行身份以实际发布的 source SHA 与 manifest 为准。

原始结果、命令、精确源码清单、二进制及 SHA256 保存在测量机：

`~/.local/state/astral-performance/runtime-audits/20261105T070936-sgt/`

- 现场：`activity-resources-summary.json`、`sample-analysis.md`、`symbolicated-sample.txt`、`io-summary.json`。
- Outbox：`outbox-terminal-regression/`，含旧代码失败及新代码通过、局部 A/B。
- 完整后台：`standby-replay/after-003-natural-io/` 与 `standby-replay/scoped-final-002-natural180/`；旧界面 profile 为 `scoped-final-004-ui-profile15/`。
- 活跃：`active-work-replay-scoped/`、`active-work-replay-send-003/`；拒绝候选的完整结果保留在 `active-work-replay/` 与 `active-work-replay-resume-diagnostic/`。
- 长期内存：`longrun-memory-replay/comparison-before-8a-final.json`，两侧完整数据均保留。
- 请求：`final-request-regression/acceptance.json` 与冻结清单。
- 功能和并发：`root-checks/`、`ui-directory-regression/`、`ui-events-checks/` 及各 Worker 回归目录。

精简后的最终源码完整 `go test ./... -count=1 -timeout=5m` 通过（51.13 秒），CI 同版本 golangci-lint v2.15.0 为 0 issues，公开 Core 模块且无本地 Replace。公开文档 `pnpm check:website` 通过。持久化、Inbox、Probe、Outbox、连接/清理生命周期、子代理调度及恢复分别完成针对性 race 检查；多个 SSE 订阅、Stop、排队输入、纯 Host 资源变化和快照竞态均通过普通及 race 检查。最终记录为 `root-checks/final-simplified-go-lint/result.json`。

所有数字均注明测量阶段；最后仅影响会话查询的简化不冒充重新执行过完整 1,000 步内存或 900k 基准。原始长期内存、大上下文基准和最终行为/并发检查共同限定结论，现场、局部重放、完整 Host 与模型网络等待不会混用。

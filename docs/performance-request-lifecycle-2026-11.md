# 请求生命周期性能验收（2026-11-05）

本记录补充 `performance-host-2026-11.md` 的现场诊断，区分正常请求、异常 Compact 恢复和长期归档三个不同工况。历史诊断中的累计分配不是同时驻留内存，异常恢复的 42 秒也不是正常请求延迟。

## 原因与改动

长上下文中的图片本来已经是不可变 JSON 数据，但请求在 Core 投影、估算、校验、冻结以及 SDK 编码之间多次经过完整 JSON 复制。Compact 候选只改变少量工具文本，却重新处理整份图片历史；SDK 估算还为计算图片成本构造发送用的 data URL。图片、原始 JSON、临时 URL 和最终请求正文同时存在，引起大量分配与 GC。

本次改变数据经过这些阶段的方式：

- Core 私有流水线借用已确认的不可变正文，修改时只复制发生变化的容器。旧的可变回调仍收到独立副本；只读投影、估算、收集上下文、校验和执行准备需要明确选择接口。
- 大小预算和既有上下文来源摘要共享一次遍历。无提前 usage facts 的 generation 将两种独立摘要并行计算，返回前始终汇合；不存在跨请求摘要缓存。已有 usage-anchor 路径仍保持原执行顺序。
- SDK 估算验证真实图片后使用占位值计算成本，不构造完整发送 URL，也不重复扫描图片。最终发送正文只编码一次；重试复用同一次执行的字节，修复请求重新编码。
- 大文本先计算所需容量，转义仍采用标准 JSON 语义；图片字符串直接写入最终缓冲区，避免临时完整 data URL。大 Base64 校验使用固定版本的 MIT [moego](https://github.com/MoeForge/moego)，保留纯 Go 与兼容路径。
- Compact 候选复用固定数据，只改变被截断的工具文本。没有引入分段压缩，也没有改变 Core 的上下文模型。

保留预算校验、现有两种摘要的字节语义、冻结请求重建、持久化格式和 Core SDK 独立性。编码失败在创建持久化 attempt 前返回，避免遗留无法执行的 pending attempt。

## 正常大上下文：完整 Host 路径

机器为 Apple M2、24 GiB、Go 1.26.3、macOS arm64，默认 Go GC 配置，未启用 race。Core 为 `cc66dd9ec30b3d817df1b19ec90254c6143b0525`，Host 生产连接使用本分支的只读转发接口。测试执行实际 `Service.Submit`、环境收集、Computer RPC、Core、SDK 和 SQLite 提交。

500k/900k 是由真实 SDK 估算器校准后的综合输入，图片计入其中；不是仅修改模型窗口。900k 输入约 899,578–900,757 estimated tokens，包含 227 张冻结 PNG/JPEG，图片位于历史中段，发送正文约 21.31 MB。文本混合代码、JSON、换行、引号、反斜线、HTML 和中文。测试模型窗口为 1M，输出预留 16k、安全余量 4096。这是合成大输入，不是上游 API 报告的真实 900k usage；替身回复不提供 usage，完整 SDK fallback 估算仍实际执行。

本地开销定义为提交到 durable run-end 的墙钟时间，扣除全部模型 HTTP RoundTrip；包含请求准备、校验、摘要、最终编码、回复解析和落库。`dispatch` 表示进入 RoundTrip，并非 socket 首字节。网络上传、服务端处理和响应等待不属于这个本地指标，但完整总时间另行保留。

测试接收器在独立子进程中完整读取请求、验证 JSON、计算 SHA 后才返回响应。父进程使用真实 loopback HTTP Transport。每个冻结请求均在计时后重新构造，验证摘要与公开 SDK 请求正文 SHA 一致。将接收器移出进程是测量隔离修正，不能记为产品优化收益。

| 工况 | 次数 | 每次本地墙钟最小 / 中位 / 最大 |
| --- | ---: | ---: |
| 900k 混合上下文，第 1 批 | 20 | 36.96 / 37.57 / 44.88 ms |
| 900k 混合上下文，独立复跑 | 20 | 36.85 / 37.84 / 41.71 ms |
| 500k 混合上下文 | 20 | 28.15 / 29.90 / 45.31 ms |

900k 每次累计分配约 34.79 MB，进程 CPU 时间约 63.3–63.5 ms；两种摘要并行缩短墙钟延迟，并不等于减少了同等 CPU 工作。900k 的含真实传输及接收器处理总时间约 212–213 ms/次。测试进程含 fixture 构造与校准的峰值 RSS 为 255–259 MB，不能据此声称生产常驻内存已经小于 100 MB。

另做三次 900k 真实 Computer `read` 工具场景。每次任务包含两次模型请求，第二次进入发送的本地间隔为 42.02–43.09 ms，已包含前次回复处理、工具调度、RPC 和持久化；观察到工具完成事件至下一次发送约 32.9–33.0 ms。整个任务本地合计 80.1–80.9 ms，因为有两次请求，不能把合计当成单次请求。500k 同类第二次发送间隔约 34.02 ms。

这些观测证明指定 M2 工况达到每次 50 ms 门槛，不保证树莓派或任意数据、设备、并发负载的相同上限。有真实 provider usage 的 anchor 路径另行验证，不能用上述无 usage 的并行路径代替它。

## 异常 Compact 与 IM 暂停

原始异常输入约 32 MB、352 张图片。相同输入、摘要目标、待处理 ID 和错误的恢复重放在 `c0f4d42` 测得约 523 ms 墙钟、546 ms CPU、260 MB 累计分配；仍是 51 次 prepare、0 次 HTTP、0 次工具，保留 `634610 + 12800 + 4096 > 500000` 的预算错误。这个数字是异常恢复路径，不代表正常请求性能，也不冒充最终提交重新测得的数字。

Host 同时保留 Compact 失败后的持久暂停：平台消息继续接收和归档，暂停绑定不继续向 Core 累积上下文。模型或连接配置变化、显式恢复触发核对；恢复成功必须先完成 Compact 并通过完整预算校验，再继续正常处理。仅上游服务自行恢复不会创建定时重试。凭据、额度、权限、摘要无法容纳与可退避网络故障仍按既有分类处理。

## 长期内存验收

独立的长期工况使用真实 Core、SDK、SQLite，1000 次工具调用、2000 次普通模型请求、20 次实际 Compact。每步有独立文本与 PNG，归档持续增长而活动窗口有界。热区不强制 GC，不设置 GOGC/GOMEMLIMIT；自然 RSS、自然堆和旧正文弱引用与结束后的显式 GC 诊断分开记录。

同一工作负载的旧基线为 `6793888`，最终提交为 `cc66dd9`：

| 指标 | 旧基线 | 最终提交 |
| --- | ---: | ---: |
| 热区墙钟 | 81.38 s | 68.69 s |
| 累计分配 | 76.76 GB | 37.75 GB |
| 自然 GC | 6854 | 4464 |
| 自然峰值 RSS | 129.61 MB | 106.30 MB |
| 最终自然 RSS | 129.43 MB | 106.10 MB |
| 采样峰值 HeapAlloc | 56.85 MB | 33.20 MB |

热区强制 GC 均为零。两侧均归档 131.39 MB 独立正文、12,022 条 entry，最终活动上下文约 406 KB；活动窗口峰值约 7.16 MB。请求数、工具数、实际 Compact 数、SDK 估算次数/峰值及总发送字节均相同；活动 entry 大小的峰值有 1 B envelope 观测差异。两侧计时边界的微小统计差异另在 comparison 记录，不用于声称精确到亚毫秒的加速比。

最终版本第 750 到第 1000 步，归档增加 32.85 MB，RSS 仅从 105.94 MB 到 106.10 MB。旧 900 份文本与图片 backing 在自然 GC 后均无存活弱引用。这支持本工况的归档增长没有造成正文持续滞留，不能外推为对所有小对象的无限时间无泄漏证明。

100 MB 是争取的进程内存目标，最终自然 RSS 仍比它高约 6 MB。结束后单独强制 GC 的约 3.87 MB 活跃堆不用于替换自然 RSS。此长测范围为 Core＋SDK＋SQLite＋进程内 discard 接收器，不代表全部 Host、IM 连接、桌面界面或独立 900k 工况的进程内存。

## 重放入口与证据

测试均为显式 opt-in。正常 CI 不读取私有 fixture，不发送真实 IM 消息或请求外部模型。原始聊天数据库保持只读封存，不进入仓库。

```sh
go test -tags astral_prepared_execution -c ./internal/harness -o /tmp/astral-harness-perf.test
ASTRAL_SUCCESS_SCALED_HOST=900k \
ASTRAL_SUCCESS_SCALED_HOST_MODE=prompt \
ASTRAL_SUCCESS_SCALED_HOST_TEXT_STYLE=mixed \
ASTRAL_SUCCESS_SCALED_HOST_ROUNDS=20 \
ASTRAL_SUCCESS_SCALED_RECEIVER=process \
ASTRAL_SUCCESS_SCALED_SEED=/absolute/path/large-images-request.json \
ASTRAL_SUCCESS_SCALED_HOST_OUTPUT=/absolute/path/new-output \
/tmp/astral-harness-perf.test -test.run '^TestHostSuccessScaledContext$' -test.v
```

`result.json` 必须同时确认 success、请求重建校验和逐轮 `all_rounds_local_under_50ms`，不能只看 Go 测试 PASS。工具任务包含两次请求，逐段间隔需单独检查。设 `ASTRAL_SUCCESS_SCALED_PROFILE=1` 可另做 CPU profile，带 profile 的结果不代替上表无 profile 的延迟验收。

开发机封存路径：`~/.local/state/astral-performance/baselines/request-host-final-cc66dd9-20261105/`，包括每批 result、metadata、源版本及二进制 SHA。最终 profile 保存在其中的 `cc66-process-mixed-20-profile/cpu.pprof`。1000 步长测位于 `~/.local/state/astral-performance/baselines/memory-lifecycle-20261105-final-cc66dd9/`，保留源码/二进制身份、阶段统计、比较 JSON 和 SHA manifest。

上一轮串行摘要的 profile 为 `/tmp/astral-scaled-host-20261105/0569-process-mixed-20-profile/cpu.pprof`：20 次累计 CPU 约 1.11 s，SHA 约 320 ms，字符串校验/编码约 180 ms。最终 20 次 profile 累计采样 1.08 s，其中 SHA 270 ms，标准 JSON 字符串编码约 70 ms；仍保留两种 SHA，改变的是执行重叠和重复处理。采样存在噪声，调用树存在嵌套，不能将 inclusive 数字相加，也不能把两个 profile 的小差异当成确定的 CPU 百分比收益。

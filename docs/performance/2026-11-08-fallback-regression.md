# Fallback、自动压缩与等待工况性能回归（2026-11-08）

## 当前验收状态

已完成 **232 个正式外层进程记录**，其中三版本、七个请求工况占 51 个，其余 idle、恢复、Probe 和长期运行占 181 个；另有 2 个独立 profile。记录经独立严格验证，`validated_rounds_success=true`，缺失、未完成和失败记录均为 0；完整发送正文、冻结请求重建、请求／工具次数及最终提交按各工况检查。测试通过只说明工况及结果满足验证条件，不代表 CPU、延迟或内存没有回归。

原 common runner 在进入 idle 工况时因驱动与结果 schema 不一致报 `case did not explicitly validate idle`。原 `failure.json`、`summary.success=false` 与 acceptance 顶层 `success=false` 均保留；没有改写成整套成功，也没有把驱动错误算作产品失败。51 条请求记录另行验收，不依赖这次原驱动的成功标志。主任务额外核对已测源码和二进制的稳定性，`source/binary stability=True`；独立 acceptance 验证器本身不执行这项身份核对。

idle、实际恢复、Probe 和 1000 步长期运行现已补齐，新增 181 个正式外层记录；连同请求共 232 个，另有 2 个独立 profile。结果显示 before→fallback 存在明确后台 CPU／分配增量，1000 步单次 current 也较 v31 慢，不能给出“全工况无回归”的结论。

## 固定版本、输入与测量口径

| 标签 | Host 产品源码 |
| --- | --- |
| before | `0ed77527eb0b7182f9e91f183222c0d9fa2c8a17`，引入 fallback 前 |
| v31 | `f2a05a3dde3fb5b2098bf51a8403084031c24de1` |
| current | `7228850389e6d3fe1dec9638cc5a25bcb1f46f10` |

三版均固定公开 Core 模块 `v0.0.0-20261106045805-4e2310bd0d4a`，没有修改 Core、replace 或生产配置。相应版本加入测量用测试夹具和资源采样器，测试源码清单与二进制 SHA 单独封存；产品源码不相互覆盖。机器为 Apple M2、8 个逻辑核、24 GiB、Darwin 24.6 arm64、Go 1.26.4。普通测量未启用 race/profile；GOGC、GOMEMLIMIT、GOMAXPROCS、GODEBUG 均未另设。各版本串行、交替顺序执行。

图片种子完全合成：227 张确定生成的 128×128 RGB PNG，JSON 共 **14,952,986 B**，SHA256：

```text
cd2d68fe8d9832672797085a7f9f974ef6c39ad0922b4aac4e9ddd28b5beef7a
```

文本同样为公共合成历史，含代码、JSON、日志、转义和中文。500k／900k 是当前 SDK 对实际综合请求的校准估算，不是仅修改窗口，也不是提供商报告的 usage。回复不提供虚构 usage 锚点。该种子不是历史事故的私有图片语料，不能把本次绝对值与旧语料直接混算。

请求测量使用实际 Host Submit、环境收集、Core、SDK、SQLite 和本机 Computer；只替换模型服务。scaled 接收器在独立进程完整消费、校验并哈希正文，父进程的 CPU／分配不包含接收器子进程；另记录真实 loopback RoundTrip。本文的“本地 wall”是原墙钟减去该 RoundTrip 区间的派生量，仍可能受到接收器引起的调度或 GC 影响。

- CPU 使用测试内 `RUSAGE_SELF` 的用户态加内核态时间，不等于墙钟，也不包含其他应用的 CPU。wrapper 的 `wait4` 可能包含已 wait 的隔离接收器后代 CPU，保留作完整运行取证，不用于本文 Host CPU 对照。累计分配不是同时存活内存。
- 本文 **MB＝1,000,000 B**，**KiB＝1,024 B**；机器内存使用 GiB。没有将 MB 和 MiB 混用。
- 请求 post-GC 是计时外、冻结请求验证后仍保留 live Host／Computer 的堆；waiting、Probe 和长期内存也在对象存活时单独诊断。TestMain 的 post-GC 则发生在全部 cleanup 之后，各表明确标注，不能混用。
- scaled RSS 是热区末记录的父进程生命周期高水位，包含构造和校准，不是热区专属峰值，也不是两次 RSS 的差。
- I/O 是 OS 归属到进程的存储读写字节，不是设备 NAND 写入量。数据库／WAL 文件长度、逻辑提交和 JSON 大小另记，不能相互代替。
- 样本少时仅报告中位数和实际范围，不推断 p95／p99。热区没有强制 GC，诊断 GC 不进入请求计时。

## 已完成的请求对照

下表时间单位为 ms，内存为 MB，格式为中位数［最小–最大］。Warm 各版本各三次独立进程；tool 包含两次模型请求，compact 包含一次手动摘要加随后一次普通请求，均按整个测量操作报告。

| Warm 工况 | 版本 | CPU ms | wall ms | 累计分配 MB | live post-GC MB | 热区写 KiB |
| --- | --- | ---: | ---: | ---: | ---: | ---: |
| text | before | 10.61［10.51–11.41］ | 7.86［7.50–8.41］ | 1.99［1.97–2.00］ | 3.50［3.49–3.53］ | 408.00［408.00–408.00］ |
| text | v31 | 10.60［10.30–10.86］ | 7.69［7.55–7.72］ | 2.14［2.13–2.15］ | 3.52［3.52–3.59］ | 428.00［428.00–428.00］ |
| text | current | 11.35［11.18–11.46］ | 8.11［7.93–8.17］ | 2.12［2.10–2.20］ | 3.56［3.50–3.58］ | 428.00［428.00–428.00］ |
| tool | before | 27.70［27.53–29.07］ | 21.63［20.31–22.39］ | 6.16［6.07–6.21］ | 4.00［3.95–4.01］ | 880.00［880.00–880.00］ |
| tool | v31 | 28.90［26.88–28.98］ | 21.27［20.29–22.12］ | 6.27［6.20–6.31］ | 4.00［3.99–4.02］ | 900.00［900.00–900.00］ |
| tool | current | 29.00［27.75–29.68］ | 21.27［21.02–21.35］ | 6.27［6.26–6.29］ | 4.01［3.97–4.03］ | 900.00［900.00–900.00］ |
| compact | before | 35.10［32.06–36.35］ | 26.44［25.49–27.76］ | 7.75［7.43–7.77］ | 4.36［4.33–4.46］ | 600.00［600.00–600.00］ |
| compact | v31 | 34.85［34.13–36.75］ | 26.69［26.08–27.61］ | 7.55［7.36–7.90］ | 4.07［4.05–4.34］ | 636.00［636.00–636.00］ |
| compact | current | 35.55［34.15–38.40］ | 27.18［26.65–50.56］ | 7.53［7.53–7.64］ | 4.33［4.07–4.34］ | 636.00［636.00–636.00］ |

Warm 热区读取均为 0，仅 before/tool 有一次 76 KiB 读取。Host WAL 增量在 before 为 0，v31/current 三种操作均为 8.05 KiB；这是文件长度变化，与上表 OS 写入量分别记录。

500k／900k prompt **每版本只有一个独立进程，内部连续执行 20 个请求**；其 CPU／分配／本地 wall 范围来自相关内轮，RSS 和 post-GC 各只有一次观测。900k tool 每版本三个独立任务，每任务两次 HTTP；其“每请求 CPU／分配”是任务总量÷2的归一值，不是逐 HTTP 的直接归因，本地 wall 仍是两请求任务总量。held 每版本三个独立单请求，每个接收器真实停留 10 秒，**不是 20 内轮**。

| Scaled 工况 | 版本 | 每请求 CPU ms | 每请求分配 MB | 每逻辑轮本地 wall ms | 父进程峰值 RSS MB | live post-GC MB |
| --- | --- | ---: | ---: | ---: | ---: | ---: |
| 500k-prompt | before | 45.51［39.82–51.92］ | 28.07［25.72–28.94］ | 26.47［24.99–35.38］ | 184.30 | 39.02 |
| 500k-prompt | v31 | 49.12［42.54–57.03］ | 29.66［26.19–29.82］ | 28.43［26.28–35.84］ | 186.22 | 39.06 |
| 500k-prompt | current | 50.98［41.91–61.57］ | 28.72［25.99–29.87］ | 28.70［26.30–36.24］ | 192.53 | 39.03 |
| 900k-prompt | before | 66.61［59.57–81.93］ | 31.17［31.00–31.43］ | 39.01［37.19–48.09］ | 202.60 | 57.64 |
| 900k-prompt | v31 | 71.90［65.77–82.30］ | 31.53［31.25–31.74］ | 42.70［41.21–48.18］ | 213.65 | 57.62 |
| 900k-prompt | current | 74.62［66.39–83.67］ | 31.46［31.16–31.84］ | 45.32［42.04–51.59］ | 181.75 | 57.64 |
| 900k-tool | before | 70.63［67.75–74.32］ | 32.27［32.17–32.27］ | 90.84［88.50–95.72］ | 211.14［186.99–228.77］ | 53.78［42.08–57.97］ |
| 900k-tool | v31 | 71.51［68.02–111.54］ | 32.40［32.32–32.41］ | 92.75［87.14–145.74］ | 210.42［203.52–211.40］ | 57.98［42.10–58.00］ |
| 900k-tool | current | 78.14［74.61–87.40］ | 32.28［32.19–32.39］ | 94.63［93.94–110.34］ | 233.01［211.70–233.57］ | 57.98［42.07–57.99］ |
| 900k-prompt-held | before | 63.57［57.26–72.87］ | 31.38［31.21–31.38］ | 39.82［35.86–45.82］ | 231.64［208.44–233.11］ | 57.49［44.90–57.50］ |
| 900k-prompt-held | v31 | 98.33［97.37–102.35］ | 32.23［32.15–32.28］ | 39.84［33.96–43.27］ | 187.56［187.53–212.73］ | 57.50［44.94–57.52］ |
| 900k-prompt-held | current | 105.80［96.12–115.29］ | 32.17［32.16–32.32］ | 37.52［35.21–37.85］ | 211.94［211.71–233.28］ | 53.36［41.61–57.53］ |

held 完整墙钟中位数为 before 10.180s、v31 10.185s、current 10.172s。current 相比 before 多约 **42.23ms CPU／10秒**，约相当于额外 0.42% 单核，而不是消耗 10 秒 CPU。held 热区 OS 写入为 496→544→544 KiB；900k tool 为 1072→1120→1120 KiB，均未发生恢复模型切换。

已观测的风险与不能推出的结论：

- current 相比 before，500k／900k prompt 的内轮 CPU 中位数均增加约 12%；本地 wall 分别由 26.47→28.70ms、39.01→45.32ms。current/900k 有 1/20 轮为 51.59ms，其余版本该工况为 0/20 超过 50ms。尚未以 profile 归因，不能只凭一个进程的内轮分布宣布稳定回归比例。
- Warm current/text 的 CPU 中位数增加约 7%，tool 约 4.7%；compact 中位 wall 变化约 2.8%，但有 50.56ms 的单次尾点。三样本不支持稳定尾延迟结论。
- live post-GC 未见成倍增长；scaled 20 轮后的堆接近对应 before。RSS 有明显跨运行波动，current/900k 单次较低不证明内存优化，tool 较高也不能单独证明泄漏。
- 正常成功请求也有新增 Host 状态写入。多会话、长时间等待下的 CPU／I/O 放大与持久维护记录增长，应由后续工况判断，不能从这七项成功请求外推。

## 慢链路 stdout：受测版本的缺陷

在相同 Host/Core 固定源码上，独立临时诊断仅执行合成 `printf`，通过真实本地进程以及 Daemon.handle＋transport.Peer（本机 net.Pipe）两层验证。普通 pipe 两层的立即读取均为 22 B；确认进程退出后，把读取／输出挂接延后 750ms，两层均为 **0 B、exit 0、wait/read error 均为空**。因此数据在进入模型之前已经丢失，不支持“模型吞 stdout”的归因。

固定 Core 的 `standardtools/local/process_unix.go` 在退出后最多等 500ms 就关闭输出，`os.ErrClosed` 又归一成 EOF。Host 的 `Backend.StartProcess` 先等 `process.start` 返回，再发 `process.output`；Daemon 到后者才开始读输出。这个组合解释了迟到挂接为什么可以表现为正常退出但没有 stdout。

本机 PTY 立即与延后读取均为 23 B，**没有复现相同丢失**。最初对 PTY 先 Wait 再读会阻塞，并触发 30 秒诊断限时；最终 PTY 诊断只延后读取／挂接，不假定它已退出。不能将 pipe 的实证泛化为所有 PTY 或 Windows 行为。最终八组诊断耗时 3.07 秒；诊断 PASS 表示成功观察当前缺陷，不代表产品行为正确。

最小复现入口是公共 `local.Backend.StartProcess` 执行合成 `printf`，pipe 模式先 `Wait()`，再等待 750ms，最后 `io.ReadAll(process.Output())`；立即读取作为对照。全链版本只在 Daemon 的 `process.output` 转发前插入该延迟。诊断源码、命令及 SHA 单独保留在仓库外，复现本身没有修改产品或 Core。

后续修复已在 Host 实现，正在进行功能与并发测试。本报告性能矩阵的 current 仍固定为 `7228850`，没有把新 RPC／输出缓冲版本纳入测量；上述数据不能作为新修复的性能验收。

## 已完成的后台与恢复矩阵

以下均为同一批冻结源码的离线测量。外层进程数不等于模型请求数；请求次数、工具结果和状态不变量单独验收。232 个正式外层记录全部通过 v2 数据验收；另有 2 个 profile 进程，仅用于定位。原 common 驱动失败仍保留，不将这 232 个有效记录表述为原 suite 成功。

| Suite | 版本 | 外层记录 | 验收结果 |
| --- | --- | ---: | --- |
| 请求 | before / v31 / current | 51 | 记录有效；原 common 失败状态保留 |
| idle / 挂起请求 / 已结束子代理 | before / v31 / current | 45 | 完整 |
| recovery（含重复与额度等待） | v31 / current | 98 | 完整 |
| 群聊判断 Probe | v31 / current | 36 | 完整 |
| 1000 步长期内存 | v31 / current | 2 | 每版单次 |
| 独立 profile | current | 2 | 诊断，不混入正式数字 |

### Idle 与尚未返回的模型请求

每个工况每版 3 个独立进程，热区均为 10 秒。history 含 10,000 条 completed 维护记录；active-10/50 含 1,000 条 completed 记录及 10/50 个被离线 transport 阻塞的真实请求；settled 含 35 个已结束子代理及 1,000 条 completed 记录。热区只等待 timer，不轮询 View、不强制 GC、不结束请求。全部检查均确认没有额外模型请求、Core 版本变化或热区强制 GC。

CPU 为 ms 中位数［范围］，内存为 MB。这里的 post-GC 特指 **整个测试清理后** 的堆，不能当作活动 Host 的保留堆；RSS 是热区末的进程高水位，仍包含 setup。OS 读／写为 KiB，中位数。

| 工况 | 版本 | 热区 CPU ms | 分配 MB | 清理后 GC 堆 MB | RSS MB | OS 读/写 KiB |
| --- | --- | ---: | ---: | ---: | ---: | ---: |
| 空服务 | before | 0.23［0.21–0.27］ | <0.001 | 3.02 | 28.02 | 0/0 |
| 空服务 | v31 | 15.55［8.86–16.79］ | 0.033 | 3.02 | 28.43 | 0/0 |
| 空服务 | current | 8.39［7.95–17.48］ | 0.032 | 3.01 | 28.46 | 0/0 |
| 10k completed | before | 0.18［0.14–0.25］ | <0.001 | 3.13 | 43.12 | 0/0 |
| 10k completed | v31 | 248.84［215.13–275.69］ | 0.032 | 3.13 | 42.53 | 224/0 |
| 10k completed | current | 202.51［171.54–267.76］ | 0.032 | 3.18 | 42.75 | 224/0 |
| 10 个挂起请求 | before | 0.22［0.21–0.31］ | <0.001 | 3.21 | 43.60 | 0/0 |
| 10 个挂起请求 | v31 | 201.59［190.72–227.78］ | 6.814 | 3.23 | 42.99 | 0/0 |
| 10 个挂起请求 | current | 208.01［178.49–209.01］ | 6.822 | 3.20 | 43.06 | 0/0 |
| 50 个挂起请求 | before | 0.25［0.24–0.83］ | <0.001 | 3.27 | 70.34 | 0/0 |
| 50 个挂起请求 | v31 | 633.85［625.67–690.03］ | 33.796 | 3.36 | 75.17 | 0/0 |
| 50 个挂起请求 | current | 680.61［648.46–714.00］ | 33.812 | 3.35 | 76.05 | 0/0 |
| 35 个 settled 子代理 | before | 0.23［0.21–0.26］ | <0.001 | 3.43 | 55.57 | 0/0 |
| 35 个 settled 子代理 | v31 | 46.16［36.77–49.00］ | 0.032 | 3.34 | 56.75 | 0/0 |
| 35 个 settled 子代理 | current | 23.55［13.62–26.08］ | 0.032 | 3.39 | 57.38 | 0/0 |

相较 before，fallback 版本存在明确的后台 CPU 与分配增量。current 的 10k completed 工况约消耗单核 2.03%；50 个挂起请求约 6.81%，并分配约 3.38 MB/s。current 相对 v31 并非所有读数都更高，但这不能消除 before→fallback 的新增成本。全部工况热区 OS 写入为 0；history 的 224 KiB 归属读取也不等于反复从 SSD 读取同量数据。

### 失败、自动压缩和生命周期恢复

12 类功能恢复工况各版各 3 轮。下表均为完整 `m.Run` 的中位数，包含建库、配置、离线模型、断言和 teardown，**不是单次 fallback 的热区成本**。CPU 使用 `RUSAGE_SELF`；post-GC 是清理后的堆；内存 MB、OS 读／写 KiB。表中箭头均为 v31 → current。

| 工况 | CPU ms | 分配 MB | 清理后 GC 堆 MB | RSS MB | OS 读/写 KiB |
| --- | ---: | ---: | ---: | ---: | ---: |
| 网络五次冻结重试 | 4178.29 → 3665.19 | 191.43 → 191.78 | 3.32 → 3.22 | 35.57 → 33.91 | 448/2500 → 440/2500 |
| quota 后 overflow | 258.39 → 238.88 | 28.43 → 28.45 | 3.27 → 3.23 | 34.42 → 34.23 | 24/2268 → 20/2268 |
| B 重新生成摘要 | 598.54 → 627.15 | 150.62 → 150.26 | 3.92 → 3.77 | 44.83 → 44.50 | 32/5128 → 12/5128 |
| 大窗口 B 免摘要 | 554.39 → 556.82 | 141.83 → 143.27 | 3.92 → 3.94 | 44.78 → 44.79 | 16/4996 → 8/4996 |
| 候选本地预算不足 | 524.72 → 526.47 | 146.92 → 139.90 | 3.39 → 3.42 | 38.57 → 38.78 | 4/2692 → 4/2692 |
| 重启后恢复并复用工具结果 | 204.83 → 197.56 | 24.27 → 24.15 | 3.26 → 3.27 | 35.11 → 33.90 | 20/3516 → 20/3516 |
| 完成后取消恢复检测 | 85.44 → 84.02 | 10.19 → 10.06 | 3.24 → 3.13 | 31.83 → 31.51 | 12/1880 → 12/1880 |
| 丢弃过期恢复检测 | 85.34 → 81.69 | 9.72 → 9.71 | 3.16 → 3.18 | 31.74 → 31.85 | 4/1868 → 4/1868 |
| Stop/Close 与维护并发 | 159.37 → 159.75 | 43.00 → 43.67 | 3.45 → 3.40 | 37.00 → 37.14 | 8/2448 → 4/2448 |
| 子代理继承实际模型 | 39.26 → 41.68 | 8.32 → 8.34 | 3.04 → 3.07 | 30.16 → 30.77 | 4/1536 → 16/1536 |
| 未开始子代理的模型失效 | 128.45 → 108.24 | 18.25 → 18.46 | 3.25 → 3.22 | 33.78 → 33.60 | 20/3212 → 24/3212 |
| IM 等待不误触发群聊判断 | 69.57 → 72.16 | 9.62 → 9.72 | 3.26 → 3.25 | 31.33 → 31.11 | 8/1872 → 4/1872 |

网络测试真实执行约 12 秒退避，并核对同一模型五次 wire payload／凭据不变后才切下一候选。自动压缩测试分别确认：窗口较小的 B 必须重新摘要，较大的 B 可以继续原任务；失败摘要不被采用，已完成工具不重放。功能验收成立不等于 CPU 全面不变，例如 B 重新摘要的完整测试 CPU 中位数约 598.55 → 627.15 ms，其他路径也存在上下波动；三轮不足以把每个差值都归因于产品变化。

### 额度耗尽后的真实等待

10 秒工况各版 3 轮；65 秒工况各版仅 1 轮。生产 60 秒恢复检测周期没有缩短，热区不轮询、不强制 GC。下表 post-GC 为 **仍在 waiting、Stop 之前** 的活跃堆；CPU ms、内存 MB、OS 读／写 KiB。

| 会话/窗口 | 版本 | 热区 CPU ms | 分配 MB | waiting GC 堆 MB | RSS MB | OS 读/写 KiB |
| --- | --- | ---: | ---: | ---: | ---: | ---: |
| 1 / 10s | v31 | 72.23［63.45–82.18］ | 3.50 | 3.26 | 30.82 | 0/0 |
| 1 / 10s | current | 76.34［71.78–78.45］ | 3.49 | 3.31 | 31.05 | 0/0 |
| 10 / 10s | v31 | 383.56［321.52–425.23］ | 33.33 | 4.41 | 38.32 | 0/0 |
| 10 / 10s | current | 397.46［339.97–419.35］ | 33.14 | 4.45 | 38.65 | 0/0 |
| 50 / 10s | v31 | 1552.55［1476.08–1563.73］ | 160.78 | 9.06 | 54.15 | 0/0 |
| 50 / 10s | current | 1488.63［1398.92–1630.26］ | 160.16 | 9.10 | 54.07 | 0/0 |
| 1 / 65s，单次 | v31 | 547.03 | 22.44 | 3.35 | 30.97 | 16/48 |
| 1 / 65s，单次 | current | 472.64 | 21.80 | 3.31 | 30.24 | 12/48 |

50 个等待恢复任务的 current 中位数为单核约 **14.89%**、累计分配约 **16.02 MB/s**，这是需要保留的已知后台成本，不能用约 9.10 MB 的 GC 后活跃堆掩盖。10 秒内没有重复模型请求或 OS 写入；65 秒内每个测试恰好发生一次真实 SDK 检测，失败后继续等待，Stop 后不再检测。两版各有 48 KiB OS 归属写入，与周期性状态持久化相容，不能宣称恢复等待永远零写入。

### 群聊判断 Probe

每一选择方式／结果类型每版 3 轮，每轮 100 次，合计 3,600 次离线判断。测量直接调用 `shouldParticipate`，包含真实本机 SDK/HTTP 正文消费；输入、平台和 Host 保持存活进行计时外 GC。下表为 v31 → current 的三轮中位数，CPU ms／100 次，其他为 MB；所有热区 OS 读／写均为 0。

| 结果 / 模型选择 | CPU ms | 分配 MB | live GC 堆 MB | RSS MB |
| --- | ---: | ---: | ---: | ---: |
| 成功 / 默认 | 66.24 → 64.45 | 17.61 → 17.33 | 3.86 → 3.85 | 35.49 → 35.39 |
| 成功 / Session | 66.06 → 65.62 | 17.81 → 17.76 | 3.84 → 3.89 | 35.27 → 35.93 |
| 成功 / Probe 独立模型 | 65.16 → 65.01 | 17.83 → 17.69 | 3.87 → 3.86 | 35.67 → 36.04 |
| 额度错误 / 默认 | 64.32 → 63.64 | 16.67 → 16.59 | 3.85 → 3.79 | 35.26 → 35.31 |
| 额度错误 / Session | 65.56 → 65.75 | 17.02 → 16.91 | 3.87 → 3.88 | 35.73 → 35.70 |
| 额度错误 / Probe 独立模型 | 66.94 → 62.04 | 17.00 → 16.80 | 3.88 → 3.92 | 36.06 → 35.80 |

当前两版 CPU／分配读数接近。每次只发一条判断请求；失败批次不会唤醒主 Session、变更模型或改写 Core 状态，不发送平台消息。该工况验证的是现有群聊判断行为，**不是 Probe 自己具备候选模型 fallback**。本机单次调用的约 0.4 ms 中位数也不能代表真实提供商的网络或生成延迟。

### 同一 Session 连续 100 次 fallback

每版 3 轮；每轮 primary 100、backup 200、完整请求 300、已完成工具 100，未重放工具。下表为完整测试中位数，CPU 秒、分配 GB＝10⁹ B、内存 MB、OS 读／写 KiB，均含 setup／断言／清理。

| 版本 | CPU s | 分配 GB | 清理后 GC 堆 MB | RSS MB | OS 读/写 KiB |
| --- | ---: | ---: | ---: | ---: | ---: |
| v31 | 16.04 | 3.241 | 4.05 | 54.38 | 1368/224396 |
| current | 15.92 | 3.236 | 3.80 | 54.25 | 1180/224400 |

两版维护状态 JSON 均从 step0 的 **382 B** 增到约 **4,037 B**，`suppressed_runs` 从 0 增到 100；中间 step20/40/60/80 分别约 1,236/1,936/2,636/3,336 B。状态载荷确实随累计内部失败 Run 数增长，不能将其描述为大小恒定。

goroutine 端点每轮均为 18 → 18；current 中途为 18–20，未发现结束后持续遗留。热区无强制 GC，自然堆端点中位数约 v31 4.48 → 10.31 MB、current 4.18 → 10.00 MB；这受 GC 阶段影响，不是 GC 后泄漏量。current 每 20 步的分配从首段约 459.61 MB 增到末段 822.94 MB；完整历史和请求也随步数增长，这些数据不能把后段增长全部归因于 `suppressed_runs`。

### 1000 步归档与内存稳定性

每版只有 1 次完整执行，范围是 **Core＋真实 SDK＋SQLite＋进程内离线 sink**，不含完整 Host/fallback worker。1000 步每 50 步手动 compact；两版均完成 2000 次普通 API、20 次 summary、1000 个工具结果、2020 个 Attempt 和 12022 条归档记录，热区强制 GC 为 0。

| 指标 | v31 单次 | current 单次 |
| --- | ---: | ---: |
| 热区墙钟 s | 67.825 | 71.798 |
| 完整 m.Run CPU s | 82.262 | 87.180 |
| 热区累计分配 GB | 36.500 | 36.477 |
| 250ms 采样峰值 HeapAlloc MB | 36.900 | 37.844 |
| 自然热区结束 RSS MB | 92.176 | 99.254 |
| 热区末生命周期峰值 RSS MB | 99.434 | 99.451 |
| Core 保持存活的 GC 后堆 MB | 3.900 | 3.846 |
| 全部测试清理后 GC 堆 MB | 3.691 | 3.674 |
| 完整 m.Run OS 读 MB | 58.073 | 53.047 |
| 完整 m.Run OS 写 MB | 2214.113 | 2209.079 |
| 归档唯一 payload MB | 131.394 | 131.394 |
| 最终 active context MB | 0.406246 | 0.406244 |
| 最大 active context MB | 7.157675 | 7.157680 |
| 最终 DB / WAL MB | 155.296 / 4.392 | 155.644 / 4.380 |


这一对单次读数中，current 热区墙钟高 **5.86%**、完整 CPU 高 **5.98%**，分配仅低约 0.066%。没有重复样本支持把差异稳定归因于代码，也不能据 PASS 宣称速度和 CPU 无回归。同机其他应用可影响墙钟、调度和缓存；本表的 `RUSAGE_SELF` CPU 不包含其他应用本身的 CPU 时间。

step200–1000 的 17 个 after-compact 样本中，两版自然 HeapAlloc 相对归档 payload 的回归斜率均为负；GC 后旧文本／图像 backing 存活计数均为 0，最后 900 个 body 已超过两轮窗口。这支持本场景下旧大正文没有随归档持续保留。自然 RSS 后四分位相对前四分位仍增加约 v31 **9.45 MB**、current **14.75 MB**；未触发 body-sized growth 阈值不等于证明 RSS 恒定或小元数据永远不增长。

两版 sink 累计消费同样的 **7,524,688,055 B** 请求体。重复 wire 字节、131.39 MB 唯一 payload、约 160 MB 最终 DB＋WAL 和约 2.21 GB OS 归属写量是四种口径，不能互相替代。本机没有采到 logical syscall I/O（`logical_io_available=false`），其中默认 0 不能写成零逻辑 I/O。

### 独立 profile 定位与结论边界

仅 current 额外跑 idle-history 30s 与 waiting-50 原 10s，各 1 轮。CPU/heap profile 与正式数据分目录。CPU 采样覆盖完整 `m.Run`，包含创建数据、断言、诊断 GC 和 Stop/Close，不能把 profile 百分比当作纯等待热区占比；cum 父子链重叠，不可相加。

| Profile | 完整时长 / CPU 样本 | 可支持的定位 |
| --- | --- | --- |
| idle-history | 31.93s / 1.33s | fallback worker → PendingMaintenance / SQLite 读页累计 0.74s，55.64%；SaveMaintenance 0.26s，19.55%，主要可由 setup 写入 10k completed 解释 |
| waiting-50 | 12.49s / 2.63s | 调度唤醒累计约 1.02s，38.78%；newproc 0.45s，17.11%；reconcileModelFallback 0.33s，12.55%；另有 Session 创建、GC 和 Stop 成本 |

冻结源码的 worker 每 250ms 查询未完成维护记录；`operations` 仅有 id 主键，没有对应 kind/state 过滤的索引。idle-history 的查询／读页样本与这一周期成本吻合，但本轮没有用查询计划证明完整扫描。waiting Session 尚未到 60 秒探测时点也会被再次调度并进入 open／状态／snapshot 检查；50 Session×约40 tick 对应约2000次调度是源码数量级推算，不是 profile 实测调用次数。

这些证据支持后台检查值得收敛，也明确保留现有成本。本轮没有据此重构 worker、放大 tick 或改变 60 秒检测周期；后续实现不属于这次冻结版本的验收。当前不能给出“全工况无回归”或“已修复后台开销”的结论。真实 4 分钟单次请求超时也未通过连续五次等待来测量，不能声称执行了 20 分钟超时压力测试。

## 可复现入口与数据边界

仅提交合成夹具、生成脚本和本文汇总。原始 request／数据库／profile／身份清单保留在仓库外；本文不包含用户聊天、主机名、账号、个人绝对路径、凭据或私有图片。所有输出目录必须为新建可丢弃目录；以下是复跑入口，不会覆盖本次保留失败的结果。

```sh
PERF_RUN_DIR="$(mktemp -d /tmp/astral-fallback-perf.XXXXXX)"
python3 scripts/generate-performance-seed.py --output "$PERF_RUN_DIR/public-images-request.json"
python3 scripts/generate-performance-cases.py --suite common \
  --seed "$PERF_RUN_DIR/public-images-request.json" --output "$PERF_RUN_DIR/common-cases.json"
```

生成器输出的种子字节数与 SHA 应与上文一致，校验不符时应停止比较。`--suite recovery|probe|longrun` 生成其余清单，不需要私有种子。仅验收七项已完成请求时，生成独立 request-cases 清单：

```sh
python3 - "$PERF_RUN_DIR" <<'PYCASES'
import json, pathlib, sys
p = pathlib.Path(sys.argv[1])
names = {"warm-text", "warm-tool", "warm-compact", "500k-prompt",
         "900k-prompt", "900k-tool", "900k-prompt-held"}
cases = json.loads((p / "common-cases.json").read_text())
with (p / "request-cases.json").open("x") as output:
    json.dump([case for case in cases if case["name"] in names], output, indent=2)
PYCASES
```

在三个隔离 worktree 中保持各自产品源码，加入同一公共测量夹具、`performance_resources_*_test.go` 和 `internal/testkit/perf`，记录测试源码 SHA 后分别构建。公共测量夹具包括 `harness_test.go` 的资源登记、`success_host_baseline_unix_test.go`、`success_scaled_host_unix_test.go`、`success_scaled_host_process_unix_test.go` 和 idle 测试；旧 before 不加入依赖 fallback 私有类型的恢复测试。构建与 runner 入口如下，路径替换为各自已准备的独立源码／二进制：

```sh
# 将 /path/worktrees 替换为已准备的隔离源码根目录。
# 普通测量不加 -race 或 profile。
mkdir "$PERF_RUN_DIR/bin"
for PERF_VERSION in before v31 current; do
  (
    cd "/path/worktrees/$PERF_VERSION"
    GOWORK=off go test -tags astral_prepared_execution -c \
      -o "$PERF_RUN_DIR/bin/$PERF_VERSION-harness.test" ./internal/harness
  )
done

python3 scripts/run-performance-comparison.py \
  --binary "before=$PERF_RUN_DIR/bin/before-harness.test" \
  --binary "v31=$PERF_RUN_DIR/bin/v31-harness.test" \
  --binary "current=$PERF_RUN_DIR/bin/current-harness.test" \
  --source before=/path/worktrees/before \
  --source v31=/path/worktrees/v31 \
  --source current=/path/worktrees/current \
  --cases "$PERF_RUN_DIR/common-cases.json" --output "$PERF_RUN_DIR/common" --rounds 3

# 独立验收已有原始记录；输出到新 acceptance 文件，保留原失败信息。
python3 scripts/validate-performance-case.py \
  --cases "$PERF_RUN_DIR/request-cases.json" --run "$PERF_RUN_DIR/common" \
  --expected-seed cd2d68fe8d9832672797085a7f9f974ef6c39ad0922b4aac4e9ddd28b5beef7a \
  --output "$PERF_RUN_DIR/request-acceptance.json"
```

Runner 只运行已编译二进制，不自动编译、不访问外部模型。每项顶层测试、证据文件、请求／工具计数和种子身份由严格验证器检查；v2 额外逐条核对 held delay、实际 receiver 等待时间和所有存储快照的错误／operations 可用性。源码／二进制稳定性另核对。整进程 setup／teardown 指标与热区指标均保留，不能将其中一个替代另一个。

本轮 v2 重验输出为独立的 `request-acceptance-v2.json` 及各 suite 的 `acceptance-v2.json`，旧 identity、validator 哈希、raw 与失败记录保持原样。request 51 条的 `validated_rounds_success=true`，但 `original_suite_failed=true` 和总体 `success=false` 保留；其余正式 suite 及 profiles 均 `all_expected`，missing/incomplete/failed 为 0。复验只读小 JSON／日志，不重读种子、数据库或运行 benchmark。

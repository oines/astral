# 预置服务商的推理请求契约

核对日期：2026-10-23。本表覆盖 `ProviderPresets` 的全部 38 个预设。代码绑定位于 `internal/modelbridge/preset_reasoning.go`、`preset_protocols.go` 与 `preset_responses.go`（Responses 可选字段差异）；这份资料记录公开接口契约，不表示已使用用户账号逐个完成付费调用验证。

## 使用规则

推理请求格式决定字段位置，模型路由能力决定可以显示哪些选项。表中的格式名称**不是**整个平台全部模型的能力声明。`reasoning: true` 只表明模型可以推理，不能据此补出开关、预算或 `low / medium / high`。

运行时以当前连接、当前协议和完整模型 ID 为边界。模型显式覆盖、连接显式配置优先于本表；连接返回的能力资料优先于公共目录推荐值。原厂模型在不同网关上可采用不同控制方式。改变协议后重新选择对应协议的格式，不把 Chat 的扩展字段带入 Responses。

没有控制能力资料时允许使用接口默认；用户保存的显式选择无法编码时应返回具体错误。接口默认、Profile 默认、隐藏推理内容和关闭推理分别处理。下面提到的默认值只描述接口行为，不自动保存为 Profile 主动设置。

## 全量预设绑定

“标准兼容”表示使用该协议规定的字段且仍需该模型的能力资料；“端点扩展”表示已经核对该服务的专属字段。两者均不允许通过模型名称推导未经声明的档位。

| 预设 ID | 默认协议 / 格式 | 可选客户端协议 | 加密 reasoning / 自动 include | 已核对的控制与限制 | 官方依据 |
| --- | --- | --- | --- | --- | --- |
| `anthropic` | Messages / `anthropic` | Messages | 不适用：本表未声明 Responses | 根据能力选择旧预算或 adaptive；新模型可能强制推理，不能统一提供关闭。签名和完整 thinking block 保留重放。 | [Thinking](https://platform.claude.com/docs/en/build-with-claude/thinking) |
| `azure-openai` | Responses / `openai-responses` | Responses、Chat | 已证实；include 兼容接受，stateless 默认返回 | `reasoning.effort`；部署名不是原厂模型名，档位和关闭能力按部署路由确定。 | [Azure reasoning](https://learn.microsoft.com/en-us/azure/foundry/openai/how-to/reasoning) |
| `cerebras` | Chat / `openai-chat` | Chat | 不适用：本表未声明 Responses | 使用 `reasoning_effort`；GLM 4.7 的旧 `disable_reasoning` 已弃用，不能照搬 Z.AI 的 thinking 开关。 | [GLM 4.7 migration](https://inference-docs.cerebras.ai/resources/glm-47-migration) |
| `cloudflare` | Chat / `openai-chat` | Responses、Chat | 未获字段级证据；省略自动 encrypted include | 连接默认 Chat，仅两个 GPT-OSS 精确 ID 推荐 Responses；其他模型不可据供应商支持 Responses 就改协议。GLM 5.3 Flash 的 none 不能解释为关闭。 | [兼容路由](https://developers.cloudflare.com/workers-ai/configuration/open-ai-compatibility/)、[GLM 5.3 Flash](https://developers.cloudflare.com/workers-ai/models/glm-5.3-flash/) |
| `codex` | Responses / `openai-responses` | Responses | 已证实；官方客户端订阅契约 | 使用订阅端点的模型资料与 Responses effort；不能把公共 API 模型列表当作该账号授权清单。 | [Codex configuration](https://developers.openai.com/codex/config-reference) |
| `deepseek` | Responses / `openai-responses` | Responses、Chat、Messages | 明确无加密支持；省略 include | Responses 使用 `reasoning.effort`；不发送不支持的 `include/store`，原生明文 reasoning content 完整回放。Chat 才用 `thinking.type` 与 `reasoning_effort`。 | [Responses 契约](https://api-docs.deepseek.com/guides/responses_api/)、[Thinking](https://api-docs.deepseek.com/guides/thinking_mode/) |
| `fireworks` | Responses / `openai-responses` | Responses、Chat、Messages | include 未声明；保守省略 | Responses 使用标准 `reasoning`，保留 `store:false`；公开 schema 没有 `include`，不主动发送。Chat 才使用 `fireworks` 格式；Messages 将预算转换为 effort，不能宣称按精确预算执行。 | [Responses](https://docs.fireworks.ai/api-reference/post-responses)、[Messages](https://docs.fireworks.ai/tools-sdks/anthropic-compatibility)、[Reasoning](https://docs.fireworks.ai/guides/reasoning) |
| `github-copilot` | Chat / `openai-chat` | Responses、Chat | 未获字段级证据；省略自动 encrypted include | 连接默认 Chat，账号目录的 supported_endpoints 决定模型真实路由并优先 Responses；CLI OAuth token 直传，与 VSCode 内部 token 兑换分开。仅导入该账号权限内的库存。 | [CLI OAuth](https://docs.github.com/en/copilot/how-tos/copilot-cli/set-up-copilot-cli/authenticate-copilot-cli)、[GitHub adapter](https://github.com/github/gh-aw-firewall/blob/main/containers/api-proxy/providers/copilot.js) |
| `google` | Generate Content / `gemini` | Google | 不适用：本表未声明 Responses | `generationConfig.thinkingConfig`；2.5 预算与 3/4 系列 level 分开。`minimal` 不等于关闭；只有明确支持的模型才使用预算 0。 | [Generate Content thinking](https://ai.google.dev/gemini-api/docs/generate-content/thinking) |
| `groq` | Responses / `openai-responses` | Responses、Chat | include 明确不支持；省略 | Responses 使用 `reasoning.effort`，`include` 明确不支持；API reference 允许 `store:false/null`，不支持服务端续写。Chat 的 `reasoning_format/include_reasoning` 只控制呈现。 | [Responses](https://console.groq.com/docs/responses-api)、[API reference](https://console.groq.com/docs/api-reference)、[Reasoning](https://console.groq.com/docs/reasoning) |
| `huggingface` | Responses / `openai-responses` | Responses、Chat | 未获字段级证据；省略自动 encrypted include | 默认 Responses beta 已有官方路由；使用标准 `reasoning.effort`，实际能力依选中的上游和模型。完整 `repo:provider` ID 保留，不照搬原厂开关。 | [Responses beta](https://huggingface.co/docs/inference-providers/guides/responses-api)、[Chat](https://huggingface.co/docs/inference-providers/tasks/chat-completion) |
| `lmstudio` | Responses / `openai-responses` | Responses、Chat、Messages | 未获字段级证据；省略自动 encrypted include | Responses 自 0.3.29 提供，官方示例声明 `reasoning.effort`；Messages 自 0.4.1 提供。模型、模板与运行版本决定实际控制能力，不能把原生 `/api/v1` 参数套到兼容端点。 | [API changelog](https://lmstudio.ai/docs/developer/api-changelog)、[Responses](https://lmstudio.ai/docs/developer/openai-compat/responses)、[Messages](https://lmstudio.ai/docs/developer/anthropic-compat/messages) |
| `minimax` | Messages / `anthropic-adaptive` | Chat、Messages | 不适用：本表未声明 Responses | M3 使用 `adaptive`；M3.1 Flash 强制推理并接受 `output_config.effort`；旧 M2.x 关闭参数被忽略。不能给整个预设补 Anthropic 预算能力。 | [Anthropic compatibility](https://platform.minimax.io/docs/api-reference/text-anthropic-api) |
| `mistral` | Chat / `openai-chat` | Chat | 不适用：本表未声明 Responses | 原生 `reasoning_effort`；`none` 表示极少推理并省略思考输出，不能保证关闭。内容可能为 thinking/text 混合块，完整块需重放。 | [Reasoning](https://docs.mistral.ai/studio/conversations/reasoning) |
| `moonshot` | Chat / `thinking-type` | Responses、Chat、Messages | 密文固定 null；仅禁 encrypted include | 连接默认 Chat；精确 `kimi-k3` 新模型推荐 Responses，并使用 `reasoning.effort`；Chat K3 用顶层 effort，拒绝旧 thinking。Responses 的 include 只接受搜索来源/结果，不能请求 encrypted reasoning。 | [Responses schema](https://platform.kimi.com/docs/api/responses.md)、[Messages](https://platform.kimi.com/docs/api/messages)、[Thinking models](https://platform.kimi.com/docs/guide/use-thinking-models) |
| `newapi` | Responses / `openai-responses` | Responses、Chat、Messages、Google | 未获字段级证据；省略自动 encrypted include | 实例配置与上游转换决定能力；默认仅标准 Responses，不因 slug 自动选择原厂扩展。特殊实例可显式配置格式或有限映射。 | [Responses](https://docs.newapi.pro/en/api/openai-responses/) |
| `ollama` | Responses / `openai-responses` | Responses、Chat、Messages | 未获字段级证据；省略自动 encrypted include | Responses 为无状态兼容接口；Chat 的 `reasoning_effort` 与原生 `/api/chat` 的 `think` 分开。Messages 本地忽略密钥，云端必须 Bearer，不能仅发 `x-api-key`。 | [OpenAI compatibility](https://docs.ollama.com/api/openai-compatibility)、[Anthropic compatibility](https://docs.ollama.com/api/anthropic-compatibility) |
| `openai` | Responses / `openai-responses` | Responses、Chat | 已证实；include 兼容接受，stateless 默认返回 | `reasoning.effort`；档位与默认值随模型不同。某些新模型拒绝 `none`，不能将所有推理模型统一显示关闭。 | [Reasoning models](https://developers.openai.com/api/docs/guides/reasoning) |
| `openrouter` | Responses / `openai-responses` | Responses、Chat | 已证实；官方 SDK include enum | 默认 Responses 已证实 `reasoning.effort`。显式使用 Chat 时采用 `openrouter`，支持 `reasoning.enabled / effort / max_tokens`；以网关 `/models` 的 reasoning 能力为准。 | [Responses reasoning](https://openrouter.ai/docs/api_reference/responses/reasoning)、[Chat reasoning](https://openrouter.ai/docs/guides/best-practices/reasoning-tokens) |
| `perplexity` | Responses / `openai-responses` | Responses、Chat（旧 Sonar） | 未获字段级证据；省略自动 encrypted include | 新连接使用 `/v1/responses`；`reasoning.effort` 与平台顶层任务 `effort` 不同。旧 Sonar 连接保留其已存协议，迁移要求见下文。 | [OpenAI compatibility](https://docs.perplexity.ai/docs/agent-api/openai-compatibility)、[Migration](https://docs.perplexity.ai/docs/agent-api/migrate-from-sonar/how-to) |
| `qwen` | Responses / `openai-responses` | Responses、Chat、Messages | 未获字段级证据；省略自动 encrypted include | Responses 使用 `reasoning.effort` 等该接口声明字段；Chat 使用 `enable_thinking/thinking_budget`；Responses 旧 `enable_thinking` 将弃用，优先使用 effort。区域、模型、工作空间分别限制可用能力；旧域名仍可用。 | [Responses](https://help.aliyun.com/zh/model-studio/qwen-api-via-openai-responses)、[Deep thinking](https://www.alibabacloud.com/help/en/model-studio/deep-thinking)、[地域端点](https://help.aliyun.com/en/model-studio/beijing-access-information) |
| `together` | Chat / `together` | Chat | 不适用：本表未声明 Responses | 开关为 `reasoning.enabled`，effort 为顶层 `reasoning_effort`；不是 OpenRouter 的 effort 路径。强制推理、混合推理和可调 effort 模型分别声明能力。 | [Reasoning](https://docs.together.ai/docs/inference/chat/reasoning) |
| `xai` | Responses / `openai-responses` | Responses、Chat | 已证实；include 与原生加密回放 | `reasoning.effort`；当前 Grok 推理不能关闭，原生档位随模型不同。保留加密 reasoning item。 Anthropic 兼容已正式弃用，不列为新连接可选协议。 | [Reasoning](https://docs.x.ai/developers/model-capabilities/text/reasoning)、[Deprecated interfaces](https://docs.x.ai/developers/rest-api-reference/inference/legacy) |
| `zai` | Chat / `thinking-type` | Chat、Messages | 不适用：本表未声明 Responses | `thinking.type`，部分模型同时有 effort；GLM 5.3 系列强制推理。`clear_thinking` 是历史保留策略，不是开关。 | [Thinking mode](https://docs.z.ai/guides/capabilities/thinking-mode) |
| `zhipu` | Responses / `openai-responses` | Responses、Chat、Messages | 未获字段级证据；省略自动 encrypted include | Responses 基址 `/api/v1`，与 Chat 的 `/api/paas/v4`（Coding Plan 为 `/api/coding/paas/v4`）分开；原生 thinking 与历史回放分别处理，不能从 Responses 地址猜测模型目录。 | [Responses](https://docs.bigmodel.cn/cn/guide/develop/responses/introduction)、[Thinking](https://docs.bigmodel.cn/cn/guide/capabilities/thinking-mode)、[Coding Plan](https://docs.bigmodel.cn/cn/coding-plan/usage-notes) |
| `commandcode` | Responses / `openai-responses` | Responses、Chat、Messages | 未获字段级证据；省略自动 encrypted include | 标准 Responses；实时模型列表提供 `supported_endpoints`，Claude 路由要求 Messages，不能仅用 slug 给 Responses 添加 Anthropic 参数。 | [Provider API](https://commandcode.ai/docs/provider) |
| `opencode` | Chat / `openai-chat` | Responses、Chat、Messages、Google | 未获字段级证据；省略自动 encrypted include | 标准 Chat；不同模型分别有 Chat、Responses、Messages、Google 原生路由。应使用当前路由的契约，原厂能力不能改变已选协议。 | [Models and endpoints](https://opencode.ai/v2/docs/console/models/) |
| `opencode-go` | Chat / `openai-chat` | Responses、Chat、Messages、Google | 未获字段级证据；省略自动 encrypted include | 与 Console 分开的订阅入口；仅导入该入口能提供的模型，控制按实际路由能力确定。 | [OpenCode Go](https://opencode.ai/v2/docs/console/go/) |
| `mimo` | Responses / `openai-responses` | Responses、Chat、Messages | 未获字段级证据；省略自动 encrypted include | `none` 为关闭，其余接受值实际均为相同的开启行为，不能伪装成不同强度。Chat 才使用 `thinking.type`。 | [Responses compatibility](https://mimo.mi.com/docs/en-US/api/chat/responses) |
| `mimo-token-plan` | Responses / `openai-responses` | Responses、Chat、Messages | 未获字段级证据；省略自动 encrypted include | 使用 Token Plan 区域地址与相同协议字段；推理显示开关不能当作模型推理开关。 | [Token Plan](https://mimo.mi.com/docs/zh-CN/tokenplan/Token%20Plan/quick-access)、[Responses](https://mimo.mi.com/docs/zh-CN/api/chat/responses) |
| `minimax-token-plan` | Messages / `anthropic-adaptive` | Chat、Messages | 不适用：本表未声明 Responses | 与 MiniMax Messages 共用格式，不共用账号授权；M3、M3.1、M2.x 的能力差异仍逐模型处理。 | [Anthropic compatibility](https://platform.minimax.io/docs/api-reference/text-anthropic-api) |
| `zai-coding-plan` | Chat / `thinking-type` | Chat、Messages | 不适用：本表未声明 Responses | Coding Plan 为独立地址；Preserved Thinking 默认可与通用 API 不同，不把此差异当推理开关。 | [Thinking mode](https://docs.z.ai/guides/capabilities/thinking-mode) |
| `zhipu-coding-plan` | Responses / `openai-responses` | Responses、Chat、Messages | 未获字段级证据；省略自动 encrypted include | Responses 基址 `/api/v1`，与 Chat 的 `/api/paas/v4`（Coding Plan 为 `/api/coding/paas/v4`）分开；原生 thinking 与历史回放分别处理，不能从 Responses 地址猜测模型目录。 | [Responses](https://docs.bigmodel.cn/cn/guide/develop/responses/introduction)、[Thinking](https://docs.bigmodel.cn/cn/guide/capabilities/thinking-mode)、[Coding Plan](https://docs.bigmodel.cn/cn/coding-plan/usage-notes) |
| `bailian-coding-plan` | Chat / `qwen` | Chat、Messages | 不适用：本表未声明 Responses | 使用 coding.dashscope 路由与百炼控制；模型列表、预算与 effort 能力依当前套餐模型确定。 | [Coding Plan FAQ](https://www.alibabacloud.com/help/en/model-studio/coding-plan-faq)、[Deep thinking](https://www.alibabacloud.com/help/en/model-studio/deep-thinking) |
| `kimi-code` | Responses / `openai-responses` | Responses、Messages | 未获字段级证据；省略自动 encrypted include | 使用 Coding API 的模型 ID；`k3 / k3-256k / kimi-for-coding` 与开放平台 ID 不同。关闭可能路由到另一无思考模型，不能从 K3 原厂资料推导透明关闭。 | [Model configuration](https://www.kimi.com/code/docs/kimi-code/models.html)、[Codex integration](https://www.kimi.com/code/docs/third-party-tools/codex.html) |

| `floway` | Responses / `openai-responses` | Responses、Chat、Messages、Google | 网关源码已证实；实际密文取决于上游 | 独立本地网关，默认 `127.0.0.1:8788/v1`；模型声明与转换链决定能力。Gemini 客户端从同源 `/v1beta` 进入，不能发送到 `/v1/models/...`。 | [官方路由源码](https://github.com/Menci/Floway/blob/3919a92f90b2a00b54cc0e78b90fd614cf6f14f5/packages/protocols/src/common/data-plane-routes.ts) |
| `litellm` | Responses / `openai-responses` | Responses、Chat、Messages、Google | 未获字段级证据；省略自动 encrypted include | 独立网关，默认 `127.0.0.1:4000/v1`；保留部署模型别名，原生透传与协议翻译能力由网关配置决定。Gemini 客户端基址为同源 `/v1beta`。 | [Responses](https://docs.litellm.ai/docs/response_api)、[Messages](https://docs.litellm.ai/docs/anthropic_unified)、[Gemini 路由源码](https://github.com/BerriAI/litellm/blob/main/litellm/proxy/google_endpoints/endpoints.py) |
| `magpie` | Responses / `openai-responses` | Responses、Chat、Messages、Google | 未获字段级证据；省略自动 encrypted include | 独立网关，默认 `127.0.0.1:3425/v1`；本地 key 可标识 Astral，远程必须使用授权网关 key。Gemini 用同源 `/v1beta`，列表只描述用户启用的模型。 | [官方集成契约](https://usemagpie.ai/docs/integrate) |

## Responses 可选字段和历史回放

提供 Responses 路由，并不等于支持 OpenAI 的所有可选字段。SDK 的 `ResponsesCompatibility` 只约束自动附加字段；不能借此丢弃响应中已经返回的原生 reasoning item，也不能跨连接重用另一账号的回放身份。

| 端点 | 自动 include | store | reasoning 回放依据 |
| --- | --- | --- | --- |
| DeepSeek | 省略：官方明确不支持，未知参数静默忽略 | 省略：官方明确不支持，响应恒 false | 明文 `content` 归并 assistant；`summary/encrypted_content` 不支持。[兼容表](https://api-docs.deepseek.com/guides/responses_api/) |
| Groq | 省略：官方明确不支持 | 保留 false：具体 API reference 允许 false/null；指南的“不支持 store”指不提供状态存储 | 返回的 `reasoning.content` 原样回放，不假设存在密文。[Responses guide](https://console.groq.com/docs/responses-api)、[具体字段](https://console.groq.com/docs/api-reference) |
| Fireworks | 省略：公开 Create Response schema 未声明 include；这是保守客户端策略，不代表实测服务端拒绝 | 必须明确 false：官方默认 true，会保存响应 | 原生返回内容完整回放；未证明能够请求 OpenAI encrypted include。[官方 OpenAPI](https://docs.fireworks.ai/responses.openapi.yaml) |
| 百炼 Qwen | 省略：当前参数表没有 include；不宣称已验证服务端拒绝 | 必须明确 false：官方默认 true | 以实际返回的 reasoning item 回放，不推导原厂加密能力。[官方参数表](https://help.aliyun.com/zh/model-studio/qwen-api-via-openai-responses) |
| Moonshot 开放平台 K3 | 保留 include 功能，只禁 `reasoning.encrypted_content`；enum 仅允许搜索结果/来源 | 本地完整历史；响应固定 false，不能用 previous_response_id 续写 | 官方 schema 的 reasoning.encrypted_content 固定 null，不能宣称自动返回密文；与 Kimi Code 订阅契约分开。[原始 schema](https://platform.kimi.com/docs/api/responses.md) |

自动 `reasoning.encrypted_content` 的已证实名单为 OpenAI、Azure OpenAI、Codex、OpenRouter、xAI；Floway 另有网关源码证据，但其当前模型能否生成密文仍由上游决定。[OpenAI stateless reasoning](https://developers.openai.com/api/docs/guides/reasoning)、[Azure](https://learn.microsoft.com/en-us/azure/ai-foundry/openai/how-to/reasoning)、[Codex 官方客户端](https://github.com/openai/codex/blob/main/codex-rs/core/src/client.rs)、[OpenRouter 官方 SDK enum](https://github.com/OpenRouterTeam/typescript-sdk/blob/main/src/models/responseincludesenum.ts)、[xAI](https://docs.x.ai/developers/model-capabilities/text/reasoning)、[Floway 原生协议与 include](https://github.com/Menci/Floway/blob/3919a92f90b2a00b54cc0e78b90fd614cf6f14f5/packages/protocols/src/openai-responses/index.ts)

MiMo（包括 Token Plan）的 Responses 页没有声明 include/encrypted，且警告未定义参数可能被过滤或报错；Kimi Code 的 Codex 集成文档证明 Responses/reasoning 支持，却没有字段级加密保证，因此两者不能仅因“Codex 可接入”就视为已证实 encrypted include。其他网关能转发 reasoning，同样不自动证明每个上游都接受该 include。省略请求该字段与保留实际返回的 opaque item 是两个独立行为。[MiMo 参数](https://mimo.mi.com/docs/en-US/api/chat/responses)、[Kimi Code](https://www.kimi.com/code/docs/third-party-tools/codex.html)

其他端点若未找到字段级证据，应保留“未知”，默认省略自动 encrypted include，不能把“兼容 OpenAI”推导为全部字段已经验证。原生推理明文、摘要与加密内容是三种不同的服务端输出形式。

## 模型目录和导入边界

表格中的协议集合是官方声明的客户端接口集合，不表示所有账号、所有模型、所有版本均支持这些接口，也不表示已逐家登录、调用或成功发现模型。Fireworks 的 Messages 与三家网关的 Google 是本次官方资料审查确认的接口，应与代码声明同步。

模型发现和推理基址是两个契约。已知网关的标准根路径可以按协议转换：Responses/Chat/Messages 使用 `/v1`，Google 使用 `/v1beta`；自定义子路径和反向代理地址不能擅自改写。LiteLLM 还兼容不带版本的 `/models/:model:generateContent`，Astral 统一选择正式 `/v1beta` 路由。

- Codex 与 GitHub Copilot 必须使用当前账号授权的目录。公共目录只能补充元数据，不能补出该账号未返回的库存；401 与订阅/组织策略导致的 403 分开处理。
- DeepSeek、Groq、Fireworks、Hugging Face、OpenAI/OpenRouter、Google、LM Studio/Ollama 及自建网关有各自模型列表接口，但返回形态、限制字段、权限与本地安装状态各异。提供列表端点不保证列表包含足够上下文和输出限制，不能因此杜撰参数或声称通过了连通性验证。
- Azure 的部署名与公共模型 ID 不同；不能把公共 OpenAI 模型表作为该 Azure 资源已部署模型。使用用户的部署名与相应资源权限。
- 智谱官方 Responses 文档只声明 `/api/v1/responses` 及响应检索、删除接口，没有在该基址声明 `/models`。不能把 HTTP 200 的错误对象解释成空库存，也不能无依据把 `/api/paas/v4/models` 认作官方保证；缺少可靠目录时，通过公共资料推荐并由用户明确选择模型。
- MiniMax 及 Token/Coding Plan 等订阅入口不能因为存在 Chat/Messages 推理路由，就假定同一路径提供 `/models`。这不是断言所有此类入口永远没有模型列表，而是当前没有足够契约保证统一发现。当前账号模型发现不可用时，保留明确错误，允许按官方公开模型资料手工输入/导入；不要虚构“已发现”状态。
- Floway、LiteLLM、Magpie 的库存属于各自独立网关连接。模型完整 ID 和网关别名保留，不能用公共目录覆盖网关返回的限制、价格或明确关闭的能力。

本次审查没有以真实账号逐一验证 38 家 `/models`；“官方提供列表”“本地无密钥请求得到错误”“当前凭据真实发现成功”必须在验收记录里分别表述。

## Perplexity 新连接与旧连接

官方声明 Sonar Chat Completions 的支持于 2026-09-27 结束，但同步和流式请求仍继续工作，由服务端逐步转换为 Agent API 请求；停止支持不等于旧请求立即全部失效。[官方迁移说明](https://docs.perplexity.ai/docs/agent-api/migrate-from-sonar/overview)

新连接的标准入口为 `https://api.perplexity.ai/v1`，使用 Responses，`/v1/responses` 是 `/v1/agent` 的正式别名。`GET /v1/models` 给出 Agent API 的模型列表。可直接指定完整模型 ID，例如 `openai/gpt-5.6-sol`，沿用 Responses 的请求和流式响应处理。[兼容接口](https://docs.perplexity.ai/docs/agent-api/openai-compatibility)

旧连接不能自动改协议或模型 ID：旧 `sonar*` 模型、内置搜索及参数与新 Agent API 不完全等价。用户迁移时新建连接或显式修改连接，重新发现模型；不要把 Sonar 名称静默映射到某个新模型。平台顶层 `effort` 选择任务配置，`reasoning.effort` 调节模型推理，两者不能合并成一个选项。[迁移参数表](https://docs.perplexity.ai/docs/agent-api/migrate-from-sonar/how-to)

## 回归约束

1. 全部预设必须有显式审计绑定；新增或删除预设时覆盖测试要求同步更新。
2. 同一远端模型经过原厂、百炼、Together、Fireworks、OpenRouter 时，字段跟随当前连接契约，不跟随模型 slug。
3. 预设更换调用协议后必须重新解析格式；用户显式覆盖仍按既定优先级生效。
4. 显式不支持、空档位集合和强制推理不能被低优先级目录补成可控。`none` 的语义必须逐端点确认。
5. 请求参数与回传参数分别验证：`reasoning_content`、`reasoning`、`reasoning_details`、Messages thinking/signature、Responses reasoning item、Google thought signature 不能互相猜测替代。
6. 固定能力规则只描述已经证实的控制契约；模型名称、价格、上下文限制和可用清单仍由运行时目录/连接发现维护，不新增完整静态模型快照。

# 网页搜索

实现与资料核对日期：2026-10-22。界面使用 Bot；后端实体仍名为 Agent。

在“设置 → 网页搜索”添加连接，再在具体 Bot 的基本设置中选择该服务。每个 Bot 同时使用一个搜索实例，调用在 Astral Server 上执行，不使用当前电脑的命令环境。一次调用失败时不会自动尝试其他服务。

连接名称、服务类型、Base URL、普通配置与密钥作为一个草稿，在“保存连接”后整体生效。测试只使用已保存的连接；草稿未保存时不能测试。更换适配器不会将原适配器的密钥带给另一个服务。密钥输入框留空表示保留；多个密钥字段可以单独更新。

## 模型工具

```json
{
  "query": "release notes",
  "domains": ["example.com"],
  "recency": 14,
  "limit": 20
}
```

`query` 必填且不能全为空白。其余三个参数可省略：

- `domains`：域名数组，接受国际化域名并转为 ASCII，去除首尾空白、重复项和结尾的点。拒绝 URL、路径、端口、通配符及 IP 地址。空数组等同于未提供。
- `recency`：正整数，表示过去多少天。需要绝对日期的服务以调用时的 UTC 日期计算范围；只支持离散时间段的服务明确拒绝其他数值。
- `limit`：正整数，直接映射到上游数量参数。Astral 不把它改成默认值或强制压到某个上限；超出服务允许范围时由上游返回错误。没有原生数量参数的接口明确拒绝此参数。

未提供可选参数时，Astral 不发送对应的上游字段。也不额外指定搜索深度、Exa 内容选项、DeepSeek `need_content` 或 Bocha `summary`，由服务自身决定默认行为。

输出统一为 `provider_id`、`query`、`results`；每条结果提供 `title`、`url`、`summary`，以及可用的 `published_at`、`author`。保留每一条有效 HTTP(S) 来源及其完整摘要，保持上游顺序。没有固定 5 条结果、900 字符、8,000 Token 或 4 MiB 响应正文限制。多段 highlights、extra snippets、Yandex passages 完整拼接。失效或不安全的来源 URL 不作为可点击结果返回。

“不限制输出”不改变上游服务自身的默认数量、最大数量、摘要长度、计费规则或模型的上下文容量，也不意味着自动翻页或抓取完整网页。请求仍可取消，并有 40 秒超时。

## 连接与参数映射

Base URL 留空时使用下表官方地址；SearXNG 必须填写自己的实例地址。可以填写根地址、带代理前缀的地址、带版本前缀的地址或完整搜索端点；Astral 会避免重复追加同一段路径。远程地址要求 HTTPS；本机回环地址允许 HTTP。

| 服务 | 默认端点 | 必需凭据或配置 | `domains` | `recency` | `limit` |
| --- | --- | --- | --- | --- | --- |
| Tavily | `https://api.tavily.com/search` | API Key | `include_domains` | `start_date` | `max_results` |
| Exa | `https://api.exa.ai/search` | API Key | `includeDomains` | `startPublishedDate` | `numResults` |
| DeepSeek Search | `https://api.deepseek.com/v1/tools/search` | API Key | 查询中的 `site:`，多域使用 OR | 查询中的 `after:` | `count` |
| Brave | `https://api.search.brave.com/res/v1/web/search` | API Key | 查询中的 `site:` | `freshness` 日期范围 | `count` |
| Google Custom Search | `https://customsearch.googleapis.com/customsearch/v1` | API Key、搜索引擎 ID `cx` | 查询中的 `site:` | `dateRestrict=dN` | `num` |
| Sogou / Tencent | `https://wsa.tencentcloudapi.com/` | SecretId、SecretKey；开通元宝搜索 | `Site`，只支持一个域名 | `Freshness` 日期范围 | `Cnt` |
| Serper | `https://google.serper.dev/search` | API Key | 查询中的 `site:` | `tbs=qdr:dN` | `num` |
| SearXNG | 自建实例的 `/search` | 实例 URL；可选 Bearer token | 查询中的 `site:`，效果取决于实例启用的引擎 | 1 / 30 / 365 天映射到 day / month / year | 不支持，明确报错 |
| Jina Search | `https://s.jina.ai/search` | API Key | 查询中的 `site:` | 不支持，明确报错 | `count` |
| Bocha | `https://api.bocha.cn/v1/web-search` | API Key | `include` | `freshness` 日期范围 | `count` |
| DuckDuckGo | `https://html.duckduckgo.com/html/` | 无 | 查询中的 `site:` | 1 / 7 / 30 / 365 天映射到 d / w / m / y | 不支持，明确报错 |
| Yandex | `https://searchapi.api.cloud.yandex.net/v2/web/search` | API Key、Folder ID；可选搜索区域，默认 International | 查询中的 `site:`，多域使用 `\|` | 1 / 14 / 30 天映射到原生 period | `GROUP_MODE_FLAT` + `groupsOnPage` |

使用查询运算符的服务由上游搜索引擎执行筛选；Astral 不在返回后自行裁剪结果来模拟筛选或数量限制。

Sogou 适配器使用腾讯云 `SearchPro`、版本 `2025-05-08` 和 TC3-HMAC-SHA256 签名。腾讯云当前将此产品称为“元宝搜索”；返回的 `Pages` JSON 字符串会逐项解码。Yandex 使用 v2 REST API，解码 `rawData` 中的 Base64 XML，保留全部分组内的文档与 passages。

DuckDuckGo 与 Memoh 的对应实现一样使用公开 HTML 搜索页，并非官方付费搜索 API。出现浏览器验证挑战时报告失败，不绕过验证，也不把挑战页当成“没有结果”。SearXNG 实例必须启用 JSON 输出。

Google Custom Search JSON API 已关闭新客户接入，现有客户须在 **2027-01-01** 前迁移；界面保留存量客户入口并显示说明。[Google 官方状态](https://developers.google.com/custom-search/v1/overview)。Bing 搜索入口已按产品决定移除。

## Owner API 与存储

- `GET /api/v1/search-adapters`：可用服务、默认 Base URL、连接字段和字段选项；UI 使用这一目录，不再维护第二份服务列表。
- `GET /api/v1/search-providers`：保存的连接，只有普通 `config`、`credential_id`、`credential_fields`，不返回密钥。
- `POST /api/v1/search-providers`：现有连接元数据加 `config` 与 `credentials` 字典；`credentials` 是字段级补丁，未提交的密钥保留。显式空字符串表示删除该密钥，但必填密钥不能删除。兼容旧版 `api_key` 入参；与 `credentials.api_key` 冲突时拒绝。
- `POST /api/v1/search-providers/{id}/test`：与模型工具相同的四个参数，直接测试指定的已保存连接。

所有 secret 字段作为一个版本化 JSON 信封存入现有加密 Secret Store；原有单 API Key 密文仍可读取。普通配置拒绝 secret 字段和未知字段。保存沿用 revision 冲突检查，新凭据与连接文档在同一事务写入。不能通过提交其他连接的 `credential_id` 引用其密钥。

## 资料与验证边界

请求格式依据本地 astral-code 的可选参数语义、Memoh 的服务范围与以下官方资料核对。适配器的本地 HTTP 协议测试不等于所有付费账号的线上验收；没有使用用户的真实搜索凭据。

- [Tavily Search](https://docs.tavily.com/documentation/api-reference/endpoint/search)、[Exa Search](https://docs.exa.ai/reference/search)、[DeepSeek Search](https://api-docs.deepseek.com/guides/search/)。
- [Brave Web Search](https://api-dashboard.search.brave.com/app/documentation/web-search/get-started)、[Google cse.list](https://developers.google.com/custom-search/v1/reference/rest/v1/cse/list)、[腾讯 SearchPro](https://cloud.tencent.com/document/api/1806/121811)。
- [Serper](https://serper.dev)、[SearXNG Search API](https://docs.searxng.org/dev/search_api.html)、[Jina 官方 OpenAPI](https://docs.jina.ai/openapi.json)。
- [Bocha 官方开放平台](https://open.bocha.cn)、[Bocha 官方插件协议类型](https://github.com/bocha-ai/dsh-web-search-bocha/blob/main/src/types.ts)、[DuckDuckGo 查询语法](https://duckduckgo.com/duckduckgo-help-pages/results/syntax)。
- [Yandex WebSearch REST](https://aistudio.yandex.ru/en/docs/search-api/api-ref/WebSearch/search)、[Yandex 站点筛选语法](https://yandex.com/support/search/en/query-language/qlanguage)。

具体测试和浏览器证据见 [验证记录](verification.md)。

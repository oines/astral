<h1 align="center">
  <picture>
    <source media="(prefers-color-scheme: dark)" srcset="packages/icons/assets/astral-lockup-inverse.svg">
    <img src="packages/icons/assets/astral-lockup.svg" alt="Astral" width="260" height="80">
  </picture>
</h1>

<p align="center"><strong>一个 Agent，在你的所有电脑上工作。</strong></p>
<p align="center"><a href="README.md">English</a> · 简体中文</p>

Astral 是一个开源、可自托管的个人 AI Agent。它可以在同一个会话中使用你的笔记本、台式机和服务器，读写文件、运行命令，调用你已经安装好的工具。

你可以通过桌面应用或浏览器使用 Astral，也可以把它连接到聊天软件，在私聊或群聊里交代任务。

## 一个会话，跨多台电脑完成工作

读取 MacBook 上的项目说明，在 Linux 服务器上运行测试，再到 Windows 台式机上检查构建。整个任务都在同一个 Session 中，上下文、执行记录和结果持续保留。

每台电脑提供自己的文件和运行环境。Astral 可以根据任务在它们之间切换，文件和已有进程仍留在原来的电脑上。

## 把 Astral 带进你的聊天

在 Telegram、QQ、微信或飞书里交代任务，让它使用你的电脑完成任务，再把结果发回聊天。

在群聊中，成员可以提出任务、补充要求、继续追问。默认通过 @ 或回复唤起 Astral；你也可以开启主动参与，让它根据收到的讨论判断是否加入。

每个聊天有独立的会话，也能在 Astral 中打开并继续处理。Telegram、QQ 和飞书支持群聊，微信目前仅支持私聊。配置方法见[聊天平台接入指南](https://astral.oines.dev/zh-cn/docs/chat/overview/)。

## 用上电脑里已有的工具

命令行工具、脚本、项目文件和现有的软件环境，都可以成为 Astral 完成任务的一部分。整理资料、生成报告、处理文件或开发项目，用上适合这项工作的工具。

## 运行在你自己的设备上

在 macOS、Windows 或 Linux 上使用桌面应用，或将 Astral Server 部署到自己的服务器，通过浏览器访问。连接你选择的模型服务，决定每个 Bot 可以使用哪些电脑和聊天。

## 开始使用

macOS、Windows 和 Linux 桌面安装包的发布状态、下载与源码构建步骤见[获取 Astral](https://astral.oines.dev/zh-cn/docs/start/get-astral/)。已发布的安装包、发行说明和校验和见 [GitHub Releases](https://github.com/oines/astral/releases)。Linux VPS 的无界面部署见 [Server 安装指南](https://astral.oines.dev/zh-cn/docs/hosting/server/)。

从源码构建时，先安装 [mise.toml](mise.toml) 固定的开发工具版本。Core 及其 Models 包的依赖版本已固定在 `go.mod`，无需另行下载 Core 源码。macOS 或 Linux 执行：

```sh
git clone https://github.com/oines/astral.git
cd astral
pnpm install --frozen-lockfile
GOWORK=off pnpm build:distribution
```

Windows PowerShell 步骤见[源码构建说明](https://astral.oines.dev/zh-cn/docs/start/get-astral/#从源码构建)。`GOWORK=off` 可避免本地工作区覆盖固定依赖。

选择运行方式：

- **桌面应用**：[下载或构建当前平台的安装包](https://astral.oines.dev/zh-cn/docs/start/get-astral/)，由应用管理本地 Server 和电脑连接。
- **服务器**：[安装已发布版本或运行从源码构建的程序](https://astral.oines.dev/zh-cn/docs/hosting/server/)，再设置网页登录和远程访问。

首次启动后：

1. 连接模型服务，选择要使用的模型。
2. 选择或接入一台电脑，授权给默认的 **Astral** Bot。
3. 开始一段会话，告诉它你想做什么。

需要接入聊天软件时，打开 **设置 → Bot**，选择 Bot 后点击 **连接平台**。之后可以继续添加电脑，并调整每个 Bot 的使用权限。

## 文档

访问[官网与用户文档](https://astral.oines.dev/zh-cn/)。源码统一维护在 `apps/website/` 中。

| 指南 | 内容 |
| --- | --- |
| [首次配置](https://astral.oines.dev/zh-cn/docs/start/first-steps/) | 模型连接与第一个任务 |
| [连接电脑](https://astral.oines.dev/zh-cn/docs/computers/connect/) | 机器接入、Bot 授权与跨电脑工作 |
| [日常使用](https://astral.oines.dev/zh-cn/docs/usage/conversations/) | 会话、文件、模型与技能 |
| [聊天平台](https://astral.oines.dev/zh-cn/docs/chat/overview/) | 私聊、群聊及各平台配置 |
| [自行部署](https://astral.oines.dev/zh-cn/docs/hosting/server/) | Server 启动、网页登录与网络访问 |
| [升级与备份](https://astral.oines.dev/zh-cn/docs/hosting/maintenance/) | 保存历史和工作文件 |

## 开发

安装依赖后，启动后端：

```sh
GOWORK=off pnpm dev:server
```

另开一个终端，启动 Web 应用：

```sh
pnpm dev
```

桌面开发、本地模型测试服务和检查命令见[开发指南](docs/development.md#development-and-verification)。参与贡献请阅读 [CONTRIBUTING.md](CONTRIBUTING.md)；界面工作遵循 [UI/UX 规范](docs/ui-ux-spec.md)。安全问题请通过 [SECURITY.md](SECURITY.md) 中的私密渠道反馈。

Astral 使用 [Astral Core monorepo](https://github.com/oines/astral-core)：Core 包管理持久化会话，Models 包负责模型服务协议、认证与模型元数据。

## 许可

Copyright (C) 2026 oines and Astral contributors.

项目采用 [GNU Affero General Public License, version 3 only](LICENSE)（`AGPL-3.0-only`），不提供任何担保。第三方组件保留各自的许可，详见 [THIRD_PARTY.md](THIRD_PARTY.md) 及发行包附带的声明。

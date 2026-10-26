---
title: "获取 Astral"
description: "从源码运行 Astral，选择桌面端或自行部署。"
---

Astral 的源码已公开。目前尚未提供公开发行的安装包；可先从源码构建。后续安装包以 [GitHub Releases](https://github.com/oines/astral/releases) 实际发布的内容为准。

## 准备工具

在构建电脑上安装仓库固定的工具链：Go **1.26.3**、Node.js **26.7.0** 和 pnpm **10.31.0**。也可以使用仓库的 `mise.toml` 安装相应版本。

```sh
git clone https://github.com/oines/astral.git
cd astral
pnpm install --frozen-lockfile
```

接下来的命令都在仓库根目录运行。源码工作区中的 `go.work` 若引用了你机器上不存在的相邻项目，`GOWORK=off` 会让构建使用 `go.mod` 中的依赖。

## 构建 Server 和产品界面

macOS 或 Linux：

```sh
GOWORK=off pnpm build:distribution
```

Windows PowerShell：

```powershell
$env:GOWORK = 'off'
pnpm build:distribution
```

生成的可执行文件位于 `dist/bin/`。选择与运行机器的操作系统和 CPU 架构匹配的文件；`apps/web/dist/` 是 Astral 产品界面，`apps/website/` 是官网和本使用文档。

[运行 Server](/zh-cn/docs/hosting/server/)说明如何启动、设置网页登录以及开放远程访问。

## 在桌面上运行

完成上面的发行文件构建后运行：

```sh
pnpm dev:desktop
```

Desktop 管理本机运行服务，打开后可直接进行[首次配置](/zh-cn/docs/start/first-steps/)。如果需要安装包，在对应操作系统上运行：

| 构建系统 | 命令 |
| --- | --- |
| macOS | `pnpm --filter @astral/desktop package` |
| Windows | `pnpm --filter @astral/desktop package:windows` |
| Linux | `pnpm --filter @astral/desktop package:linux` |

自行构建的安装包不等于经过官方签名和公证的发行包。构建输出在 `apps/desktop/release/`。

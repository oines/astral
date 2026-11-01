---
title: "从源码构建"
description: "为开发或自行打包，构建 Astral Server 和桌面版。"
---

本文适合需要修改 Astral 或自行打包的开发者。日常使用可以直接[安装发行版](/zh-cn/docs/start/get-astral/)。下面的命令在 Astral 仓库根目录运行。

## 准备工具和源码

安装仓库指定的 Go **1.26.3**、Node.js **26.7.0** 和 pnpm **10.31.0**。这些版本记录在 `mise.toml` 中，也可以用 mise 管理。

取得代码并安装依赖：

```sh
git clone https://github.com/oines/astral.git
cd astral
pnpm install --frozen-lockfile
```

Go 依赖版本已经固定在 `go.mod`。下面使用 `GOWORK=off`，避免你电脑上的工作区文件改用其他本地依赖。

## 构建程序

在 macOS 或 Linux 执行：

```sh
GOWORK=off pnpm build:distribution
```

在 Windows PowerShell 执行：

```powershell
$env:GOWORK = 'off'
pnpm build:distribution
```

完成后，`dist/bin/` 包含 macOS、Linux、Windows 的 x86_64 和 ARM64 程序。每个 Server 程序都包含网页界面。选择与你运行机器匹配的文件；后续手动启动和设置登录见[前台运行](/zh-cn/docs/hosting/server-options/#手动前台运行)。

只需要构建当前系统的 Server 时，可以将 `build:distribution` 换成 `build`。需要打包桌面版或让 Server 创建容器电脑时，使用完整发行构建，以提供对应平台的程序。

## 运行或打包桌面版

先完成上面的完整发行构建，再启动开发版桌面应用：

```sh
pnpm dev:desktop
```

需要生成安装包时，在对应系统执行：

| 当前构建系统 | 命令 |
| --- | --- |
| macOS | `pnpm --filter @astral/desktop package` |
| Windows | `pnpm --filter @astral/desktop package:windows` |
| Linux | `pnpm --filter @astral/desktop package:linux` |

输出位于 `apps/desktop/release/`。自行向他人分发时，还需要按目标系统处理签名和公证。

## 修改界面或使用文档后怎么检查

Astral 产品界面位于 `apps/web/`。修改后运行项目的 `pnpm typecheck`、`pnpm test` 和相应构建。

官网及使用文档位于 `apps/website/`，可以单独预览和检查：

```sh
pnpm dev:website
```

终端会显示本地预览地址。完成文档修改后运行：

```sh
pnpm check:website
```

网站是独立静态站点，不需要重新打包 Server 或桌面版。

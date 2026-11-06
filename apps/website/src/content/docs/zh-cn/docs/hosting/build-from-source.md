---
title: "从源码构建"
description: "为开发或自行打包，构建 Astral Server 和桌面版。"
---

本文适合需要修改 Astral 或自行打包的开发者。日常使用可以直接[安装发行版](/zh-cn/docs/start/get-astral/)。下面的命令在 Astral 仓库根目录运行。

## 准备工具和源码

安装仓库指定的 Go **1.26.4**、Node.js **26.7.0** 和 pnpm **10.31.0**。这些版本记录在 `mise.toml` 中，也可以用 mise 管理。

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

## 贡献与发布

通过 Pull Request 提交修改和相应测试；行为变化时同步更新公开使用文档。合并到 `main` 的应用修改自动进入同一条滚动发行流水线。日常发布无需维护者修改包版本、在本机创建发行 Tag，或再次运行候选构建。

只有产品代码、打包资源、安装器、共享依赖及构建或签名输入变更才自动发版。仅修改 README、文档、官网、测试或 CI 配置时保留对应检查，不启动应用发行。共享根包清单和依赖锁仍算构建输入；嵌入提示词和打包的许可证声明属于产品资源。

准备流水线在编译前固定源码提交、构建身份与应用版本。版本采用 `MAJOR.MINOR.PATCH`；当前 `0.1.x` 系列按准备流水线编号生成补丁号。失败或取消可能产生跳号，同一次准备任务重跑仍使用原版本。源码中的版本是开发基线；CI 在临时工作目录中将固定的发行版本同时用于 Go 和 Electron。

测试与统一程序构建并行执行。准备成功后，发行流水线只签名一次 Darwin 程序，所有桌面包都包含同一份已签名字节。Intel 和 Apple 芯片的 Mac 包分别在对应的原生系统上并行构建和验收。原生安装、升级、数据保留、迁移验证、Developer ID 签名和 Apple 公证通过仍是发布条件。

流水线随后封存最终安装包、manifest、更新清单、发行说明与校验和。发布直接上传这些文件，不重新构建或签名。CI 在已验证的源码提交上创建版本的注释 Tag，并通过 OIDC 身份为封存清单生成来源证明；公开发行附有源码与构建证据。发布后，独立检查再从真实公开链接安装 Server，并从未经修改的上一官方桌面版验证升级。

连续合并产品修改会替换过时的准备任务和等待中的旧发行请求；已经开始签名、验收或上传的发行任务会完成。必需检查失败时停止发布；公开后的验收失败保留在 Actions 中，不覆盖已公开版本的文件。重试前先在 [Actions 记录](https://github.com/oines/astral/actions)中确认失败阶段和保留的诊断。

自动发布不替用户决定何时重启已安装的 Astral。桌面版检查新版本，由用户选择安装时机，见[升级与备份](/zh-cn/docs/hosting/maintenance/)。下载页自动检查 GitHub Latest；暂时取不到最新信息时，页面上标明版本、已经验证的安装包仍可使用，无需每次发行再修改官网版本。

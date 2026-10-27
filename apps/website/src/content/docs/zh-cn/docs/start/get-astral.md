---
title: "获取 Astral"
description: "下载适用于 macOS、Windows 或 Linux 的 Astral 桌面版，或一键安装到 Linux VPS。"
---

下载 Astral 桌面版，或将 Astral Server 安装到 Linux VPS。

<p><span data-astral-release-status data-fallback-version="0.1.0" data-latest-prefix="最新发行版：" aria-live="polite">可用版本：v0.1.0</span> · <a data-astral-release-notes href="https://github.com/oines/astral/releases/tag/v0.1.0">发行说明、全部文件与校验和</a> · <a href="https://github.com/oines/astral/releases/latest">在 GitHub 查看最新版</a></p>

## 下载桌面版

| 平台 | 架构 | 下载 |
| --- | --- | --- |
| macOS | Apple 芯片 | <a data-astral-download="mac-arm64-dmg" href="https://github.com/oines/astral/releases/download/v0.1.0/Astral-0.1.0-mac-arm64.dmg">DMG</a> |
| macOS | Intel | <a data-astral-download="mac-x64-dmg" href="https://github.com/oines/astral/releases/download/v0.1.0/Astral-0.1.0-mac-x64.dmg">DMG</a> |
| Windows | x64 | <a data-astral-download="win-x64-exe" href="https://github.com/oines/astral/releases/download/v0.1.0/Astral-0.1.0-win-x64.exe">EXE 安装包</a> |
| Windows | ARM64 | <a data-astral-download="win-arm64-exe" href="https://github.com/oines/astral/releases/download/v0.1.0/Astral-0.1.0-win-arm64.exe">EXE 安装包</a> |
| Linux | x86_64 | <a data-astral-download="linux-x64-appimage" href="https://github.com/oines/astral/releases/download/v0.1.0/Astral-0.1.0-linux-x86_64.AppImage">AppImage</a> · <a data-astral-download="linux-x64-deb" href="https://github.com/oines/astral/releases/download/v0.1.0/Astral-0.1.0-linux-amd64.deb">DEB</a> |
| Linux | ARM64 | <a data-astral-download="linux-arm64-appimage" href="https://github.com/oines/astral/releases/download/v0.1.0/Astral-0.1.0-linux-arm64.AppImage">AppImage</a> · <a data-astral-download="linux-arm64-deb" href="https://github.com/oines/astral/releases/download/v0.1.0/Astral-0.1.0-linux-arm64.deb">DEB</a> |

- **macOS**：打开 DMG，将 Astral 拖入“应用程序”。官方 Mac 安装包已使用 Developer ID 签名；该版本的 Apple 公证状态见发行说明或签名报告（`mac-signing-report.json`）。
- **Windows**：运行 EXE，为当前用户安装。Windows 安装包尚未签名，可能显示 SmartScreen 提示。
- **Linux**：为 AppImage 添加执行权限后打开，或通过包管理器安装 DEB。AppImage 支持应用内更新；DEB 需要手动安装新版。

Windows 和 Linux 的窗口控制按钮位于应用顶部右侧，侧栏开关在左侧；按 Alt 可显示应用菜单。

打开 Astral 后，按[首次配置](/zh-cn/docs/start/first-steps/)连接模型并选择电脑。应用内更新和 Host 3 / Core 11 数据基线见[升级与备份](/zh-cn/docs/hosting/maintenance/)。Desktop 使用 `schema-3-core-11/`，旧私有版本的数据目录保留，不会自动导入。

## 一键安装到 Linux VPS

```sh
curl --fail --silent --show-error --location --proto '=https' --proto-redir '=https' \
  https://github.com/oines/astral/releases/latest/download/install-server.sh | bash
```

支持 Linux x86_64 和 ARM64，需要可用的 systemd 用户会话。安装器会注册当前用户的服务，并输出设置网页登录和访问 Astral 的命令。环境要求与远程访问步骤见[运行 Server](/zh-cn/docs/hosting/server/)。

## 从源码构建

### 准备工具

在构建电脑上安装仓库固定的工具链：Go **1.26.3**、Node.js **26.7.0** 和 pnpm **10.31.0**。也可以使用仓库的 `mise.toml` 安装相应版本。

```sh
git clone https://github.com/oines/astral.git
cd astral
pnpm install --frozen-lockfile
```

Core 及其 Models 包的依赖版本已固定在 `go.mod`，无需另行下载 Core 源码。`GOWORK=off` 可避免本地 `go.work` 覆盖固定依赖。接下来的命令都在 Astral 仓库根目录运行。

### 构建 Server 和产品界面

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

### 从源码运行桌面版

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

<script is:inline type="module" src="/scripts/release-downloads.js"></script>

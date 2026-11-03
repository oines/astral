---
title: "下载与安装"
description: "在自己的电脑上安装 Astral，或选择在长期运行的服务器上使用。"
---

**第一次使用，建议安装桌面版。** 下载适合自己电脑的安装包，打开应用后就能开始配置。

如果你希望 Astral 在另一台长期在线的 Linux 服务器上运行，并通过浏览器使用，请看下方的[服务器安装](#一键安装到-linux-vps)。两种方式任选一种即可。

<span id="下载桌面版"></span>

## 选择桌面安装包

<p><span data-astral-release-status data-fallback-version="0.1.13" data-latest-prefix="最新发行版：" aria-live="polite">可用版本：v0.1.13</span> · <a data-astral-release-notes href="https://github.com/oines/astral/releases/tag/v0.1.13">发行说明、全部文件与校验和</a> · <a href="https://github.com/oines/astral/releases/latest">在 GitHub 查看最新版</a></p>

先确认自己的电脑类型：Mac 使用 Apple M 系列芯片时选“Apple 芯片”，使用 Intel 时选“Intel”；采用 Intel 或 AMD 处理器的 Windows/Linux 电脑选 x64 或 x86_64，ARM 设备选 ARM64。

| 平台 | 架构 | 下载 |
| --- | --- | --- |
| macOS | Apple 芯片 | <a data-astral-download="mac-arm64-dmg" href="https://github.com/oines/astral/releases/download/v0.1.13/Astral-0.1.13-mac-arm64.dmg">DMG</a> |
| macOS | Intel | <a data-astral-download="mac-x64-dmg" href="https://github.com/oines/astral/releases/download/v0.1.13/Astral-0.1.13-mac-x64.dmg">DMG</a> |
| Windows | x64 | <a data-astral-download="win-x64-exe" href="https://github.com/oines/astral/releases/download/v0.1.13/Astral-0.1.13-win-x64.exe">EXE 安装包</a> |
| Windows | ARM64 | <a data-astral-download="win-arm64-exe" href="https://github.com/oines/astral/releases/download/v0.1.13/Astral-0.1.13-win-arm64.exe">EXE 安装包</a> |
| Linux | x86_64 | <a data-astral-download="linux-x64-appimage" href="https://github.com/oines/astral/releases/download/v0.1.13/Astral-0.1.13-linux-x86_64.AppImage">AppImage</a> · <a data-astral-download="linux-x64-deb" href="https://github.com/oines/astral/releases/download/v0.1.13/Astral-0.1.13-linux-amd64.deb">DEB</a> |
| Linux | ARM64 | <a data-astral-download="linux-arm64-appimage" href="https://github.com/oines/astral/releases/download/v0.1.13/Astral-0.1.13-linux-arm64.AppImage">AppImage</a> · <a data-astral-download="linux-arm64-deb" href="https://github.com/oines/astral/releases/download/v0.1.13/Astral-0.1.13-linux-arm64.deb">DEB</a> |

## 安装并打开

- **macOS**：打开下载的 DMG，把 Astral 拖进“应用程序”，再从“应用程序”打开。v0.1.13 的 Mac 安装包已签名并通过 Apple 公证，可查看[签名报告](https://github.com/oines/astral/releases/download/v0.1.13/mac-signing-report.json)。
- **Windows**：双击 EXE，按安装向导完成安装。安装包尚未签名，系统可能显示 SmartScreen 提示；核对下载来自本页链接的官方发行版。
- **Linux**：使用 AppImage 时，先在文件属性中允许它作为程序执行，再打开；使用 Debian/Ubuntu 的软件安装器时，可选择 DEB。AppImage 支持应用内更新，DEB 需要安装新的软件包。Linux 安装包尚未签名。

打开后，跟着[完成第一个任务](/zh-cn/docs/start/first-steps/)连接模型并选择电脑。桌面版直接进入配置，无需先创建网页登录账户。

<span id="一键安装到-linux-vps"></span>

## 想在服务器上一直运行

准备一台使用 systemd 的 Linux x86_64 或 ARM64 服务器。通过 SSH 登录服务器，在**服务器的终端**执行：

```sh
curl --fail --silent --show-error --location --proto '=https' --proto-redir '=https' \
  https://github.com/oines/astral/releases/latest/download/install-server.sh | bash
```

按提示设置网页登录的用户名和密码。安装成功后，打开终端显示的访问地址登录；可以关闭 SSH，后台服务会继续运行。

安装要求、远程访问和日常管理的完整步骤在[安装服务器版](/zh-cn/docs/hosting/server/)。如果你只是想在自己的笔记本上试用，使用上面的桌面版即可。

## GitHub 下载失败时

可以使用第三方 [GH-Proxy 下载代理](https://gh-proxy.com/docs/github-accelerator/)。对桌面安装包：

1. 复制上表中安装包的完整链接。
2. 在链接开头加上 `https://gh-proxy.org/`，中间不加空格。
3. 在浏览器打开这个新地址下载。

例如，原链接以 `https://github.com/` 开始，添加后应以 `https://gh-proxy.org/https://github.com/` 开始。代理是第三方服务，可用性取决于当前网络。

安装服务器版时，请使用[完整的备用安装命令](/zh-cn/docs/hosting/server/#国内无法访问-github)，让后续程序下载也使用所选代理。

开发者需要自行编译时，可以阅读[从源码构建](/zh-cn/docs/hosting/build-from-source/)。

<script is:inline type="module" src="/scripts/release-downloads.js"></script>

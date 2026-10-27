---
title: "运行 Server"
description: "一键安装 Astral Server 到 Linux VPS，设置网页登录并配置网络访问。"
---

官方 Server 二进制已包含 Web UI，不需要单独构建或部署前端。

## 一键安装到 Linux VPS

使用 Linux x86_64 或 ARM64，需要 Bash、curl、Python 3 和可用的 systemd 用户会话。以将要运行 Astral 的账户执行：

```sh
curl --fail --silent --show-error --location --proto '=https' --proto-redir '=https' \
  https://github.com/oines/astral/releases/latest/download/install-server.sh | bash
```

默认安装目录为 `${XDG_DATA_HOME:-$HOME/.local/share}/astral-server`。安装器校验发行文件后，注册 `systemd --user` 的 `astral-server.service`，不会安装系统级服务，也不会自动开启用户驻留。需要退出登录后持续运行、重启后自动启动时，请 VPS 管理员用 `loginctl enable-linger USER` 为该账户开启驻留。

以后升级时，以同一账户、相同安装目录重新执行安装命令。需要指定已发布版本时，将结尾的 `bash` 改为 `bash -s -- --tag v0.1.0`。未指定的网络设置会保留。

## 设置网页登录

执行安装器输出的账号初始化命令。默认安装目录对应：

```sh
"${XDG_DATA_HOME:-$HOME/.local/share}/astral-server/current/bin/astral" auth setup \
  --data "${XDG_DATA_HOME:-$HOME/.local/share}/astral-server/data/server" --username admin
```

按提示隐藏输入并确认密码。默认关闭远程直接管理。通过 SSH 访问时，在你自己的电脑上运行，替换 `USER@VPS`：

```sh
ssh -N -L 4545:127.0.0.1:4545 USER@VPS
```

然后在浏览器打开 `http://127.0.0.1:4545` 登录。直接远程访问与反向代理见下方网络设置。

## 手动运行

也可以直接运行与系统匹配的[发行二进制](https://github.com/oines/astral/releases/latest)，或使用[源码构建](/zh-cn/docs/start/get-astral/)。下面假设程序已命名为 `astral` 并放入 PATH，Windows 使用 `.exe`。

```sh
astral serve --data ./data/server --computers-dir ./data/computers
```

在另一终端为**同一个数据目录**设置网页登录：

```sh
astral auth setup --data ./data/server --username admin
```

在 Server 本机打开 `http://127.0.0.1:4545` 登录。手动启动的前台进程通过 Ctrl+C 结束。长期运行时使用绝对路径和服务管理器，重启后沿用同一数据目录。

## 网络访问的三个设置

| 设置 | 实际作用 |
| --- | --- |
| 监听地址 | 决定进程在哪些网络接口和端口接收连接，修改后需重启 |
| 访问地址 | 告诉其他设备使用哪个完整 HTTP/HTTPS 地址，不会创建代理或修改监听端口 |
| 允许远程管理 | 允许远端直接访问网页与管理 API，仍然需要账号登录 |

默认监听 `0.0.0.0:4545`，远程管理默认关闭，电脑连接仍可使用。可在设置的网络访问区域管理访问地址和远程管理开关。

命令行参数优先于环境变量，环境变量优先于保存的设置。被启动参数控制的项目会在界面显示来源。新访问地址用于新生成的接入命令，已接入电脑保留原地址。

## 通过 HTTPS 反向代理访问

Astral 本身提供 HTTP。需要 HTTPS 时，让 Caddy、Nginx 或你已有的代理终止 TLS。由安装器管理的 Server，可重新执行安装命令，将结尾的 `bash` 改为 `bash -s -- --public-url https://astral.example.com`；未指定的设置会保留。手动以前台进程运行、代理与 Server 在同一台机器时：

```sh
astral serve --data /srv/astral/server --computers-dir /srv/astral/computers \
  --public-url https://astral.example.com
```

Caddy 配置示例，域名需替换为你实际控制并正确解析的域名：

```text title="Caddyfile"
astral.example.com {
    reverse_proxy 127.0.0.1:4545
}
```

代理应保留外部 Host、支持 WebSocket，并及时转发 SSE。默认信任本机回环代理的 HTTPS 标记；独立代理主机或代理容器还需用 `--trusted-proxy IP_OR_CIDR` 明确指定可信来源，并为实际远程管理路径开启 `--allow-remote-management`。

本机代理可以转发本地管理入口，因此“允许远程管理”关闭不代表代理地址不可访问；账号登录仍然有效。跨公网传输网页登录应使用 HTTPS。

## 连接电脑时注意地址

目标电脑必须能实际访问选定地址。Astral 不自动提供 NAT 穿透或中继，可使用自己的局域网、VPN 或公网网络。

仅监听回环地址会使其他电脑及 Docker Desktop 容器无法直接连接。Server 放在容器中时，要持久化数据、发布端口并设置可达的外部地址；目前没有官方 Server 容器镜像，电脑执行环境镜像也不是 Server 镜像。

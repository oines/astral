---
title: "运行 Server"
description: "启动 Astral Server，设置网页登录并配置网络访问。"
---

先按[获取 Astral](/zh-cn/docs/start/get-astral/)构建程序。下面示例假设所选二进制已命名为 `astral` 并放入 PATH；也可以替换为 `dist/bin/` 中对应文件的完整路径，Windows 使用 `.exe`。

## 启动并设置账号

在仓库根目录中运行：

```sh
astral serve --data ./data/server --computers-dir ./data/computers
```

源码构建后的 `apps/web/dist/` 会被用作产品界面。脱离仓库运行时，按发行布局放置界面，或通过 `--web-dir` 指向该目录。

在另一终端为**同一个数据目录**设置网页登录：

```sh
astral auth setup --data ./data/server --username admin
```

按提示输入并确认密码，然后在 Server 本机打开 `http://127.0.0.1:4545` 登录。密码通过终端隐藏输入，无需放进命令行参数。

长期运行时使用绝对数据路径，并通过系统服务管理进程。重启时沿用相同目录，避免意外创建一套空数据。

## 网络访问的三个设置

| 设置 | 实际作用 |
| --- | --- |
| 监听地址 | 决定进程在哪些网络接口和端口接收连接，修改后需重启 |
| 访问地址 | 告诉其他设备使用哪个完整 HTTP/HTTPS 地址，不会创建代理或修改监听端口 |
| 允许远程管理 | 允许远端直接访问网页与管理 API，仍然需要账号登录 |

默认监听 `0.0.0.0:4545`，远程管理默认关闭，电脑连接仍可使用。可在设置的网络访问区域管理访问地址和远程管理开关。

命令行参数优先于环境变量，环境变量优先于保存的设置。被启动参数控制的项目会在界面显示来源。新访问地址用于新生成的接入命令，已接入电脑保留原地址。

## 通过 HTTPS 反向代理访问

Astral 本身提供 HTTP。需要 HTTPS 时，让 Caddy、Nginx 或你已有的代理终止 TLS。例如，代理与 Server 在同一台机器时：

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

---
title: "NapCat 网络连接"
description: "NapCat 在 Docker 或另一台电脑上时，确定 Astral 应填写的连接地址。"
---

只有 NapCat 不按[QQ 教程](/zh-cn/docs/chat/qq/#qq-napcat)直接运行在 Astral 同一台电脑上时，才需要查这页。先确定 **Astral Server 在哪里运行**：桌面版使用本机服务；浏览器访问的 Astral 则从服务器发起连接。

Astral 需要访问 NapCat 的 HTTP 和正向 WebSocket 两个服务。当前只允许本机回环地址使用 `http://` / `ws://`；其他地址必须使用 `https://` / `wss://`，即使是局域网 IP 或 Docker 服务名也一样。

| 你的部署情况 | 使用方法 |
| --- | --- |
| NapCat 在 Docker，Astral 直接运行在同一宿主机 | 将两个端口发布到宿主机 `127.0.0.1`，见下面的 Docker 配置 |
| NapCat 在另一台电脑，Astral 直接运行在本机或服务器 | 在运行 Astral 的机器上建立 SSH 转发，再填本机地址 |
| Astral 也在独立容器中，或已有反向代理 | 提供 Astral 容器能访问的 HTTPS 和 WSS 入口 |
| 只想打开远程 NapCat 管理页面 | 单独转发 WebUI 端口，见本页末尾 |

`0.0.0.0` 用于指定服务监听哪些网卡，不能填成 Astral 的目标地址。容器中的 `127.0.0.1` 只指该容器自身。

## NapCat 在 Docker，Astral 在同一宿主机

先用 `docker ps -a` 查找已有 NapCat 容器，保留原来的配置和数据卷。不要为同一 QQ 账号重新部署另一套实例。

在已有 Compose 服务中检查或加入端口发布：

```yaml
services:
  napcat:
    # 保留已有的 image、environment、volumes 等配置。
    ports:
      - "127.0.0.1:6099:6099" # 管理页面
      - "127.0.0.1:3000:3000" # HTTP 服务
      - "127.0.0.1:3001:3001" # 正向 WebSocket 服务
```

这是配置片段，不是完整安装文件。修改后，在原 Compose 文件所在目录使用 `docker compose up -d napcat` 应用端口变更；如果服务名不同，替换命令末尾的名称。保留原来的持久数据卷，重新创建容器会短暂中断连接。

到 NapCat WebUI 启用两个服务，将容器内的 Host 都设为 `0.0.0.0`，端口分别对应 `3000`、`3001`。然后在 Astral 填：

- HTTP：`http://127.0.0.1:3000`
- 正向 WebSocket：`ws://127.0.0.1:3001`

如果现有映射是 `13000:3000`，Astral 应填宿主机端口 `13000`，WebSocket 同理。端口发布与容器内服务启用都要完成；只修改一边仍无法连接。关于发布地址见 [Docker 官方说明](https://docs.docker.com/engine/network/port-publishing/)。

## NapCat 在另一台电脑：SSH 转发

这种方式适用于你能通过 SSH 登录 NapCat 电脑，而且 Astral 直接运行在电脑或服务器上。NapCat 的两个服务可继续仅监听它自己的 `127.0.0.1`。

在 **运行 Astral Server 的机器**上打开终端，执行：

```sh
ssh -N -o ExitOnForwardFailure=yes \
  -L 127.0.0.1:13000:127.0.0.1:3000 \
  -L 127.0.0.1:13001:127.0.0.1:3001 \
  USER@NAPCAT_HOST
```

将 `USER`、`NAPCAT_HOST` 换成 SSH 用户名和 NapCat 电脑地址；最后的 `3000`、`3001` 换成那台电脑实际的 OneBot 端口。保持这个终端和 SSH 连接运行，然后在 Astral 填：

- HTTP：`http://127.0.0.1:13000`
- 正向 WebSocket：`ws://127.0.0.1:13001`

前面的 `13000`、`13001` 是 Astral 机器上的端口，需要未被占用。SSH 断开后 Astral 也会失去 NapCat 连接；恢复转发后，将断线期间需要处理的消息重新发送。

如果 Astral 在容器中，在宿主机建立这个转发不会自动让容器通过自己的 `127.0.0.1` 访问它。使用下一节的 HTTPS/WSS 入口，或在你明确管理的同一网络环境中提供回环转发。

## Astral 在容器中，或使用 HTTPS 反向代理

为两个 OneBot 服务提供 Astral 实际可达的加密入口：

1. 确认代理能访问 NapCat HTTP 和正向 WebSocket 服务。同一个 Docker 网络内，代理的上游可以使用 `napcat:3000`、`napcat:3001` 等实际服务名和端口；NapCat 在容器内监听 `0.0.0.0`。
2. 使用受 Astral 所在系统信任、与域名匹配的 TLS 证书。HTTP 入口转发到 HTTP 服务，WSS 入口转发到正向 WebSocket 服务，并保留 Authorization 头。
3. 开启代理的 WebSocket 升级支持。使用 Nginx 时可参阅[官方 WebSocket 代理配置](https://nginx.org/en/docs/http/websocket.html)。只让 Astral 所在网络访问这些入口，保留 OneBot Token，不公开 WebUI。
4. 在 Astral 填真实的入口，例如 `https://qq-api.example.com` 和 `wss://qq-events.example.com`。两个入口必须对应同一 QQ 账号，不能指向只处理请求的 `/api` WebSocket 端点。

不能直接填写 `http://napcat:3000`、`ws://192.168.1.20:3001` 等非回环明文地址。VPN 解决网络可达性，但不会改变 Astral 对地址协议的要求。

如果 NapCat 原生运行在宿主机、代理在容器中，代理还需要能访问宿主机：`host.docker.internal` 只在容器运行环境提供它时可用；Linux 环境可能需要 host-gateway 映射。宿主机只监听 `127.0.0.1` 的服务通常不能被桥接网络容器直接访问。

## 只打开远程管理页面

在自己的电脑建立 WebUI 转发：

```sh
ssh -N -o ExitOnForwardFailure=yes \
  -L 127.0.0.1:16099:127.0.0.1:6099 USER@NAPCAT_HOST
```

替换 SSH 账号和实际 WebUI 端口，然后打开 `http://127.0.0.1:16099/webui/`，使用 NapCat 日志中的管理登录凭据。保持终端运行。

这个转发只用于配置 NapCat。Astral 收发 QQ 消息仍需要前面准备的 HTTP 和 WebSocket 连接。完成网络设置后，回到 [QQ 教程](/zh-cn/docs/chat/qq/#qq-napcat)启用服务、连接账号并测试。

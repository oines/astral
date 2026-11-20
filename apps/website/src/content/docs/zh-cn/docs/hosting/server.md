---
title: "运行 Server"
description: "一条命令在 Linux 服务器部署后台服务、设置管理员账号，并通过浏览器或 HTTPS 随时访问。"
---

把 Astral 安装到一台长期开机的 Linux 服务器上，关闭笔记本后也能从任何浏览器打开，保持聊天连接与计划任务全天在线。

## 安装前准备

- 一台支持 `systemd` 的 Linux（x86_64 或 ARM64）服务器，具备 `bash`、`curl`、`python3`、`sha256sum`、`flock`、`systemctl`、`loginctl`。
- 用你打算运行 Astral 的系统账号登录 SSH（推荐普通账号；以 `root` 运行会让 Bot 在本机拥有 root 权限）。

## 1. 运行安装命令

在**服务器终端**执行（无法直连 GitHub 请看下方[备用代理安装命令](#国内无法访问-github)）：

```sh
curl --fail --silent --show-error --location --proto '=https' --proto-redir '=https' \
  https://github.com/oines/astral/releases/latest/download/install-server.sh | bash
```

## 2. 设置网页管理员账号

按提示输入 Astral 管理员用户名与密码（独立于服务器 SSH 账号；密码输入不显示字符）。看到 `Astral Server … is ready` 即表示后台服务已启动并设为开机自启，可以放心关闭 SSH（忘记密码可[在服务器重置](/zh-cn/docs/hosting/security/#无法登录时恢复)）。

## 3. 打开网页完成首次配置

在浏览器访问 `http://服务器IP:4545`（或安装器输出的可用地址）登录，然后按[完成第一个任务](/zh-cn/docs/start/first-steps/)连上模型并跑通首条任务。

:::note[这里选中的“本机”是服务器]
首次配置的本机电脑指这台 Linux 服务器。需要处理你日常笔记本的文件时，另外[连接你的笔记本](/zh-cn/docs/computers/connect/)即可。网页打不开请查[排查问题](/zh-cn/docs/hosting/troubleshooting/#网页打不开或无法登录)。
:::

## 管理后台服务

以**当初安装 Astral 的同一系统账号**在服务器执行（停止/重启会中断运行中的任务）：

| 目的 | 命令 |
| --- | --- |
| 查看状态 / 日志 | `systemctl --user status astral-server.service` / `journalctl --user -u astral-server.service` |
| 启动 / 停止 / 重启 | `systemctl --user start\|stop\|restart astral-server.service` |
| 开关开机自启 | `systemctl --user enable\|disable astral-server.service` |

若提示 `Failed to connect to bus`，见[用户服务管理器连接失败](/zh-cn/docs/hosting/troubleshooting/#用户服务管理器连接失败)。

## 升级

先等当前任务完成或手动停止，再到 **设置 → 关于与诊断** 点**更新并重启**；也可以用原账号重跑上面的安装命令（自定义目录需保留原 `--root`）。账户、会话、电脑身份与配置全部保留，备份建议见[升级与备份](/zh-cn/docs/hosting/maintenance/)。

## 卸载后台服务

停止运行中的任务后，以原账号执行（自定义目录在最后一行追加 `--root /原绝对路径`）：

```sh
curl --fail --silent --show-error --location --proto '=https' --proto-redir '=https' \
  https://github.com/oines/astral/releases/latest/download/install-server.sh -o install-server.sh
bash install-server.sh --uninstall
```

:::note[卸载会保留你的数据]
脚本只停服并清理程序与命令入口；**账户、会话、工作文件、配置与备份均保留**，方便重装恢复。也不会删 Docker 容器或关闭账号级 linger。需手动清理数据前，先看[备份范围](/zh-cn/docs/hosting/maintenance/#备份哪些内容)。
:::

## 通过 HTTPS 反向代理访问

公网长期使用建议配域名 + HTTPS（Astral 默认在 `4545` 端口提供 HTTP；Passkey 登录也必须使用真实 HTTPS）。以 [Caddy](https://caddyserver.com/docs/install) 为例，先解析域名并开放 `80`/`443` 端口，再向 `/etc/caddy/Caddyfile` 加入：

```text title="Caddyfile"
astral.example.com {
    reverse_proxy 127.0.0.1:4545
}
```

重载配置（参考 [Caddy 服务说明](https://caddyserver.com/docs/running#using-the-service)）：

```sh
sudo systemctl reload caddy
```

新安装无需在 Astral 登记域名；若开启了主机名限制，先[放行该域名](/zh-cn/docs/hosting/security/#主机名限制)。如果反代在另一台机器上，另需配置[可信代理](/zh-cn/docs/hosting/server-options/#代理位于另一台机器)。

## 国内无法访问 GitHub

可通过第三方 [GH-Proxy](https://gh-proxy.com/docs/github-accelerator/) 代理安装脚本与后续发行文件下载（仍会执行完整校验；后续停用代理可重跑安装器加 `--github-proxy ''`）：

```sh
curl --fail --silent --show-error --location --proto '=https' --proto-redir '=https' \
  https://gh-proxy.org/https://github.com/oines/astral/releases/latest/download/install-server.sh \
  | bash -s -- --github-proxy https://gh-proxy.org/
```

需要无人值守安装、改端口/目录或前台运行，见[定制服务器部署](/zh-cn/docs/hosting/server-options/)；需要容器执行环境，见[在容器中工作](/zh-cn/docs/computers/containers/)。

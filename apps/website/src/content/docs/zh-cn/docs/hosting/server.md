---
title: "在 Linux 上安装 Server"
description: "安装后台服务、设置登录账户，然后从浏览器完成第一个任务。"
---

这篇教程适合把 Astral 放在长期运行的 Linux 服务器上。完成后，你可以从浏览器使用 Astral，关闭安装终端后服务会继续运行。

## 安装前准备

你需要一台使用 systemd 的 Linux 服务器，CPU 为 x86_64 或 ARM64，并能通过终端或 SSH 登录。系统需已提供 Bash、curl、Python 3、sha256sum、flock、systemctl 和 loginctl。

以准备运行 Astral 的系统账号登录。安装器需要时会请求 sudo，用于准备后台运行条件或安装命令入口。若使用 root 账号，Bot 在服务器本机执行命令也会拥有 root 权限；可以使用普通账号运行 Astral。

## 1. 运行安装命令

在**服务器的终端**中复制并运行：

```sh
curl --fail --silent --show-error --location --proto '=https' --proto-redir '=https' \
  https://github.com/oines/astral/releases/latest/download/install-server.sh | bash
```

安装器会下载程序、校验文件并启动后台服务。如果下载失败，使用下方的[备用下载命令](#国内无法访问-github)。如果提示缺少系统组件或权限，先按错误提示补齐，再重新运行。

## 2. 设置登录账户

按终端提示输入 Astral 的管理员用户名、密码，再输入一次密码确认。输入密码时终端不显示字符。

这是之后打开 Astral 网页使用的账户，与服务器的 SSH 账号分别设置。妥善保存它；忘记密码时，可以在服务器上[重置网页登录](/zh-cn/docs/hosting/security/#无法登录时恢复)。

终端显示 `Astral Server … is ready` 后，安装完成。现在可以关闭 SSH：后台服务会继续运行，服务器重启后也会自动启动。安装器为此按需启用账号的用户驻留（linger）；这一设置让该账号的用户服务在退出登录后继续运行。

## 3. 打开网页，完成第一个任务

在自己电脑的浏览器打开 `http://服务器IP:4545`，把“服务器IP”换成实际 IP；也可以使用安装器显示的、从你所在网络可达的地址。输入上一步设置的用户名和密码登录。

看到首次配置页面后，按[完成第一个任务](/zh-cn/docs/start/first-steps/)连接模型、选择服务器本机并发送一条任务。这里选中的电脑是服务器：要处理笔记本上的文件，需要另外[连接笔记本](/zh-cn/docs/computers/connect/)。

网页打不开时，按[网页打不开](/zh-cn/docs/hosting/troubleshooting/#网页打不开或无法登录)检查服务、地址和防火墙。通过公网长期使用时，可为下面的 [HTTPS 入口](#通过-https-反向代理访问)配置自己的域名。

## 管理后台服务

下面的命令都在服务器上、以**安装 Astral 的同一系统账号**执行。停止和重启会中断正在进行的任务。

| 需要做什么 | 命令 |
| --- | --- |
| 查看服务状态 | `systemctl --user status astral-server.service` |
| 查看日志 | `journalctl --user -u astral-server.service` |
| 立即停止 | `systemctl --user stop astral-server.service` |
| 再次启动 | `systemctl --user start astral-server.service` |
| 重启服务 | `systemctl --user restart astral-server.service` |
| 取消开机启动 | `systemctl --user disable astral-server.service` |
| 恢复开机启动 | `systemctl --user enable astral-server.service` |

`stop` 只停止当前运行，`disable` 只取消自动启动。需要停用服务时，分别执行这两个动作。若命令提示 `Failed to connect to bus`，见[用户服务管理器连接失败](/zh-cn/docs/hosting/troubleshooting/#用户服务管理器连接失败)。

## 升级

先完成或停止当前任务，再到 **设置 → 关于与诊断** 检查更新；官方安装支持时，点击**更新并重启**。

也可以在服务器上用原账号再次运行上面的安装命令。使用自定义安装目录时，继续传入原来的 `--root`。升级保留账户、会话、电脑身份和配置。升级前备份及失败处理见[升级与备份](/zh-cn/docs/hosting/maintenance/)。

## 卸载后台服务

停止正在进行的任务后，在服务器上以原账号执行：

```sh
curl --fail --silent --show-error --location --proto '=https' --proto-redir '=https' \
  https://github.com/oines/astral/releases/latest/download/install-server.sh \
  -o install-server.sh
bash install-server.sh --uninstall
```

自定义安装目录时，将最后一行改为 `bash install-server.sh --uninstall --root /原来的绝对安装路径`。

脚本会停止服务、取消开机启动，并清理可安全删除的程序和命令入口。**账户、会话、电脑文件、配置和备份会保留**，同一目录重装后可以继续使用。为重装核验和恢复数据，还会保留一套完整发行文件；仍被其他电脑安装共用的命令也会保留。

卸载不会删除 Docker 容器、远程电脑文件或关闭账号级 linger。如果脚本发现服务定义被改过或安装归属不符，会停止清理并指出位置。需要另外删除数据时，先确认[备份范围](/zh-cn/docs/hosting/maintenance/#备份哪些内容)。

## 通过 HTTPS 反向代理访问

有自己的域名、希望用 `https://astral.example.com` 访问时，可以让 Caddy、Nginx 或已有代理为 Astral 提供 HTTPS。Astral 本身在默认的 `4545` 端口提供 HTTP。

将域名解析到服务器，并允许外部访问 `80`、`443` 端口。还没有代理时，先按 [Caddy 官方安装指南](https://caddyserver.com/docs/install)安装并启动 Caddy。

使用官方 Caddy 服务时，在 `/etc/caddy/Caddyfile` 中加入下面的站点，保留已有站点配置，并把示例域名换成自己的域名：

```text title="Caddyfile"
astral.example.com {
    reverse_proxy 127.0.0.1:4545
}
```

保存后按 [Caddy 服务说明](https://caddyserver.com/docs/running#using-the-service)重新加载配置：

```sh
sudo systemctl reload caddy
```

在浏览器打开自己的 HTTPS 地址并登录。新安装无需在 Astral 登记域名；如果已经开启“限制访问主机名”，先按[主机名设置](/zh-cn/docs/hosting/security/#主机名限制)允许这个域名。

代理与 Astral 不在同一台机器时，还需要[配置可信代理](/zh-cn/docs/hosting/server-options/#代理位于另一台机器)。Passkey 必须通过实际 HTTPS 网页配置和使用。

## 国内无法访问 GitHub

可以显式使用第三方 [GH-Proxy](https://gh-proxy.com/docs/github-accelerator/)下载。下面的命令同时代理安装脚本和后续程序下载：

```sh
curl --fail --silent --show-error --location --proto '=https' --proto-redir '=https' \
  https://gh-proxy.org/https://github.com/oines/astral/releases/latest/download/install-server.sh \
  | bash -s -- --github-proxy https://gh-proxy.org/
```

下载仍会校验文件。这个代理只用于 GitHub 下载，服务可用性取决于第三方和当前网络。以后不再使用时，重跑安装器并传入 `--github-proxy ''`；未指定时保留原选择。

## 需要定制部署？

[Server 部署选项](/zh-cn/docs/hosting/server-options/)介绍无人值守安装、自定义目录和端口、前台运行，以及限制访问范围。需要容器电脑时，按[使用容器](/zh-cn/docs/computers/containers/)准备 Docker；安装 Astral 本身不会安装 Docker 或修改 Docker 用户组。

---
title: "Server 部署选项"
description: "无人值守安装、自定义目录和端口、前台运行及网络访问配置。"
---

普通 Linux 后台安装按[安装 Server](/zh-cn/docs/hosting/server/)操作即可。下面只在你需要自动化安装、修改默认值或自行管理进程时使用。

## 无人值守安装

自动化脚本无法交互输入密码时，先准备一个只有安装账号能读取的密码文件。文件中只放管理员密码，不要把密码直接写进公开脚本或命令参数。

下载安装器，再执行安装。将用户名和密码文件路径替换为自己的值：

```sh
curl --fail --silent --show-error --location --proto '=https' --proto-redir '=https' \
  https://github.com/oines/astral/releases/latest/download/install-server.sh \
  -o install-server.sh
ASTRAL_ADMIN_USERNAME=admin \
ASTRAL_ADMIN_PASSWORD_FILE=/secure/path/astral-admin-password \
  bash install-server.sh
```

提前为系统准备步骤和命令安装提供所需权限。管理员网页登录密码不会代替 sudo 授权。已有账户升级时，这些初始凭据不会重置原密码。

## 自定义安装目录、端口或版本

先按上方命令下载 `install-server.sh`，再根据需要选择参数：

| 需要改变什么 | 参数示例 |
| --- | --- |
| 安装和数据根目录 | `--root /srv/my-astral` |
| HTTP 监听端口 | `--listen 0.0.0.0:4546` |
| 只监听本机 | `--listen 127.0.0.1:4545` |
| 指定已发布版本 | 使用该发行版附带的安装器，传入 `--tag TAG`，把 `TAG` 换成发行页上的版本标签 |

例如，首次安装到自定义目录并使用 `4546` 端口：

```sh
bash install-server.sh --root /srv/my-astral --listen 0.0.0.0:4546
```

安装账号需要有权写入该目录。之后升级和卸载都要继续使用相同的 `--root`。同一系统账号的官方安装器只管理一个 `astral-server.service`，不要用不同目录重复安装多个同名服务。

默认根目录是 `${XDG_DATA_HOME:-$HOME/.local/share}/astral-server`。其中 Server 数据在 `data/server/`，本机电脑文件在 `data/computers/`。这与手动 `serve` 的默认目录不同，备份时按[实际安装方式确认路径](/zh-cn/docs/hosting/maintenance/#找到数据目录)。

安装器会保留已有网络设置；只有显式传入的选项才修改相应值。需要安装较旧版本时，使用那个版本附带的安装器，避免新安装器要求旧程序没有的功能。

## 手动前台运行

这适合临时调试或自行编写服务。先取得[发行程序](/zh-cn/docs/start/get-astral/)或[从源码构建](/zh-cn/docs/hosting/build-from-source/)，将当前系统的程序命名为 `astral` 并放入 PATH；Windows 使用 `astral.exe`。

选定两个不同的绝对目录，分别保存 Server 数据和本机电脑数据。替换下面的示例路径后，在终端执行：

```sh
astral serve \
  --data /absolute/path/to/astral-data/server \
  --computers-dir /absolute/path/to/astral-data/computers
```

保持这个终端运行，在另一终端初始化同一个 Server 的网页登录：

```sh
astral auth setup --data /absolute/path/to/astral-data/server --username admin
```

打开启动日志显示的地址，使用刚设置的账户登录。Ctrl+C 会结束前台服务，断开 SSH 也可能使它退出。长期运行时，需要自行配置进程管理、日志和开机启动，始终使用相同的数据路径。

## 限制网页访问范围

独立 Server 默认接受远程网页登录。你可以通过防火墙或 VPN 控制谁能连到它，也可以只监听 `127.0.0.1`，再使用 SSH 转发或反向代理访问。

官方安装器可以保存“关闭直接远程管理”的选择：

```sh
bash install-server.sh --allow-remote-management=false
```

恢复时使用 `--allow-remote-management=true`。自定义根目录仍要带上原 `--root`。这个开关不停止电脑连接，本机反向代理仍可以转发网页入口，所有入口都需要登录。

手动运行对应 `astral serve --allow-remote-management=false`，也可使用 `ASTRAL_ALLOW_REMOTE_MANAGEMENT=false`。独立 Server 的网页设置没有这个开关；桌面版可以在安全与访问设置中控制远程管理。

只想限制允许使用的域名或 IP 时，使用[主机名限制](/zh-cn/docs/hosting/security/#主机名限制)。

## 代理位于另一台机器

[HTTPS 示例](/zh-cn/docs/hosting/server/#通过-https-反向代理访问)假设代理与 Astral 在同一台机器。代理在另一台机器或容器中时，Astral 需要知道哪些来源可以报告外部的 HTTPS 连接。

为 Server 启动配置添加 `--trusted-proxy IP_OR_CIDR`，或用 `ASTRAL_TRUSTED_PROXIES` 指定实际代理来源。代理应保留原 Host、支持 WebSocket，并及时转发 SSE。不要把任意来源都设为可信代理。

修改官方服务定义后，该安装会改为手动更新；网页更新只适用于安装器管理的原始服务。需要重新使用官方更新流程时，先确认自定义启动项如何保留，再重新运行安装器。

## 把 Server 放进容器

目前没有官方 Server 容器镜像或 Compose 安装方案。文档中的 Ubuntu 环境镜像用于“容器电脑”，不用于部署 Server。

自行制作 Server 容器时，需要持久化完整数据与身份、发布实际端口、设置管理员账户，并为其他电脑提供可达地址。Server 里的“本机”指该容器内部；希望只使用其他电脑时，可传入 `--no-local`。这类部署由自己的容器配置负责更新和恢复。

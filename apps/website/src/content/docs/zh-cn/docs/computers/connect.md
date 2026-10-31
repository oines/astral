---
title: "连接电脑"
description: "把服务所在电脑或远程机器接入 Astral，并授予 Bot 使用权限。"
---

在 **设置 → 电脑** 管理机器连接，再到具体 Bot 的 **电脑权限**决定谁可以使用它。连接和授权分别管理。

## 使用服务所在电脑

选择“添加电脑”中的服务所在机器入口。它使用运行 Astral Server 的机器及其操作系统账号；在 Desktop 中通常就是安装 Desktop 的电脑。

电脑上线后，可以在详情中查看类型、运行环境、版本和数据目录。

## 连接其他机器

1. 打开 **设置 → 电脑 → 添加电脑 → 连接其他机器**。
2. 选择目标机器能够访问的服务器地址与目标操作系统。
3. 生成接入命令，在目标电脑的 Shell 或 PowerShell 中运行完整命令。
4. 命令会检查连接并安装后台服务。回到 Astral，等待该电脑显示在线。

命令包含临时配对凭据，请只在要接入的电脑上使用。命令过期时重新生成，不要反复运行旧命令。安装器需要访问对应版本的官方发行资源。从源码开发、尚无匹配发行包时，需要自行准备目标电脑的程序。

远程电脑主动连接 Server。`127.0.0.1` 只适用于同一台机器；局域网、VPN 或公网地址必须从目标电脑实际可达。接入地址可以是 HTTP 或 HTTPS，电脑通道自身使用加密连接和服务器身份校验。浏览器登录的网络要求见[运行 Server](/zh-cn/docs/hosting/server/)。

## 更新已连接的 Daemon

在电脑详情的 **Daemon 维护** 中点击 **更新 Daemon**，确认正在运行的任务会中断。Astral 检查最新官方版本、下载校验并重启 Daemon，确认新版本已重新连接。电脑身份、凭据和工作文件保留。如果 Daemon 比 Server 新，Astral 会建议同时更新 Desktop 或 Server，但不会因此阻止 Daemon 更新。

服务所在电脑的本机 Daemon 随 Desktop 或 Server 更新。电脑离线、尚不支持管理更新或更新失败时，使用同一区域的 Shell 或 PowerShell 更新命令：裸机命令在目标电脑运行，容器命令在容器所在电脑运行。裸机更新始终使用原操作系统用户的权限。

## 授予 Bot 使用权限

进入 **设置 → Bot → 目标 Bot → 电脑权限**，允许使用刚接入的电脑。按需要设置新会话的默认电脑。

如果会话里找不到已在线的电脑，先检查该 Bot 是否已获准使用它。

## 后台服务与断开

远程接入以当前用户安装后台服务：macOS 使用用户 LaunchAgent，Linux 使用用户 systemd，Windows 使用当前用户的计划任务。能访问哪些文件，取决于运行服务的账号权限。

Linux 裸机后台接入会在配对前检查用户服务是否可用。使用 root（UID 0）时，Astral 会显示准备动作，为 root 启用 linger，使用户服务无需登录会话也能运行，并启动 root 的用户管理器。如果用户管理器无法就绪，接入会在配对前停止。普通账户不会自动提权或启用 linger；前台运行和容器接入不执行这项准备。

要以 root 接入，请直接登录 root，或先运行 `sudo -i`，再粘贴完整接入命令，避免保留其他账户的 `HOME` 或 XDG 会话环境。Agent 执行的命令将拥有 root 权限。linger 作用于整个账户，因此卸载某个 Astral Daemon 不会自动撤销 linger 或停止整个用户管理器。

## 断开与停用电脑

先停止相关会话，并检查仍在运行的任务。取消 Bot 的电脑授权会阻止它继续发起操作；要断开所有 Bot 与这台电脑的连接，在电脑详情中选择**撤销连接**。

远程裸机服务可以在目标电脑上停止或卸载。使用当初接入的同一系统账号，把 `--root` 替换为电脑详情中的数据目录；下面假设 `astral` 已在 PATH 中，Windows 使用 `astral.exe`：

```sh
astral computer service stop --root /path/to/computer
astral computer service status --root /path/to/computer
```

以后需要继续使用时，运行 `astral computer service start --root /path/to/computer`。长期停用可卸载后台服务：

```sh
astral computer service uninstall --root /path/to/computer
```

卸载会停止服务并移除开机启动配置，电脑数据和工作文件保留。容器电脑在宿主机用容器管理器停止对应容器；删除容器或数据卷前先备份工作文件。Desktop 管理的本机服务随应用运行，按桌面后台运行设置停用。

更新和备份步骤见[升级与备份](/zh-cn/docs/hosting/maintenance/)。

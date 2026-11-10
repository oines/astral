---
title: "使用容器"
description: "为 Bot 准备独立的 Linux 工作环境，保留日常文件和已安装工具。"
---

容器适合给 Bot 一套单独的工作环境：它在 Astral 里显示为另一台电脑，有自己的文件和软件。你可以让公开群聊使用专用 Bot，并只授权这个容器，避免直接把日常工作电脑交给群聊使用。

如果只是希望 Bot 处理电脑上已有的文件，可以直接[连接那台电脑](/zh-cn/docs/computers/connect/)。宿主机文件不会自动出现在容器里，需要上传、复制或明确配置挂载；“宿主机”就是实际运行容器的电脑。容器也需要配合权限安排，具体见[公开群聊的环境建议](/zh-cn/docs/start/safe-use/#接入群聊给群安排专用-bot)。

## 创建容器电脑

如果已经安装并启动 Docker Desktop、OrbStack 等兼容环境，可以直接打开**设置 → 电脑 → 添加电脑**：

- 在 Astral 所在机器创建，选择**使用本机容器**。这里的“本机”指 Server 主机，包括 Desktop 自带的 Server，不是打开网页的设备。
- 在另一台机器创建，选择**连接其他机器**并启用**使用容器**，到那台机器运行生成的命令。

如果容器只需要执行命令和处理文件，可以关闭**启用 Astral API**。本机容器默认开启，远程容器默认关闭。具体影响见[控制电脑能否调用 Astral API](/zh-cn/docs/computers/connect/#控制电脑能否调用-astral-api)。

Astral 会下载准备好的 Ubuntu 环境并启动容器。首次下载可能需要一些时间，界面或目标终端会显示进度和错误；以后创建时可以复用已下载的镜像。

**使用本机容器**通过容器运行环境提供的宿主机入口连接 Server，不绕行公开网页地址或反向代理。Astral 会在实际容器环境中测试这条本地连接，并核对 Server 身份；本地不可达时会明确报错，不回退到公网地址。这适用于新建本机容器，已有容器的连接不会被自动改写。在另一台机器运行的接入命令仍使用为那台机器选择的 Server 地址。

电脑上线后，到 Bot 的**电脑权限**中授权，再在会话里选择它。可以发一个小任务核对环境：

> 在当前电脑创建“容器试用.md”，写入今天的日期，并给我文件链接。告诉我保存目录，不要修改其他文件。

打开文件确认结果，然后按[文件与结果](/zh-cn/docs/usage/files/)提供自己的资料。容器环境基于 Ubuntu 24.04，已包含常用开发、数据、文档、媒体、OCR 和浏览器工具，以及中文和 emoji 字体；Bot 可以继续安装任务需要的软件。

## 准备运行环境

如果还没有容器运行环境，先在**容器所在电脑**上安装并启动适合系统的工具：

| 容器所在电脑 | 可选运行环境 |
| --- | --- |
| macOS | Docker Desktop、OrbStack、Colima，或使用 Docker 兼容后端的 Rancher Desktop |
| Linux | Docker Engine（含 Rootless Docker）或 Docker Desktop |
| Windows | 使用 Linux 容器模式的 Docker Desktop |

Astral 使用本地 Docker 命令访问运行环境。运行 Astral 或接入命令的系统账号需要有相应权限，并选择本地 Docker context，也就是本地容器引擎的连接配置。**使用本机容器**不通过远程 Docker context 创建容器。

Windows 上直接使用 Astral 和 PowerShell 接入命令，不需要进入 WSL；容器工具负责它自己的 Linux 环境。

在“添加电脑”中，Astral 会检查 Server 主机的 Docker。处理环境问题后点击**重新检测**；检测通过即可创建，检查本身不会下载镜像或创建容器。

## 容器创建不了

| 提示或现象 | 下一步 |
| --- | --- |
| 未安装 Docker 命令 | 在容器所在机器安装运行环境，确认运行 Astral 的账号能找到 Docker 命令。 |
| 引擎不可达 | 启动 Docker Desktop 或对应容器服务，检查当前是否选中了本地引擎。 |
| 权限不足 | 按提示辨认实际运行 Server 的系统账号。Linux Server 参照下方配置。 |
| 下载镜像或程序失败 | 检查该机器能否访问官方镜像仓库和发行下载地址。 |
| 容器无法连接本机 Server | 检查 Server 监听地址、本机防火墙，以及容器运行环境是否能访问宿主机上的 Server 端口；公开网页地址不能代替这条本地连接。 |
| 浏览器所在电脑有 Docker，入口仍不可用 | 检查的是 Server 主机。要在浏览器这台电脑创建，使用“连接其他机器”的容器命令。 |

其他检测错误可以展开**错误详情**查看 Docker 的具体原因。暂时不能用容器时，仍可连接普通电脑。

### Linux Server 的 Docker 权限

本节适用于在 Linux 上自行运行 Server、Docker 已安装但 Astral 无法使用它的情况。Astral 使用已有 Docker，不负责安装引擎、启动系统服务或把账号加入用户组。尚未安装时，先按 [Docker 官方安装文档](https://docs.docker.com/engine/install/)准备。

用**实际运行 Astral Server 的系统账号**登录终端，检查：

```sh
id -un
id -nG
docker context show
docker info
```

关键结果是 `docker info` 不加 sudo 也能成功。网页登录用户名和这个系统账号是两回事；在另一个管理员账号中测试成功，也不能说明 Server 有权限。

若只有 `sudo docker info` 成功，可以由管理员选择授予 docker 组权限，或使用下方 Rootless Docker。**docker 组相当于授予宿主机 root 等级的权限。** 接受这个范围时，将 `SERVER_USER` 替换为 Server 的实际系统账号：

```sh
sudo usermod -aG docker SERVER_USER
```

按 [Docker 官方说明](https://docs.docker.com/engine/install/linux-postinstall/)重新登录以应用组变更。已经运行的后台进程仍可能保留旧权限：`newgrp docker` 只影响新的 Shell，单独重启 Astral 也可能继续继承旧权限。

如果该账号的 systemd 用户管理器早已运行，让管理员在维护时间重启其 `user@UID.service`，或重启宿主机。`UID` 是 `id -u` 得到的数字。此操作会中断该账号的其他服务和任务，安排好之后再执行。重新检查 `docker info`、启动 Astral，并在网页点击**重新检测**。

### Rootless Docker

不希望授予 docker 组权限时，可按 [Rootless Docker 指南](https://docs.docker.com/engine/security/rootless/)在 Server 的同一个系统账号下安装，并先满足指南列出的系统条件。完成后选择本地引擎、启动用户服务：

```sh
docker context use rootless
systemctl --user enable --now docker.service
docker info
```

如果你的 context 使用其他名字，替换 `rootless`。确认 `docker info` 的安全选项包含 `rootless`，并始终使用 Server 账号检查；配置和服务属于这个账号。

需要退出 SSH 后继续运行、重启后启动时，该账号还需启用 linger。官方 Astral Server 安装器会准备这一项；自定义部署可让管理员执行 `loginctl enable-linger SERVER_USER`。后台 Server 应与终端使用同一 Docker context；如果配置了 `DOCKER_HOST` 或 `DOCKER_CONTEXT`，也要传给 Server 服务。

## 环境与持久化

日常使用的容器会保留工作文件和已安装的软件。重要文件仍应另行备份，特别是在准备删除容器或数据卷之前。

**更新 Daemon**只更新容器里的 Astral 程序，并重启服务，正在运行的任务会中断。原容器、文件和已安装软件保留，Ubuntu 基础环境不会随之升级。备用更新命令需要在 Docker 宿主机运行，会重启原容器。

更新 Desktop 或 Server 也不会重建已有容器。应用版本指定的环境镜像用于新建容器；需要新的基础环境时，另建容器并妥善迁移工作文件。

撤销 Bot 权限、断开电脑或删除 Astral 中的登记，都会保留宿主机上的容器和数据卷。彻底移除时，在宿主机的容器管理器操作，先确认文件已有备份。

---
title: "在容器中工作"
description: "用 Docker 容器为 Bot 提供独立的 Linux 环境，既能持久化文件与软件，又隔离日常电脑。"
---

**容器电脑**在 Astral 里就像一台独立的 Linux 电脑：它有自己的文件系统和预装工具，和你的日常电脑隔开。特别适合配给**公开群聊的专用 Bot**（详见[权限与隐私](/zh-cn/docs/start/safe-use/#接入群聊给群安排专用-bot)）。

如果只是想直接处理电脑里现成的文件，[连接那台电脑](/zh-cn/docs/computers/connect/)更直接；宿主机文件不会自动出现在容器里，需要上传、复制或显式挂载。

## 1. 准备容器运行环境

在**准备运行容器的宿主机**上安装并启动 Docker 兼容环境：

| 宿主机系统 | 推荐环境 |
| --- | --- |
| **macOS** | Docker Desktop、OrbStack、Colima，或 Docker 后端的 Rancher Desktop |
| **Linux** | Docker Engine（含 Rootless Docker）或 Docker Desktop |
| **Windows** | Docker Desktop（Linux 容器模式；直接用 Astral 和 PowerShell，无需进 WSL） |

确保运行 Astral 的系统账号有权调用本地 `docker` 命令并选中了本地 Docker context（“使用本机容器”不支持远程 Docker context）。

## 2. 创建并验证容器电脑

打开 **设置 → 电脑 → 添加电脑**（界面会自动检测 Server 主机的 Docker，修好环境后点**重新检测**）：

- **在 Server 本机创建**：选**使用本机容器**（注意“本机”指 Astral Server 所在机器，不一定是打开浏览器的设备）。
- **在其他机器创建**：选**连接其他机器**并勾选**使用容器**，把命令复制到目标宿主机终端运行。
- **Astral API 开关**：如果容器只需要跑命令、改文件，关闭[**启用 Astral API**](/zh-cn/docs/computers/connect/#控制电脑能否调用-astral-api)（本机容器默认开，远程容器默认关）。

首次创建会拉取基于 Ubuntu 24.04 的镜像（已预装常用开发、数据、文档、媒体、OCR、浏览器工具与中文字体/emoji，后续创建直接复用）。容器上线后到 Bot 的**电脑权限**授权，发一条小任务验收：

> 在当前电脑创建“容器试用.md”，写入今天的日期并给我文件链接，告诉我保存目录。

:::note[容器的持久化与更新]
日常使用中，容器会保留工作文件和 Bot 安装的软件。**更新 Daemon**（或升级 Desktop / Server）不会重建已有容器、也不会覆盖其中的文件或升级 Ubuntu 基础系统；但删除容器或数据卷前请务必另行备份。撤销权限或删除 Astral 登记不会自动删掉宿主机上的容器，需在宿主机的容器管理器自行清理。
:::

## 创建失败排查

| 提示或现象 | 怎么办 |
| --- | --- |
| 未安装 Docker 命令 | 在宿主机安装 Docker 环境，确认 Astral 运行账号能找到 `docker`。 |
| 引擎不可达 | 启动 Docker Desktop / Docker 服务，确认选中本地引擎。 |
| 权限不足 | 按报错确认 Server 实际使用的系统账号，Linux 见下方权限说明。 |
| 下载镜像或程序失败 | 检查宿主机能否访问镜像仓库和 GitHub 发行文件。 |
| 容器连不上本机 Server | 本机容器通过本地宿主机路由直连 Server（不绕行公网反代），检查 Server 监听地址、本机防火墙与容器网络互通。 |
| 浏览器本机有 Docker 但入口不可用 | 页面检测的是 Server 主机；要在浏览器所在电脑创建，请选“连接其他机器”并勾选使用容器。 |

<details class="astral-doc-details">
<summary>Linux Server 配置 Docker 权限与 Rootless Docker 步骤</summary>

若 Linux Server 已按 [Docker 官方文档](https://docs.docker.com/engine/install/)装好 Docker，但 Astral 报权限不足，用**实际运行 Astral Server 的系统账号**执行以下检查（关键是 `docker info` 不加 `sudo` 能成功）：

```sh
id -un && id -nG && docker context show && docker info
```

**方式 A：加入 `docker` 组**（注意：`docker` 组等同于宿主机 root 级权限）：

```sh
sudo usermod -aG docker SERVER_USER
```

组变更生效需重新登录；如果该账号的 systemd 用户管理器已在运行，需在维护窗口重启 `user@UID.service`（`UID` 由 `id -u` 查看）或重启宿主机，再重测 `docker info` 并回到页面点**重新检测**。

**方式 B：使用 Rootless Docker（推荐更小权限）**：在 Server 同一系统账号下按 [Rootless Docker 指南](https://docs.docker.com/engine/security/rootless/)安装后执行：

```sh
docker context use rootless
systemctl --user enable --now docker.service
docker info
```

确认 `docker info` 安全选项含 `rootless` 且该账号已启用 linger（`loginctl enable-linger SERVER_USER`）。若设置了 `DOCKER_HOST` 或 `DOCKER_CONTEXT`，需同步传给后台 Server 服务。

</details>

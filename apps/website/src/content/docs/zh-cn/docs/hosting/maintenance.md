---
title: "升级与备份"
description: "升级 Astral，备份账户和工作资料，并在需要时恢复。"
---

升级时继续使用原来的数据目录。账户、会话和电脑身份都在数据中，换程序不需要重新创建它们。重要工作开始前，保留一份独立备份；升级或恢复会重启服务，请先结束正在运行的任务。

## 更新桌面版

在 **设置 → 关于与诊断** 检查更新，按页面提示安装并重启。官方 macOS 安装版、Windows EXE 安装版和 Linux AppImage 支持应用内更新。

使用 Linux DEB、Windows ZIP 或自行构建的程序时，从[下载页](/zh-cn/docs/start/get-astral/)取得新版本，替换程序并继续使用原数据。网页访问桌面版时，页面上的更新操作更新的是那台电脑上的桌面应用。

## 更新 Server

官方 Linux 安装可以在 **设置 → 关于与诊断** 更新。也可以用原系统账号重新执行[安装命令](/zh-cn/docs/hosting/server/#升级)；自定义安装目录需要继续使用原 `--root`。

等待服务重启并重新连接，再检查版本和任务记录。下载失败时按错误重试；启动失败时，安装器会尝试恢复原程序和本次数据备份。旧安装只提供手动更新时，可以先用当前安装器更新一次。

手动 `serve`、自定义服务或自行制作的容器，需要按自己的部署方式停止服务、替换程序，再用**原数据目录和启动配置**运行。

## 更新远程电脑或容器

在 **设置 → 电脑** 打开那台电脑的详情，使用 **Daemon 维护**中的更新操作。Daemon 是安装在该电脑上的 Astral 连接程序。更新会中断这台电脑正在执行的任务，完成后应看到同一台电脑重新连接。

按钮不可用时，使用详情页提供的完整更新命令：裸机在那台电脑执行，容器在它的 Docker 宿主机执行。服务所在电脑的连接程序随 Server 或桌面版更新。

更新容器里的 Astral 程序会保留原容器、文件和已安装软件。要换一套基础环境，需要先备份工作资料，再创建新容器。

## 备份哪些内容

Astral 的记录和 Bot 实际处理的文件，可能位于不同电脑上：

| 要保留什么 | 备份范围 |
| --- | --- |
| 账户、Bot、模型连接、会话历史和电脑连接身份 | 完整 Server 数据目录，包括数据库、`master.key`、`computer-identity/` 和配置 |
| 本机或远程电脑上的工作资料 | 对应电脑的 Astral 数据、Bot 工作目录，以及任务实际使用的项目和文件 |
| 容器中的工作资料 | 数据卷；需要保留的软件和其他容器内改动还要保留可写层，或准备重建方法 |

完整复制 Server 目录才能保留读取已保存凭据的密钥，以及原电脑信任的 Server 身份。备份里含有这些敏感内容，应放在只有自己能访问的位置。

### 官方 Linux 安装的备份示例

下面适用于默认安装。以运行 Astral 的系统账号，在同一个终端依次完成。使用了 `--root` 时，把 `astral_install_root` 改成原来的绝对安装目录。

**先停止服务并确认停止：**

```sh
systemctl --user stop astral-server.service
systemctl --user show astral-server.service --property=ActiveState,MainPID
```

只有状态为 `inactive` 或 `failed`、且 `MainPID=0` 时才继续复制。这样可避免备份到正在写入一半的数据库。

**复制整个 `data/` 目录，并记录版本与服务配置：**

```sh
astral_install_root="${XDG_DATA_HOME:-$HOME/.local/share}/astral-server"
astral_backup_dir="$HOME/astral-backups/$(date +%Y%m%d-%H%M%S)"
mkdir -p "$astral_backup_dir"
chmod 700 "$astral_backup_dir"
cp -a "$astral_install_root/data" "$astral_backup_dir/data"
cp -a "$astral_install_root/installation.json" "$astral_backup_dir/installation.json"
cp -a "${XDG_CONFIG_HOME:-$HOME/.config}/systemd/user/astral-server.service" "$astral_backup_dir/astral-server.service"
astral version > "$astral_backup_dir/program-version.txt"
echo "$astral_backup_dir"
```

最后一行显示备份位置。这里包含 Server、本机电脑和自动升级备份；远程电脑文件和 Docker 数据卷需要另外备份。把这份备份再复制到另一块磁盘或自己的备份服务，才能应对原磁盘损坏。

**复制成功后恢复服务：**

```sh
systemctl --user start astral-server.service
```

桌面版和手动部署也应先彻底退出应用或停止进程，再备份实际目录。运行中备份需要专门的一致性备份方式，不能直接复制正在写入的数据库。

## 找到数据目录

已设置自定义目录时，以实际启动参数和配置为准。默认位置如下：

| 安装方式 | Server 与本机电脑数据 |
| --- | --- |
| 官方 Linux 安装器 | `${XDG_DATA_HOME:-$HOME/.local/share}/astral-server/data/server/` 和同级 `computers/` |
| macOS 桌面版 | `~/Library/Application Support/astral/schema-3-core-11/` 下的 `server/`、`computers/` |
| Windows 桌面版 | `%APPDATA%\astral\schema-3-core-11\` 下的 `server\`、`computers\` |
| Linux 桌面版 | 通常是 `~/.config/astral/schema-3-core-11/` 下的 `server/`、`computers/` |
| 手动运行 `serve` | 系统 Astral 配置目录下的 `server/`、`computers/`，或显式指定的目录 |

手动运行的 Astral 配置目录在 macOS 为 `~/Library/Application Support/astral`，Windows 为 `%APPDATA%\astral`，Linux 为 `${XDG_CONFIG_HOME:-$HOME/.config}/astral`。

需要给恢复命令填写 `--data` 时，使用上表中的 **Server 数据目录**，例如官方安装的 `…/data/server/`。

## 恢复后检查

以下步骤用于在**原系统账号、原目录**恢复自己制作的完整备份。先核对备份记录的程序版本，使用匹配的程序。更换机器或安装路径需要另外安排部署，不能只把目录搬走后沿用旧的启动配置。

1. 停止桌面版或 Server。官方 Linux 安装使用上面的 `stop` 与 `show` 命令，确认 `MainPID=0`。
2. 先把当前数据目录改名保留，再将备份复制回原数据位置，保留目录归属和读取权限，不要把新旧目录内容合并。使用上面的 Linux 备份时，把备份的 `data/` 恢复为原安装根下的 `data/`；不要把它放进 `data/server/`。
3. 核对原启动配置是否仍对应这个安装。需要时一并恢复工作电脑的文件或 Docker 数据卷；Server 的备份不会从其他电脑取回文件。
4. 启动 Astral，确认能够登录、历史可见、模型连接可用、电脑重新在线。再恢复实际任务。

不要让两个 Server 同时使用同一份数据目录。自动生成的升级快照包含清单和 `files/`，与上述手动备份格式不同，不能把整个快照目录直接覆盖成 Server 数据目录；失败升级应先按程序提示恢复。

## 升级失败或磁盘空间不足

数据格式需要转换时，Astral 会先备份；失败或中途退出后，会在下次启动时尝试恢复。升级备份位于 Server 数据目录同级的 `upgrade-backups/`，官方 Linux 默认位置为 `<安装根目录>/data/upgrade-backups/`。

空间不足时，先释放数据和备份所在磁盘的空间，再重试。大型数据库需要额外空间保存备份和临时文件。已经启动并接受新任务后，不要直接用旧备份覆盖新工作。

## 查看与清理存储

在 **设置 → 数据与存储** 查看占用，并按页面说明清理不需要的内容。删除备份会永久失去那份恢复副本；先保留仍有价值的独立备份。

清理会话与清理备份的影响不同，见[会话与记录](/zh-cn/docs/usage/conversations/)。删除后，数据库空闲空间由 Astral 在后台整理，磁盘占用可能稍后下降。

需要离线整理数据库时，先备份并停止 Astral，再执行：

```sh
astral maintenance vacuum --data /实际的/server/数据目录
```

该命令保留会话和升级备份。完成后再启动 Astral。

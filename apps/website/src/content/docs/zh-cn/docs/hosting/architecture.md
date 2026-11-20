---
title: "选桌面版还是 Server"
description: "根据使用设备和是否需要持续在线，选择 Astral 的运行方式。"
---

只想先在自己的电脑上使用 Astral，可以直接[下载桌面版](/zh-cn/docs/start/get-astral/)。桌面版已经包含运行所需的服务，安装后打开即可开始配置。

如果你希望电脑合上以后，Bot 仍能接收聊天消息、执行计划任务，可以把 Astral 安装到一台长期运行的 Linux 服务器上。这种运行方式称为 **Server**，你通过浏览器使用它。

## 根据使用场景选择

| 你想怎样使用 | 适合的方式 |
| --- | --- |
| 在自己的 Mac、Windows 或 Linux 电脑上试用，处理这台电脑的文件 | 桌面版 |
| 让 Bot 全天在线，从手机或不同电脑的浏览器访问 | 在长期运行的机器上安装 Server |
| 已经在用 Astral，只想增加一台可执行任务的电脑 | 保留现有 Astral，直接[连接那台电脑](/zh-cn/docs/computers/connect/) |

桌面版所在电脑需要保持开机，Astral 需要保持运行。电脑休眠或退出 Astral 后，依赖它的任务和聊天连接会中断。

## 任务会在哪台电脑上执行？

<figure class="astral-diagram" data-pagefind-ignore>
  <svg viewBox="0 0 720 250" xmlns="http://www.w3.org/2000/svg" role="img" aria-label="Astral 工作方式示意图：入口连接到 Astral，由 Bot 在已授权的电脑或容器中执行任务，并调用你连接的模型服务">
    <defs>
      <marker id="astral-arr-zh" viewBox="0 0 10 10" refX="8" refY="5" markerWidth="6" markerHeight="6" orient="auto-start-reverse">
        <path d="M 1 2 L 8 5 L 1 8" fill="none" stroke="var(--subtle)" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round" />
      </marker>
    </defs>
    <!-- Column labels -->
    <text x="108" y="22" text-anchor="middle" fill="var(--muted)" font-size="11.5">你从哪里发任务</text>
    <text x="360" y="22" text-anchor="middle" fill="var(--muted)" font-size="11.5">Astral 核心</text>
    <text x="612" y="22" text-anchor="middle" fill="var(--muted)" font-size="11.5">实际执行与文件位置</text>
    <!-- Left column: Entrypoints -->
    <rect x="20" y="38" width="176" height="54" rx="7" fill="var(--page)" stroke="var(--line)" />
    <text x="108" y="61" text-anchor="middle" fill="var(--ink)" font-size="13" font-weight="500">桌面版 / 浏览器</text>
    <text x="108" y="80" text-anchor="middle" fill="var(--muted)" font-size="11">发起会话、查看过程与文件</text>
    <rect x="20" y="108" width="176" height="54" rx="7" fill="var(--page)" stroke="var(--line)" />
    <text x="108" y="131" text-anchor="middle" fill="var(--ink)" font-size="13" font-weight="500">聊天软件</text>
    <text x="108" y="150" text-anchor="middle" fill="var(--muted)" font-size="11">Telegram · QQ · 微信 · 飞书</text>
    <!-- Center: Astral -->
    <rect x="256" y="46" width="208" height="108" rx="8" fill="var(--surface)" stroke="var(--subtle)" />
    <text x="360" y="77" text-anchor="middle" fill="var(--ink)" font-size="14" font-weight="600">Astral</text>
    <text x="360" y="97" text-anchor="middle" fill="var(--muted)" font-size="11.5">Desktop 本机 或 Linux Server</text>
    <line x1="278" y1="112" x2="442" y2="112" stroke="var(--line)" />
    <text x="360" y="133" text-anchor="middle" fill="var(--muted)" font-size="11">Bot 权限 · Session · 定时与事件</text>
    <!-- Right column: Computers -->
    <rect x="524" y="34" width="176" height="40" rx="7" fill="var(--page)" stroke="var(--line)" />
    <text x="612" y="59" text-anchor="middle" fill="var(--ink)" font-size="12.5" font-weight="500">本机电脑（安装所在机器）</text>
    <rect x="524" y="84" width="176" height="40" rx="7" fill="var(--page)" stroke="var(--line)" />
    <text x="612" y="109" text-anchor="middle" fill="var(--ink)" font-size="12.5" font-weight="500">其他已连接电脑</text>
    <rect x="524" y="134" width="176" height="40" rx="7" fill="var(--page)" stroke="var(--line)" />
    <text x="612" y="159" text-anchor="middle" fill="var(--ink)" font-size="12.5" font-weight="500">Docker 容器（可选隔离）</text>
    <!-- Bottom: Model services -->
    <rect x="236" y="194" width="248" height="42" rx="7" fill="var(--page)" stroke="var(--line)" stroke-dasharray="4 3" />
    <text x="360" y="213" text-anchor="middle" fill="var(--ink)" font-size="12.5" font-weight="500">你连接的模型服务</text>
    <text x="360" y="228" text-anchor="middle" fill="var(--muted)" font-size="10.5">API Key · 账号登录 · 自定义服务</text>
    <!-- Connectors -->
    <path d="M 196 65 L 250 86" fill="none" stroke="var(--subtle)" stroke-width="1.25" marker-end="url(#astral-arr-zh)" />
    <path d="M 196 135 L 250 116" fill="none" stroke="var(--subtle)" stroke-width="1.25" marker-end="url(#astral-arr-zh)" />
    <path d="M 464 82 L 518 56" fill="none" stroke="var(--subtle)" stroke-width="1.25" marker-end="url(#astral-arr-zh)" />
    <path d="M 464 104 L 518 104" fill="none" stroke="var(--subtle)" stroke-width="1.25" marker-end="url(#astral-arr-zh)" />
    <path d="M 464 126 L 518 152" fill="none" stroke="var(--subtle)" stroke-width="1.25" marker-end="url(#astral-arr-zh)" />
    <path d="M 360 154 L 360 189" fill="none" stroke="var(--subtle)" stroke-width="1.25" stroke-dasharray="3 3" marker-end="url(#astral-arr-zh)" />
  </svg>
  <figcaption>无论从桌面应用、浏览器还是聊天软件发起任务，都会先到达 Astral，再由 Bot 按权限调度对应电脑或容器执行；文件始终留在执行它的那台机器上。</figcaption>
</figure>

用桌面版时，首次配置可以直接选择你正在使用的电脑。Bot 获得授权后，就能使用这台电脑上的文件和工具。

用 Server 时，浏览器负责显示界面，任务默认在服务器上执行。例如，你在手机浏览器里让 Bot 整理文件，它处理的是服务器上的文件。要操作笔记本上的文件，先把笔记本[连接为一台电脑](/zh-cn/docs/computers/connect/)，再授权给 Bot，并在会话里选择它。

同一个 Astral 可以连接多台电脑。你可以在一个会话中切换工作机器；各台电脑上的文件仍保存在原处，需要时让 Bot 明确复制文件。例子见[跨电脑工作](/zh-cn/docs/computers/across-computers/)。

## 什么时候需要容器？

希望给任务一套单独的文件和软件环境时，可以使用[容器电脑](/zh-cn/docs/computers/containers/)。例如，为公开群聊的 Bot 准备独立环境，避免直接开放自己的日常电脑。

容器需要先准备 Docker。第一次体验 Astral，可以先在你信任的本机环境完成一个简单任务，再根据实际需要增加容器。

决定使用服务器后，继续[在 Linux 上安装 Server](/zh-cn/docs/hosting/server/)。

---
title: "Desktop or Server?"
description: "Choose where to run Astral based on your devices and whether it needs to stay online."
---

To start using Astral on your own computer, [download the desktop app](/docs/start/get-astral/). It includes the services it needs. Install it, open it, and complete the initial setup.

To keep a Bot receiving messages and running scheduled tasks while your laptop is closed, install Astral on a Linux machine that stays on. This is the **Server** option: you use it through a browser.

## Choose for your situation

| What you want to do | Choose |
| --- | --- |
| Try Astral on your Mac, Windows, or Linux computer and work with its files | Desktop |
| Keep a Bot online and use it from a phone or several computers | Server on a machine that stays on |
| Add another computer to an Astral installation you already use | Keep that installation and [connect the additional computer](/docs/computers/connect/) |

For Desktop to keep working, its computer must stay awake and Astral must remain running. Sleep or quitting Astral interrupts the tasks and chat connections that depend on it.

## Where does the work happen?

<figure class="astral-diagram" data-pagefind-ignore>
  <svg viewBox="0 0 720 250" xmlns="http://www.w3.org/2000/svg" role="img" aria-label="How Astral connects your interfaces, Bots and sessions, authorized computers or containers, and your model service">
    <defs>
      <marker id="astral-arr-en" viewBox="0 0 10 10" refX="8" refY="5" markerWidth="6" markerHeight="6" orient="auto-start-reverse">
        <path d="M 1 2 L 8 5 L 1 8" fill="none" stroke="var(--subtle)" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round" />
      </marker>
    </defs>
    <!-- Column labels -->
    <text x="108" y="22" text-anchor="middle" fill="var(--muted)" font-size="11.5">Where you send tasks</text>
    <text x="360" y="22" text-anchor="middle" fill="var(--muted)" font-size="11.5">Astral runtime</text>
    <text x="612" y="22" text-anchor="middle" fill="var(--muted)" font-size="11.5">Where work &amp; files live</text>
    <!-- Left column: Entrypoints -->
    <rect x="20" y="38" width="176" height="54" rx="7" fill="var(--page)" stroke="var(--line)" />
    <text x="108" y="61" text-anchor="middle" fill="var(--ink)" font-size="13" font-weight="500">Desktop / Browser</text>
    <text x="108" y="80" text-anchor="middle" fill="var(--muted)" font-size="11">Sessions, tools, and files</text>
    <rect x="20" y="108" width="176" height="54" rx="7" fill="var(--page)" stroke="var(--line)" />
    <text x="108" y="131" text-anchor="middle" fill="var(--ink)" font-size="13" font-weight="500">Chat platforms</text>
    <text x="108" y="150" text-anchor="middle" fill="var(--muted)" font-size="11">Telegram · QQ · WeChat · Feishu</text>
    <!-- Center: Astral -->
    <rect x="256" y="46" width="208" height="108" rx="8" fill="var(--surface)" stroke="var(--subtle)" />
    <text x="360" y="77" text-anchor="middle" fill="var(--ink)" font-size="14" font-weight="600">Astral</text>
    <text x="360" y="97" text-anchor="middle" fill="var(--muted)" font-size="11.5">Local Desktop or Linux Server</text>
    <line x1="278" y1="112" x2="442" y2="112" stroke="var(--line)" />
    <text x="360" y="133" text-anchor="middle" fill="var(--muted)" font-size="11">Bots · Sessions · Schedules</text>
    <!-- Right column: Computers -->
    <rect x="524" y="34" width="176" height="40" rx="7" fill="var(--page)" stroke="var(--line)" />
    <text x="612" y="59" text-anchor="middle" fill="var(--ink)" font-size="12.5" font-weight="500">Host computer</text>
    <rect x="524" y="84" width="176" height="40" rx="7" fill="var(--page)" stroke="var(--line)" />
    <text x="612" y="109" text-anchor="middle" fill="var(--ink)" font-size="12.5" font-weight="500">Other connected computers</text>
    <rect x="524" y="134" width="176" height="40" rx="7" fill="var(--page)" stroke="var(--line)" />
    <text x="612" y="159" text-anchor="middle" fill="var(--ink)" font-size="12.5" font-weight="500">Docker containers (optional)</text>
    <!-- Bottom: Model services -->
    <rect x="236" y="194" width="248" height="42" rx="7" fill="var(--page)" stroke="var(--line)" stroke-dasharray="4 3" />
    <text x="360" y="213" text-anchor="middle" fill="var(--ink)" font-size="12.5" font-weight="500">Your model services</text>
    <text x="360" y="228" text-anchor="middle" fill="var(--muted)" font-size="10.5">API keys · account login · custom</text>
    <!-- Connectors -->
    <path d="M 196 65 L 250 86" fill="none" stroke="var(--subtle)" stroke-width="1.25" marker-end="url(#astral-arr-en)" />
    <path d="M 196 135 L 250 116" fill="none" stroke="var(--subtle)" stroke-width="1.25" marker-end="url(#astral-arr-en)" />
    <path d="M 464 82 L 518 56" fill="none" stroke="var(--subtle)" stroke-width="1.25" marker-end="url(#astral-arr-en)" />
    <path d="M 464 104 L 518 104" fill="none" stroke="var(--subtle)" stroke-width="1.25" marker-end="url(#astral-arr-en)" />
    <path d="M 464 126 L 518 152" fill="none" stroke="var(--subtle)" stroke-width="1.25" marker-end="url(#astral-arr-en)" />
    <path d="M 360 154 L 360 189" fill="none" stroke="var(--subtle)" stroke-width="1.25" stroke-dasharray="3 3" marker-end="url(#astral-arr-en)" />
  </svg>
  <figcaption>Tasks sent from Desktop, a browser, or a chat app go to Astral, where your Bot runs work on the authorized computer or container. Files stay on the machine where work runs.</figcaption>
</figure>

During Desktop setup, you can select the computer you are using. Once authorized, the Bot can use that computer's files and tools.

With Server, the browser displays the interface while work initially happens on the server machine. For example, asking Astral to organize files from your phone means organizing files on the server. To work with files on your laptop, [connect the laptop](/docs/computers/connect/), authorize the Bot to use it, and select it in the conversation.

One Astral installation can use several computers. You can switch computers within a conversation, while files remain on their original machines. Ask the Bot to copy files when the task requires it. See [Work across computers](/docs/computers/across-computers/) for an example.

## When would I use a container?

Use a [container computer](/docs/computers/containers/) when a task needs its own files and software environment. For example, you could give a public group-chat Bot a separate environment instead of access to your everyday computer.

Containers require Docker. For your first experience, you can complete a small task on a computer you trust, then add containers when you need them.

If you have chosen a server, continue with [Install Server on Linux](/docs/hosting/server/).

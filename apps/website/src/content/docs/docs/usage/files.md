---
title: "Files and results"
description: "Upload material or point a Bot at paths on a connected computer, preview outputs, and save what you need."
---

You can attach files directly or point your Bot at files already on a connected computer. **The computer selected in the session determines where uploads land and where commands run**—the browser is just your window to it.

For example, if you open Astral on your laptop while the session targets “Office computer,” uploads and generated files live on the office computer.

## Provide files

- **Upload attachments:** check the session's selected computer first, attach your files, and state the goal and whether to keep originals untouched:

> Turn the attached meeting notes into an action list with owner and due date (mark missing items as “To confirm”). Keep the original, save a separate meeting-actions.md, and give me a download link.

- **Use files already on the computer:** provide the full path on that machine (such as `/home/mei/reports/meeting.txt` or `C:\Users\mei\reports\meeting.txt`). If unsure, ask the Bot to list the current working directory first.

:::note[Tool size limits and data flow]
- Built-in file tools read or write up to **32 MiB** per operation (separate from attachment upload/download limits). For larger files, ask the Bot to use shell commands.
- Content the Bot reads is sent to your connected model provider—check [where data goes](/docs/start/safe-use/#where-does-my-data-go) before sharing sensitive files.
:::

## View, preview, and download results

- **Images:** click any image in the conversation to open the full-screen viewer with zoom and download.
- **Docs and code:** click file links to open the right-hand preview (reading view for Markdown, code/text view for scripts and logs, with copy and download buttons).
- **Browse folders:** click **Files** at the top of the workspace to browse the computer's directory; use **Refresh preview** after a file is updated.
- **If the Bot finished without giving a link**, ask:

> Give me a link to the file you created, along with its source computer and full path.

:::tip[Links remember their source computer]
Switching the session to another computer later does not move existing files or break older links. Download or back up anything you want to keep long term.
:::

### If a file won't open

1. Check **Settings → Computers** to confirm the source computer is online.
2. Check the Bot's **Computer access** to confirm it still has permission.
3. Ask the Bot to verify the file still exists and is readable at its original path.

## Working directories

The working directory is the folder where the Bot starts by default (ask “Which computer and working directory are you using?” anytime).

- Multiple sessions of the same Bot can share a default directory—give separate projects their own folders.
- Folders only organize work; actual access rights come from the OS user running the computer service. For real isolation, use a dedicated OS account or a [container computer](/docs/computers/containers/).

## Chat attachments

In Telegram, QQ, WeChat, or Feishu, send attachments alongside your prompt. To receive results back in the chat, say so explicitly:

> Send meeting-actions.md back to this chat as an attachment.

Verify the attachment actually arrives in the chat app (creating a file inside Astral does not send it automatically, and platforms enforce their own size and format limits). Astral streams the attachment straight from its source computer—keep the file unchanged until sending completes.

:::tip[Need to read fine text in a chat image?]
Incoming chat images use lightweight previews by default. For screenshots with small text, ask the Bot to “download and read the original image.”
:::

<details class="astral-doc-details">
<summary>Preview details, native Reveal, path formats, and image compression settings</summary>

- **Windows path formats:** On Windows computers, file tools accept native `C:\...` paths as well as `/c/...`, `/mnt/c/...`, and `/cygdrive/c/...`.
- **Desktop native Reveal:** In the Desktop app, files on your local Desktop-managed computer include a context-menu action to reveal the file or folder in Finder / File Explorer. Remote computers, containers, and browser sessions support in-app browsing, previews, and downloads.
- **Preview & directory behavior:** Opening a file tab locates it in the file manager without changing the computer or working directory the Bot uses for tasks. On partially loaded large files, **Copy preview content** copies only the loaded excerpt; download for the full file. Some links point to historical snapshots—ask for the original path to edit the live file.
- **Image compression thresholds:** Computer image reads preserve original dimensions and encode as quality-75 WebP above 150 KB (leaving the file on disk untouched). Chat previews use quality-30 WebP above 30 KB. Tool-returned images remain saved in session history. If an image shows unavailable, ask the Bot to re-download it or upload again. See [Updates and backups](/docs/hosting/maintenance/) for full backups.

</details>

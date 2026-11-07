---
title: "Files and results"
description: "Provide source material, check generated files, and keep a copy where you need it."
---

Give the Bot an attachment or point it to files on a connected computer. The computer selected in the session determines where files are uploaded and work is performed. Your browser is the interface to that computer.

For example, if you open Astral on a laptop but select “Office computer,” uploads and generated files live on the office computer. Content the Bot reads may be sent to the selected model service; see [where data goes](/docs/start/safe-use/#where-does-my-data-go) before providing sensitive material.

## Provide files

Check the selected computer before attaching a file: that is where the upload goes. Explain which material to process, what to produce, and whether to preserve the original. For example:

> Turn the attached meeting notes into an action list with an owner and due date for each item. Mark missing details as “To confirm.” Keep the original and save a separate meeting-actions.md file. Give me a download link.

If a file is already on the computer, provide its full path, such as `/home/mei/reports/meeting.txt`. That path must be on the computer the Bot is using. If you are unsure, ask it to report its current directory and list the files before doing the work.

On Windows, use the native path, for example `C:\Users\mei\reports\meeting.txt`. File tools also accept `/c/Users/mei/...`, `/mnt/c/Users/mei/...`, and `/cygdrive/c/Users/mei/...` as Windows drive paths when the selected computer runs Windows. The selected computer interprets paths using its own operating system.

## Open results

Click an image in the conversation to open the full-screen viewer, where you can zoom and download it. Other files open directly in the right-hand preview. Files and subagent conversations have separate tabs, so you can switch between them without finding their links again. Available previews depend on the file format. Download a copy to your own device or back it up on the source computer when you need to keep it.

Markdown opens in a reading view. Code, configuration files, logs, and other files recognized as text use a code or plain-text preview, including files with an unfamiliar extension or no extension. Use **Copy content** in the preview toolbar to copy the text; Markdown copies its source text. When only part of a large file has loaded, **Copy preview content** copies only the current excerpt. Download the file to get its complete contents.

The workspace normally sits beside the conversation, with separate space for each. Drag the divider between them to adjust their widths. As available width decreases, the workspace narrows first; it overlays the conversation only when there is no longer enough room for both.

The **Files** button at the top of the panel shows or hides the file manager to the right of the preview. It stays in the same place in both states. Drag the divider between the preview and file manager to adjust their widths. In a narrow workspace, the file manager opens as a drawer over the preview. Click a file row to preview it; its context menu lets you download the file or copy the path.

You can use **Files** to browse the current session computer's working directory before opening any attachment. Until a file is opened, this directory follows changes to the session computer. Opening a file locates it on its source computer; switching file tabs changes the directory view to that file's computer and location. Changing the session computer keeps existing file tabs and their original sources. A hidden file manager stays hidden when you switch files. Browsing folders or opening a file does not change the computer or working directory the Bot uses to execute your task.

Subagent tabs show their conversations and tool activity in read-only form. To give a subagent more work or change its instructions, return to the original conversation and tell the Bot.

If the Bot reports completion without a link, ask:

> Give me a link to the file you created, along with its source computer and full path.

The link retains its original computer. Selecting a different computer later does not move the file or change where an older link points.

Use **Refresh preview** to read an updated working file without closing its tab. Refreshing the file manager updates its directory listing separately.

In the Desktop app, a file's context menu can reveal it in Finder, File Explorer, or the system file manager when its source is the actual local Computer managed by that Desktop. The corresponding directory action opens the directory. Remote and container files can be browsed, previewed, and downloaded in Astral; they do not open as paths on your own machine. The browser interface does not provide native file-manager actions.

A link may refer to a preserved snapshot. Its preview, download, and file-manager action use the snapshot's actual location on the source computer. Ask the Bot for the original path when you need the current working file.

When the Bot reads an image on a computer, it preserves the original dimensions by default. Images above the default 150KB encoded-data compression threshold use quality-75 WebP; smaller images keep their original bytes. Final sizes vary with the image content. The original file on the computer stays intact.

Images returned directly by a tool are retained in the conversation and remain available when you reopen its history. Displaying them does not require an extra file copy on the computer.

### A file will not open

Check these in order:

1. Confirm that the source computer is online in **Settings → Computers**.
2. Check that the Bot still has permission to use it under **Computer access**.
3. Ask the Bot to check whether the original path still exists and is readable. Locate a moved file or restore a deleted one from backup.

A link can remain in history after its source file has disappeared. Keep a separate copy of important results.

## Working directories

The working directory is the folder the Bot uses by default for its operations. Ask “Which computer and working directory are you using?” to check it. For another project, ask the Bot to switch to that project's directory before working and confirm the full path.

Several sessions belonging to the same Bot can share its default directory on a computer. A new session does not automatically get separate files. Give projects distinct folders to keep their material apart.

Folders organize work; the operating-system account running the computer service determines access. The Bot may be able to read or change other files available to that account. Use a separate system account or a [container computer](/docs/computers/containers/) when you need a narrower environment.

## Chat attachments

In a connected Telegram, QQ, or other platform chat, send attachments with the task you want performed. Chat images use a lighter preview: a 30KB encoded-data compression threshold and quality-30 WebP, keeping the original dimensions by default. These are compression settings rather than a fixed output size. The preview is prepared on arrival and reused in later replies. When a task depends on small text or fine detail, ask the Bot to download and read the original image. Downloads preserve the original file. Videos and ordinary files are downloaded to a computer when the task needs them.

To return a result to the chat, ask explicitly:

> Send meeting-actions.md back to this chat as an attachment.

Check that the attachment actually arrives. Platform file limits, supported formats, and account permissions can affect sending. Creating a file or displaying its path in Astral does not send a platform attachment.

If an image is marked unavailable, ask the Bot to retry downloading it or upload it again. For a full backup, preserve Server data, computer work files, and container data separately; see [Updates and backups](/docs/hosting/maintenance/).

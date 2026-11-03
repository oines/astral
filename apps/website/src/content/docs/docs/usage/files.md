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

## Open results

Click an image in the conversation to open the full-screen viewer, where you can zoom and download it. Other files open in the file sidebar; available previews depend on the file format. Download a copy to your own device or back it up on the source computer when you need to keep it.

If the Bot reports completion without a link, ask:

> Give me a link to the file you created, along with its source computer and full path.

The link retains its original computer. Selecting a different computer later does not move the file or change where an older link points.

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

In a connected Telegram, QQ, or other platform chat, send attachments with the task you want performed. Images initially supply a compressed preview. If text or detail is unclear, ask the Bot to download and read the original image. Videos and ordinary files are downloaded to a computer when the task needs them.

To return a result to the chat, ask explicitly:

> Send meeting-actions.md back to this chat as an attachment.

Check that the attachment actually arrives. Platform file limits, supported formats, and account permissions can affect sending. Creating a file or displaying its path in Astral does not send a platform attachment.

If an image is marked unavailable, ask the Bot to retry downloading it or upload it again. For a full backup, preserve Server data, computer work files, and container data separately; see [Updates and backups](/docs/hosting/maintenance/).

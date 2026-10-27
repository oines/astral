---
title: "Files and results"
description: "Provide input files, open generated results, and know where they are stored."
---

Work files live on the computer executing the task. File links retain their source, so switching computers does not change where an earlier link points.

## Provide files

Choose a computer in the session before uploading an attachment. The upload goes to that computer, not the public website or the device displaying your browser. You can also give the Bot an absolute path on a connected computer.

Ask it to confirm the file and location when multiple computers have similar directories.

## Open results

Open a returned file link to preview or download it. Images can appear in the conversation; previews for other files depend on their format.

Reading the original content requires the source computer to be online and the Bot to retain access. Keeping conversation history does not back up that file to the Server.

## Working directories

A Bot has a default working directory on each computer, which multiple sessions may share. Use separate project directories or containers when files should be separated.

The default directory is not a filesystem sandbox. Access follows the permissions of the account running the computer service.

## Chat attachments

Incoming chat attachments retain their source information and can be read or downloaded to a computer as needed. Sending a file also depends on platform permissions and format restrictions.

Before a new IM image enters the Bot's passive context, Astral creates a proportional preview of at most 75,000 pixels and labels it as compressed. The original attachment reference remains available. If text or details are unclear, the Bot can download the original and read it with the existing file tools. The same stored preview is used by group participation decisions, ordinary requests and later context compaction. Existing history and images opened through file tools are unchanged.

A file visible in Astral has not necessarily been sent to the external chat. Ask explicitly to send the result back and check the send outcome.

Back up Server data, computer work files, and container data separately. See [Updates and backups](/docs/hosting/maintenance/).

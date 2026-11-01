---
title: "Permissions and privacy"
description: "Decide which computers and files your assistant can use, and separate private work from shared chats."
---

Before using Astral, decide **which Bot can use which computers, which files it can reach, and who can give it tasks**. Use the sections below for your situation.

## Personal use: try a copy first

A Bot runs commands on the computers you authorize. It can use the files, network connections, and tools available to the OS account running the computer service. Before a first attempt at reorganizing, editing, or deleting files, make a test copy and check the result.

Review or remove access under **Settings → Bots → your Bot → Computer access**. A default computer is the starting choice for a task. The Bot can still use its other authorized computers.

**A working directory does not restrict file access.** Working in a project folder can still leave other files readable by the same OS account. To separate material, use a restricted OS account, a dedicated computer, or an [isolated container](/docs/computers/containers/). A computer service running as root has root privileges.

<span id="use-a-separate-environment-for-public-groups"></span>

## Group chats: use a dedicated Bot

Once you authorize a group, its members can give the Bot tasks. Astral has no additional allowlist of members permitted to give commands, and computer commands do not each require human approval.

Before connecting a public group:

1. [Create a dedicated Bot](/docs/usage/bots/) for it.
2. Authorize only a dedicated computer or container. Check that your everyday personal computer is absent from its access list.
3. Provide only the files needed for group tasks. Do not mount your whole home directory, SSH keys, or a Docker socket into the container.
4. Keep the platform account on **Specified chats only** and add the intended group.

If the platform account can mute members, remove them, or publish announcements, the Bot can use those permissions. Astral does not additionally require the person requesting the action to be a group administrator. Control the account's permissions on the chat platform.

## Separate private work from shared chats

Conversations belonging to one Bot can use information from other conversations and chats it is allowed to access. They may also share files on a computer.

If you use the same Bot, you can [make a personal conversation private](/docs/usage/conversations/#private-sessions). Other conversations then cannot discover or read it through Astral's built-in API. The Web administrator can still manage it, and files on a shared computer need separate access controls.

Different Bots with separately assigned computers make different trust boundaries easier to manage. Information already copied elsewhere must be handled at that location.

<span id="know-where-data-goes"></span>

## Where does my data go?

The model service you select receives the material needed to do the task, such as conversation text, files the Bot reads, images, and command output. Web search sends queries to the selected search service as well.

A file can remain on your own computer while some of its contents are sent to a model. Before using sensitive material, check that you accept the provider's data practices. Private conversations also use your selected model service.

In a connected group, ordinary messages delivered to the Bot can inform later replies. Turning off proactive participation reduces tasks the Bot starts on its own, but does not exclude those messages from its context. Make sure participants understand this before connecting a shared chat.

<span id="review-outside-content-and-skills"></span>

## Use care with third-party material and skills

Webpages, attachments, and third-party projects can contain instructions that try to redirect the Bot. Check the result on test material first. Review a skill's source, instructions, and bundled scripts before installing it.

Personality instructions, skills, and a project's `AGENTS.md` guide the Bot's behavior. Use computer permissions and OS access controls to restrict the material it can reach.

## If a task goes wrong

1. Select **Stop** in the conversation.
2. Review the commands already run, files changed, and messages sent. Restore your own backup where needed.
3. For tasks arriving from a chat platform, disable that account to pause future incoming messages. Stop existing computer work in its conversation too.
4. If necessary, remove the Bot's computer access or [disconnect the computer](/docs/computers/connect/#disconnect-and-stop-a-computer).

Stopping or removing access affects later actions. Completed file changes and sent messages need separate handling.

## Protect sign-in details and backups

The Web account manages the whole Astral installation, including private conversations. Do not share it as a restricted account for group members. For remote access, configure HTTPS and login protection using [Sign-in and security](/docs/hosting/security/).

Keep API keys, platform tokens, recovery codes, and computer connection commands private. Remove them and personal information before sharing screenshots or logs. A complete backup contains chat history and keys needed to recover saved credentials, so store it as sensitive material.

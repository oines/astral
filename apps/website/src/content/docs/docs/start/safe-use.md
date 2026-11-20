---
title: "Permissions and privacy"
description: "Set clear boundaries for which computers, files, and chats each Bot can reach."
---

Using Astral safely comes down to one rule: **be deliberate about which Bot can access which computer, which files live there, and who can send it tasks.**

## Personal use: protect your files

When a Bot runs commands on a computer, it inherits the files, network, and tools available to **the OS account running the computer service**.

:::caution[A working directory is not a security boundary]
Asking a Bot to work inside one project folder does not stop it from reading other locations accessible to that OS account—and a service running as `root` has full root privileges. For true file isolation, use a restricted OS account, a dedicated machine, or a [container computer](/docs/computers/containers/).
:::

Good habits:
- Try bulk edits, reorganizing, or deletions on a copy first.
- Review or revoke access anytime in **Settings → Bots → your Bot → Computer access**. A **default** computer is just where new sessions start; the Bot can still use its other authorized computers.

<span id="use-a-separate-environment-for-public-groups"></span>

## Group chats: use a dedicated Bot

:::caution[Anyone in an authorized group can direct the Bot]
Astral does not add a per-member command allowlist or prompt for approval before each command. If the chat account has platform permissions such as muting, kicking, or posting announcements, the Bot can use them too.
:::

Before adding Astral to a public group, isolate it in 4 steps:

1. **[Create a dedicated Bot](/docs/usage/bots/):** keep it separate from your personal assistant.
2. **Authorize only a dedicated machine or [Docker container](/docs/computers/containers/):** verify your everyday computer is not on its access list.
3. **Expose only what the group needs:** never mount your full home directory, SSH keys, or a Docker socket into the container.
4. **Scope the chat account:** keep it on **Specified chats only**, add only the target group, and restrict admin powers on the chat platform itself.

## Separate private work from shared chats

Sessions under the same Bot can look up context from each other and may share folders on a computer.

- **Cleanest approach:** use separate Bots with separate computers for different trust levels.
- **Sharing one Bot:** [mark personal sessions private](/docs/usage/conversations/#private-sessions) so other sessions cannot discover or read them through built-in APIs. (The Web administrator can still manage them, and files on a shared computer still need OS-level separation.)

<span id="know-where-data-goes"></span>

## Where does my data go?

| Data | Where it goes | What to know |
| --- | --- | --- |
| Messages, files the Bot reads, images, command output | **Your connected model service** | Files stay stored on your computer, while content needed for a turn is sent to the model provider (including for private sessions). Check your provider's data policy before using sensitive material. |
| Web search queries | **Your configured search service** | Sent only when the Bot runs a web search. |
| Ordinary group messages | **That group's session context** | Messages visible to the Bot may inform later replies even when proactive participation is off. Let group members know before connecting a shared chat. |

<span id="review-outside-content-and-skills"></span>

## Use care with third-party material and skills

Webpages, untrusted attachments, and external repositories can contain instructions trying to steer the Bot off course.

- Test unfamiliar material on copies first; inspect a third-party skill's source, `SKILL.md`, and bundled scripts before using it.
- Persona instructions, skills, and `AGENTS.md` guide behavior—rely on **computer authorization** and **OS or container isolation** to enforce access limits.

## If a task goes wrong

Act in this order (stopping or revoking access prevents future actions; changes already made to files or messages sent externally must be reverted separately):

1. Click **Stop** in the session and wait for it to finish stopping.
2. Check the tool activity for commands run, files changed, and messages sent; restore from backup if needed.
3. For chat-triggered tasks, disable the platform account to pause incoming messages (and stop any active work in its session).
4. Revoke the Bot's computer access or [disconnect the computer](/docs/computers/connect/#disconnect-and-stop-a-computer) if necessary.

## Protect sign-in details and backups

- **The Web account** administers the entire Astral instance, including private sessions—never share it with group members. For remote access, enable HTTPS per [Sign-in and security](/docs/hosting/security/).
- **API keys, platform tokens, recovery codes, and pairing commands** are secrets—scrub them before sharing screenshots or logs.
- **Complete backups** include chat history and keys used to recover saved credentials; store them like sensitive data.

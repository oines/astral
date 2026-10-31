---
title: "Use Astral safely"
description: "Choose an execution environment, limit group-chat access, and understand where data goes."
---

Astral can edit files, run commands, and act on connected chat platforms. Before giving it real work, decide which computers, accounts, and information belong within that Bot's reach.

## Use a separate environment for public groups

**We strongly recommend keeping your everyday computer out of a public group's Bot permissions.** Any member of an authorized group can request work. Astral authorizes chats; it has no separate list of members allowed to give commands, and computer commands do not require approval one by one.

Prepare the Bot as follows:

1. Create a dedicated Bot for the public group, separate from personal work.
2. Authorize only a [separate container](/docs/computers/containers/) or a dedicated machine. You can run the container on your own computer. Check the Bot's full computer-access list and remove access to your everyday machine: choosing a container as the default still lets the Bot switch to other authorized computers.
3. Provide only the files and credentials needed for the task. Avoid mounting your whole home directory, SSH keys, Astral's data directory, or the Docker socket into the container. Use read-only mounts for input that only needs to be read.
4. Keep the chat account on “Only specified chats” and add groups individually. Grant the platform account only the permissions its work requires.

Group-management actions use the connected platform Bot's identity. If that account can mute members, remove them, or publish notices, the Bot can use those permissions when carrying out requests. Astral does not separately authorize an action based on whether the requester is a group administrator. These platform permissions remain available when computer work runs in a container.

## Understand computer access

The Bot works as the operating-system account running the computer service. Tasks may use that account's files, network access, and configured tools. Prefer an ordinary account and keep production credentials and personal files outside its reach. Connecting as root gives commands root permissions.

The working directory sets where work begins; it does not restrict file access. Persona text, skills, and `AGENTS.md` guide behavior. Enforce access limits through computer permissions, operating-system accounts, file permissions, and container mounts.

## Separate personal sessions from shared chats

A Bot can use the built-in API to query its other non-private sessions and access its other authorized chats. Each chat has a separate session, while files on shared computers may still be accessible across sessions.

If personal work and a group use the same Bot, [make personal sessions private](/docs/usage/conversations/#private-sessions) to block discovery, reading, and changes through the API from other sessions. Private sessions still retain history, send context to the selected model service, and remain accessible through the web management account. Protect shared files and information already copied elsewhere separately.

For groups or users with different levels of trust, prefer separate Bots with their own computer permissions and execution environments.

## Know where data goes

The Server sends the context needed for a task to your selected model service. This can include messages, file contents, command output, and images. Search queries go to the selected search service. Choose services appropriate for the sensitivity of your data and review their data-use and retention settings.

An original file can stay on your computer while its contents are sent as model input. Private sessions follow the same rule.

Ordinary messages delivered by a connected group enter its context. Turning off proactive participation controls whether an idle Bot starts work on its own; those messages may still be used in a later response or an automatic summary. Let group members know before connecting it.

## Review outside content and skills

Websites, chat messages, attachments, and third-party project files can contain instructions intended to redirect the Bot. Before installing a skill or adopting a project's `AGENTS.md`, inspect its source, guidance, and included scripts. Try unfamiliar tools and content in a separate environment with a recoverable project copy.

Keep independent backups of important files. Before working with production data, have the Bot complete a small task on test data and inspect the result.

## Protect sign-in and credentials

The web account has management access to the whole Astral installation, including private sessions. Do not share it as a restricted account for group members. Use HTTPS for public access and configure [two-step verification or a Passkey](/docs/hosting/security/).

Protect API keys, platform tokens, recovery codes, and computer connection commands. Check logs, screenshots, and exported sessions for credentials and private content before reporting a problem. A full Server backup contains chats, identities, and the key needed to read saved credentials; store it as sensitive data.

## Stop unwanted work

Stop the affected session, then inspect file changes and platform actions that have already happened. Disabling a chat account prevents further incoming messages; stop any computer task already running separately.

To remove access, revoke the Bot's computer permission or the computer connection. Follow [Disconnect and stop a computer](/docs/computers/connect/#disconnect-and-stop-a-computer) to stop or uninstall a remote background service. Stopping work or revoking access does not undo earlier file changes or sent messages.

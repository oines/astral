---
title: "Troubleshooting"
description: "Locate common failures in sign-in, models, computers, chats, and files."
---

Identify the failing step, then check its connection and permissions. Keep the actual error, time, and affected object rather than only reporting “it does not work.”

## The page will not open or sign-in fails

- Fails on the Server itself: check the process, listener, and port.
- Works locally only: check networking, access addresses, remote management, and proxy forwarding.
- Sign-in not configured: [initialize the account](/docs/hosting/security/) from Desktop or the Server machine.
- Passkey unavailable: use its registered HTTPS hostname and a compatible browser.
- Password or second factor lost: use a recovery code or local account reset.

The former Owner Token and removed TLS startup flags do not repair the current login flow.

## Model connected, but tasks fail

Check that a specific model is selected, supports the required tools, and the Bot may use its current computer. A valid service connection, usable model, and online computer are distinct states.

Test custom endpoints from the Server machine, not only from your browser device.

## A computer stays offline

Check sleep state, background service, and whether its saved Server address is reachable. Changing the Server's advertised address does not rewrite existing computer connections.

Generate a fresh command when pairing expires. Distinguish installer-download failures from Server-connection failures. Regenerating a command cannot create unpublished release assets.

## Chat account connected, but no reply

Check account enabled state, chat authorization, session binding, and the Bot's model and computer. In a group, test with an explicit mention or reply.

Then check platform specifics: Telegram privacy mode or webhooks, QQ permissions and openids, initial incoming Weixin context, or Feishu publication and event permissions.

## Files cannot be opened or sent

Check whether the source computer is online, the file exists, and the Bot still has access. Creating a file in Astral and sending it to a platform are separate operations.

Read the actual send result. When uncertain, inspect the destination chat before retrying to avoid duplicates.

## Report a reproducible issue

Include Astral version, operating system, reproduction steps, expected and actual results, and redacted errors. For computers, include their type and version; for chats, include the platform and connection method. Omit credentials, passwords, recovery codes, and unnecessary personal conversation content.

Report confirmed software issues through [GitHub Issues](https://github.com/oines/astral/issues).

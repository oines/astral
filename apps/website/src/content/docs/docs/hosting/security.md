---
title: "Sign-in and security"
description: "Set up browser login, two-step verification, Passkeys, and account recovery."
---

Desktop uses its own local access identity and opens directly. Browsers and the CLI use a web account, managed separately.

## Set up web sign-in

In Desktop, use **Settings → Security & Access** to create a username and password. On a headless Server, run locally:

```sh
astral auth setup --data /path/to/server --username admin
```

Use the Server's actual data directory. Enter the password at the terminal prompt; it must be 12–1,024 bytes long. There is no first-visitor administrator registration page.

## Two-step verification and Passkeys

Adding and confirming an authenticator or Passkey enables two-step verification. Password login then requires an added verification method.

Passkeys require an actual HTTPS page and a supported browser. They belong to the hostname where they were registered; another hostname or HTTP URL may not work. Desktop opens the configured HTTPS page for setup.

“Allow Passkey passwordless sign-in” is a separate option, off by default, and requires at least one Passkey.

## Recovery codes and active logins

Generate recovery codes and store them safely. Each works once; generating a new set invalidates the old set.

Signed-in devices includes browser and CLI login sessions. The default lifetime is 30 days. Signing out all web sessions, changing the password, or resetting the account invalidates existing web logins without affecting Desktop identity or computer connections.

## Recover access

Use Desktop's security settings if it remains accessible. Otherwise, run on the Server machine:

```sh
astral auth reset --data /path/to/server --username admin
```

This resets web sign-in, removes authenticators and Passkeys, and revokes web sessions. Bots, conversations, and connected computers remain. There is no email password-recovery flow.

## Separate login from computer permission

Web login protects management access. Bot permissions control which computers execute tasks, under the computer service's operating-system account. Before connecting a group, make sure that Bot's file and tool access fits the group's intended work.

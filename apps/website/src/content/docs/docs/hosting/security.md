---
title: "Sign-in and security"
description: "Set up browser sign-in, add 2FA or Passkeys, restrict management hostnames, and recover locked accounts."
---

Browser access to Astral requires an administrator account. Because it controls every Bot, session (including private sessions), and computer, share it only with trusted administrators. Desktop opens directly without sign-in unless you enable browser access.

## 1. Set up web sign-in

- **Official Linux Server installer:** already created during installation—sign in directly.
- **Desktop app:** configure under **Settings → Security & access → Web sign-in**.
- **Standalone Server without credentials yet:** run on the server as the OS account running Astral (replace `admin`; add `--data /path/to/server-data` for custom directories):

```sh
astral auth setup --username admin
```

## 2. Add a second factor and save recovery codes

Under **Settings → Security & access → Sign-in protection**, add one or more verification methods:

| Method | Requirements |
| --- | --- |
| **Authenticator codes** | Any TOTP authenticator app on your phone |
| **Passkey** | A real [HTTPS domain](/docs/hosting/server/#use-an-https-reverse-proxy) and a Passkey-capable browser/device (Passkeys bind to the exact domain) |

:::tip[Save recovery codes right away]
Generate and store **recovery codes** offline so a lost phone won't lock you out. Each code works once; regenerating invalidates old codes. Use **Signed-in devices** anytime to revoke unfamiliar sessions.
:::

## Recover account access

| Situation | Next step |
| --- | --- |
| Forgot password | Reset the account below (Astral has no email reset) |
| Authenticator or Passkey unavailable | Sign in with a recovery code, or reset the account below |
| Returned to sign-in after editing credentials | Sign in with your new username or password |
| Page won't open or hostname rejected | Check [Troubleshooting](/docs/hosting/troubleshooting/#the-page-will-not-open-or-sign-in-fails) or **Hostname restriction** below |

If Desktop is accessible, recover web sign-in in its security settings. On a standalone Server, run as the Astral OS account:

```sh
astral auth reset --username admin
```

:::caution[What reset changes]
Resetting removes existing authenticators and Passkeys and signs out all web sessions—sign in with your new password and re-add 2FA afterward. Bots, histories, and connected computers remain intact.
:::

## Hostname restriction

To accept management traffic only on specific domains or IPs, enable **Restrict hostnames** under **Settings → Security & access**, enter bare hostnames/IPs only (e.g., `astral.example.com` for `https://astral.example.com:8443`—no scheme, port, path, or `*`), keep your current address in the list, and click **Save hostname settings**.

If a mistake triggers `403 unrecognized management host`, SSH into the server to inspect or disable the restriction (keeps your saved list and 2FA; takes effect immediately while running; add `--data` for custom dirs):

```sh
astral config hostnames show
astral config hostnames disable
```

If the command reports that flags or environment variables control the restriction, update your service configuration and restart. For Bot-level machine and file boundaries, see [Permissions and privacy](/docs/start/safe-use/).

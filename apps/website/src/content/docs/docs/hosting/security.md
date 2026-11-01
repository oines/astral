---
title: "Sign-in and account protection"
description: "Set up browser sign-in, add verification, or recover access to Astral."
---

Using Astral in a browser requires an administrator account. This account can manage every Bot, conversation, and computer, including private conversations. Share it only with trusted administrators.

The desktop app opens directly. Set up web sign-in when you want to access it from a browser.

## Set up web sign-in

**A Server installed with the official Linux installer** already has the username and password you chose during installation. You can sign in with those details.

**In Desktop**, open **Settings → Security & access → Web sign-in** and set a username and password.

If the browser says sign-in has not been set up and you use a standalone Server, open a terminal on the server as the OS account running Astral:

```sh
astral auth setup --username admin
```

Replace `admin` with your preferred username. Enter and confirm the password when prompted, then sign in through the browser. Password characters are hidden in the terminal.

The command finds existing installations. If there is more than one, select the intended one when prompted. For a manually configured data directory, add `--data /path/to/server-data`, matching the Server's startup `--data` path.

## Add a second verification step

Under **Settings → Security & access → Sign-in protection**, add a verification method:

| Method | What you need |
| --- | --- |
| Authenticator codes | An authenticator app on your phone to generate login codes |
| Passkey | An actual HTTPS page and a browser or device that supports Passkeys |

Once you add and confirm a method, password sign-in requires a second verification step. You can add more than one method so you have an alternative when a device is unavailable.

A Passkey belongs to the domain where it was added. Use the same HTTPS address to sign in; if you change domains, add a Passkey at the new address. If Desktop's embedded page cannot configure it, open your [HTTPS address](/docs/hosting/server/#use-an-https-reverse-proxy) in a browser first.

## Recovery codes and active logins

After enabling sign-in protection, generate and save recovery codes in security settings. They can help you sign in if your phone is lost or your authenticator is unavailable. Each code works once; generating a new set invalidates the old set.

Use **Signed-in devices** to sign out a browser or command-line login. Remove any login you do not recognize. **Sign out all devices** also signs out the browser you are using.

## Recover account access

Start with the situation you see:

| What happened | What to do |
| --- | --- |
| You forgot the password | Reset the account using the steps below |
| Your authenticator or Passkey is unavailable | Try a saved recovery code; reset the account if no method is available |
| Changing your username or password returned you to sign-in | Sign in with the new details |
| The page does not open, or its hostname is rejected | Use [connection troubleshooting](/docs/hosting/troubleshooting/#the-page-will-not-open-or-sign-in-fails) or the hostname steps below |

If you can still open Desktop, recover web sign-in from its security settings. For a standalone Server, run this in a server terminal as the OS account running Astral:

```sh
astral auth reset --username admin
```

Replace `admin` with the username you want after the reset, then enter a new password. Add `--data /path/to/server-data` for a custom installation when needed.

**Reset removes existing authenticators and Passkeys and signs out existing web logins.** Sign in with the new details, then add your verification methods again. Bots, history, and connected computers remain. Astral does not have an email password-recovery flow.

## Hostname restriction

To allow management access only through particular domains or IP addresses, open **Settings → Security & access**, enable **Restrict hostnames**, edit the list, and select **Save hostname settings**.

For example, if the address is `https://astral.example.com:8443`, enter `astral.example.com`. Use only domains or IP addresses, without a scheme, port, path, or `*`. Keep the domain or IP you are currently using so you retain access.

A fresh Server has this restriction turned off. Enabling it does not replace account sign-in.

If a configuration change causes `403 unrecognized management host`, SSH into the server and inspect the setting:

```sh
astral config hostnames show
```

To restore browser access, turn off hostname restriction:

```sh
astral config hostnames disable
```

This retains the list and sign-in protection. It applies immediately while the service is running, or on the next start while stopped. If the command reports that a startup argument or environment variable controls the restriction, update that service configuration and restart. Add `--data /path/to/server-data` for a custom directory.

For control over the computers and files a Bot can use, see [Permissions and safe use](/docs/start/safe-use/).

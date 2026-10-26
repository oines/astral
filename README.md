<h1 align="center">
  <picture>
    <source media="(prefers-color-scheme: dark)" srcset="packages/icons/assets/astral-lockup-inverse.svg">
    <img src="packages/icons/assets/astral-lockup.svg" alt="Astral" width="260" height="80">
  </picture>
</h1>

<p align="center"><strong>One agent. All your computers.</strong></p>
<p align="center">English · <a href="README.zh-CN.md">简体中文</a></p>

Astral is an open-source, self-hosted personal AI agent. It can work across your laptop, desktop and servers in a single session, reading files, running commands and using the tools you already have.

Use Astral from the desktop app or a browser, or connect it to your chat apps to give it tasks in private and group conversations.

## One session, across computers

Read the project notes on your MacBook, run tests on a Linux server, then check the build on your Windows desktop. The whole task stays in one session, with its context, execution history and results together.

Each computer provides its own files and environment. Astral can switch between them as the task requires; files and running processes stay on their source computer.

## Bring Astral into your chats

Give it a task in Telegram, QQ, WeChat or Feishu. Let it use your computers to get the work done and send the results back to the chat.

In a group, members can request work, add details and follow up together. Mentions and replies wake Astral by default. You can also enable proactive participation so it can decide when to join a discussion based on the messages it receives.

Each chat has its own session, which you can also open and continue in Astral. Telegram, QQ and Feishu support groups; WeChat currently supports private chats only. See the [chat platform setup guide](https://astral.oines.dev/docs/chat/overview/).

## Use the tools you already have

Command-line tools, scripts, project files and existing software environments are available to Astral on the computers you connect. Organize research, write a report, process files or work on a codebase, using the tools that fit the task.

## Run it on your own devices

Use the desktop app on macOS, Windows or Linux, or run Astral Server on your own hardware and access it through a browser. Connect your preferred model service and choose which computers and chats each Bot can use.

## Get started

**Astral is pre-release software. Public installers are not yet available.** You can build and run it from source; follow the [installation guide](https://astral.oines.dev/docs/start/get-astral/).

Install the development tools pinned in [mise.toml](mise.toml), then:

```sh
git clone https://github.com/oines/astral.git
cd astral
pnpm install --frozen-lockfile
GOWORK=off pnpm build:distribution
```

Choose how to run it:

- **Desktop:** [build a package for your platform](https://astral.oines.dev/docs/start/get-astral/). The app manages its local Server and Computer.
- **Server:** [start the built binary and set up your Web account](https://astral.oines.dev/docs/hosting/server/). See [deployment and operations](https://astral.oines.dev/docs/hosting/server/) for a persistent installation and remote access.

On first launch:

1. Connect a model service and select a model.
2. Select or connect a computer, then grant the default **Astral** Bot access.
3. Start a conversation and describe what you want done.

To use it from a chat app, open **Settings → Bots**, choose your Bot, then **Connect a platform**. You can add more computers and adjust each Bot’s access later.

## Documentation

Read the [website and user documentation](https://astral.oines.dev/). Their source is maintained in `apps/website/`.

| Guide | Covers |
| --- | --- |
| [Get started](https://astral.oines.dev/docs/start/first-steps/) | Model setup and your first task |
| [Connect computers](https://astral.oines.dev/docs/computers/connect/) | Connections, Bot permissions, and working across machines |
| [Use Astral](https://astral.oines.dev/docs/usage/conversations/) | Sessions, files, models, and skills |
| [Chat platforms](https://astral.oines.dev/docs/chat/overview/) | Private chats, groups, and platform setup |
| [Self-hosting](https://astral.oines.dev/docs/hosting/server/) | Server startup, web sign-in, and network access |
| [Updates and backups](https://astral.oines.dev/docs/hosting/maintenance/) | Preserve history and work files |

## Development

After installing dependencies, start the backend:

```sh
GOWORK=off pnpm dev:server
```

In another terminal, start the Web app:

```sh
pnpm dev
```

See the [development guide](docs/development.md#development-and-verification) for desktop development, the local model fixture and verification commands. Contribution guidelines are in [CONTRIBUTING.md](CONTRIBUTING.md); UI work follows the [UI/UX specification](docs/ui-ux-spec.md). Report suspected vulnerabilities privately through [SECURITY.md](SECURITY.md).

Astral uses [Astral Core](https://github.com/oines/astral-core) for its agent runtime and [astral-models](https://github.com/oines/astral-models) for model provider protocols.

## License

Copyright (C) 2026 oines and Astral contributors.

Licensed under the [GNU Affero General Public License, version 3 only](LICENSE) (`AGPL-3.0-only`), without any warranty. Third-party components retain their own licenses; see [THIRD_PARTY.md](THIRD_PARTY.md) and the notices included with distributions.

---
title: "Updates and backups"
description: "Update the Server and computer services while preserving history and work files."
---

Before updating, identify the current version, data locations, and running tasks. Pre-release versions may change data formats; do not assume migration or rollback is automatic.

## Update Desktop or Server

The local computer service updates with Desktop or the standalone Server. Desktop automatic updates are not implemented yet; replace the program using actual release assets or a source build, then restart.

Keep the existing data directories and startup options. A new executable should not accidentally start against an empty `--data` directory.

## Update remote computers and containers

Open **Settings → Computers → computer details → Daemon maintenance** and use the displayed update command.

- Remote computer: run it on that machine to update and restart its user service.
- Container: run it on the host to update and restart the same container, preserving environment and data.

Running tasks are interrupted. Computer identity and data remain. Updating a container's Daemon does not replace its base image.

## What to back up

| Component | Preserve |
| --- | --- |
| Server | The full data directory, including databases, `master.key`, and `computer-identity/` |
| Local and remote computers | Service state, Bot work directories, and actual project files |
| Containers | Data volumes and any required writable-layer state or rebuild instructions |

Database files alone are insufficient. The encryption key is needed for saved credentials; the computer identity preserves existing computers' trust in the Server.

The simplest consistent backup is to stop Desktop or Server before copying its complete data directory. Live backups require a consistent SQLite backup procedure, not an arbitrary copy of an active database.

## Locate the data

Check startup logs, computer details, and launch arguments first. Default roots are:

- macOS: `~/Library/Application Support/astral`
- Windows: `%APPDATA%\astral`
- Linux: `$XDG_CONFIG_HOME/astral`, normally `~/.config/astral`

Server data normally uses `server/` and local computer data uses `computers/` beneath that root. Explicit paths and environment settings override defaults.

## Verify recovery

Restore with a matching version, then check history, model connections, computer status, and chat accounts before resuming work. Do not run two Servers against the same data directory.

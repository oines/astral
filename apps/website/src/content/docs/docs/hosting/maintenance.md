---
title: "Updates and backups"
description: "Update Desktop, Server and connected computers while preserving history and work files."
---

Keep your existing data directories when replacing a program. Download availability is listed under [Get Astral](/docs/start/get-astral/); the controls below apply to builds that include the updater and migration tools. An older build without the updater needs one manual installation before it can update in the App.

## Update Desktop

Open **Settings → About & diagnostics**. Supported Desktop installations check for new versions after launch and periodically while running, then download in the background. You can also select **Check for updates**. When the download finishes, select **Restart and update**.

| Installation | Update method |
| --- | --- |
| Official signed macOS App | In-App download, then restart and update |
| Windows EXE installer for the current user | In-App download, then restart and update |
| Linux AppImage | In-App download, then restart and update |
| Linux DEB, Windows ZIP, unsigned Mac or development build | Download and install the newer package manually |

Downloading does not stop your work. Installing stops the local Server and interrupts its running tasks and computer commands. Ordinary quit does not install a downloaded update. The local computer service updates with Desktop; remote computers have their own update action.

If a Mac release says Apple notarization is pending, its Developer ID signature is verified but Gatekeeper may still block the first launch. After trying to open the trusted official download, follow [Apple's instructions](https://support.apple.com/en-us/102445) to allow it under **System Settings → Privacy & Security → Open Anyway**. This is separate from Astral's update controls.

## Update a headless Server

For a Linux Server installed with the official current-user installer, run the [installation command](/docs/hosting/server/) again, optionally selecting a published version tag. Keep the existing installation root and network configuration. The installer validates the download, stops the user service and starts the new program against the existing data.

For a Server you manage directly, stop it, replace the program and restart with the same `--data` directory and startup options. Updating the standalone Server also updates its local computer service. Do not point the replacement program at an empty directory by accident.

## Skipping versions and data changes

You only download the latest application. It includes all supported data migrations and applies any missing steps in order; intermediate application versions do not need to be downloaded or installed.

When a data conversion is needed, startup first takes exclusive ownership and backs up the complete Server directory. It keeps recovery information while migrating and preparing its databases, then commits before accepting browser, API or chat work. Migration or preparation failure restores that snapshot, and an interrupted pre-commit attempt is recovered at the next startup. A later failure does not automatically restore old data after new work may have been accepted. The Linux installer separately restores this attempt's snapshot if its activation check fails; Desktop does not automatically revert the App for every startup failure. These snapshots are in `upgrade-backups/` beside the Server directory.

The current supported data baseline is Host 3 / Core 11. This model and session refactor does not convert Core 10 or earlier data. Desktop starts with first-time setup in its new `schema-3-core-11/` directory and preserves older directories, including `schema-3/`, untouched. Older private development formats and unknown newer formats are rejected when explicitly selected. Installing an old executable is not a data downgrade: restoring older data requires a matching backup and program version. Keep independent backups, including Computer files, rather than relying only on upgrade snapshots.

## Update remote computers and containers

Open **Settings → Computers → computer details → Daemon maintenance**. For an online managed computer, select **Update Daemon** and confirm that running tasks will stop. Astral downloads and verifies the official version, replaces the program and checks that the same computer reconnects. If its Daemon is newer than the Server, Astral reminds you to update Desktop or Server; this does not block checking for a newer Daemon. The action checks the latest release even when the current versions match, and skips an equal or older target.

You can also copy the displayed update command. Use this route for older Daemons without the management action, or when the action is unavailable:

- Native computer: run the shell or PowerShell command on that machine to update its current-user service.
- Container: run the command on its Docker host. It replaces the Daemon and restarts the same container, preserving its identity, installed tools, writable layer and data.

Managed updates in new containers use their in-container supervisor and restart the Daemon without recreating the environment. Updating a container's Daemon does not change its base image. Failed updates attempt to restore the previous program; check the reported state and computer connection before resuming work.

## What to back up

| Component | Preserve |
| --- | --- |
| Server | The full data directory, including databases, `master.key`, `computer-identity/` and configuration |
| Local and remote computers | Service state, Bot work directories and actual project files |
| Containers | Data volumes and any required writable-layer state or rebuild instructions |

Database files alone are insufficient. The encryption key is needed for saved credentials; the computer identity preserves existing computers' trust in the Server. Server migration backups do not copy work files from connected computers or undo external actions.

The simplest consistent backup is to stop Desktop or Server before copying its complete data directory. Live backups require a consistent SQLite backup procedure, not an arbitrary copy of an active database.

## Locate the data

Check startup logs, computer details and launch arguments first. Standalone defaults use these Astral roots:

- macOS: `~/Library/Application Support/astral`
- Windows: `%APPDATA%\astral`
- Linux: `$XDG_CONFIG_HOME/astral`, normally `~/.config/astral`

Server data normally uses `server/` and local computer data uses `computers/` beneath that root. Desktop uses a `schema-3-core-11/` subdirectory in its OS application-data root; on macOS and Windows these are the Astral locations above, and on Linux this is normally `~/.config/astral/schema-3-core-11/`. The Linux VPS installer instead keeps them beneath its installation root's `data/`. Explicit paths and environment settings override defaults.

## Verify recovery

Stop Desktop or Server before restoring. Use a program matching the restored data, then check history, saved model connections, computer status and chat accounts before resuming work. Do not run two Servers against the same data directory.

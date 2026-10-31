---
title: "Updates and backups"
description: "Update Desktop, Server and connected computers while preserving history and work files."
---

Keep your existing data directories when replacing a program. Download availability is listed under [Get Astral](/docs/start/get-astral/); the controls below apply to builds that include the updater and migration tools. An older build without the updater needs one manual installation before it can update in the App.

## Update Desktop

Open **Settings → About & diagnostics** in the App or its signed-in Web UI and select **Check for updates**. When a newer version is available, the button becomes **Update and restart**. Supported Desktop installations also check periodically.

Selecting **Update and restart** checks the latest release, downloads and verifies it, then installs and restarts. The Web UI’s button updates the same running Desktop App.

| Installation | Update method |
| --- | --- |
| Official signed macOS App | Select **Update and restart** in the App or its signed-in Web UI |
| Windows EXE installer for the current user | Select **Update and restart** in the App or its signed-in Web UI |
| Linux AppImage | Select **Update and restart** in the App or its signed-in Web UI |
| Linux DEB, Windows ZIP, unsigned Mac or development build | Download and install the newer package manually |

You can keep using Astral during the download. Installation and restart interrupt running tasks and computer commands, so update when you are ready for that interruption. The local computer service updates with Desktop; remote computers are updated separately.

Wait for the page to reconnect and show the update result. If it fails, follow the error and retry. If Astral returns to the previous version, inspect the error before continuing work.

If a Mac release says Apple notarization is pending, its Developer ID signature is verified but Gatekeeper may still block the first launch. After trying to open the trusted official download, follow [Apple's instructions](https://support.apple.com/en-us/102445) to allow it under **System Settings → Privacy & Security → Open Anyway**. This is separate from Astral's update controls.

## Update a headless Server

An official Linux current-user service with verified installation metadata supports the same update row in **Settings → About & diagnostics**. Check for a new version, then select **Update and restart** when you are ready to interrupt current tasks. The browser does not need access to a shell.

Updates retain the installation directory, data, and network settings. Once the download is verified, the Server restarts and applies any required data conversion. If activation fails, the installer attempts to restore the previous program, service, and this update’s data backup. Reconnect and check the result before resuming work. Update records are stored in `updates/` beneath the installation root.

If an older official installation only offers manual downloads, rerun the [installation command](/docs/hosting/server/) as the same user with the same installation directory, then check again. The command also remains available for updates to a specific published version.

For a foreground `serve` process, custom service, or self-managed container, stop it, replace the program, and restart with the same `--data` directory and startup options. Its local computer service updates with the Server.

## Skipping versions and data changes

Upgrade directly from a supported earlier version to the latest release. You do not need to install intermediate versions. Keep an independent backup before updating.

When data conversion is needed, Astral first backs up the full Server directory. A failed conversion restores that backup; after an interruption, the next launch recovers it before continuing. Once the Server starts successfully and accepts new work, later failures need to be handled individually rather than automatically replacing new data with the old backup. Automatic backups are in `upgrade-backups/` beside the Server directory.

Desktop releases from 0.1.0 onward keep the existing data directory, setup, history, and computer identities. Earlier private-development data is left in place and is not imported automatically. To return to an older version, use a data backup matching that version.

## Storage maintenance

A large database conversion needs additional space for backups and temporary files. If free space is insufficient, free some space or stop Astral and move its complete data directory to a suitable disk, then retry.

The startup preparation window shows the current stage. Close it to cancel startup; the next launch recovers the interrupted conversion. If an old installer times out on a large database, retry with the current installer and the same installation directory.

### Review and clean storage

Open **Settings → Storage** in Desktop or its signed-in Web UI. The overview shows disk capacity and Astral file usage, refreshing on entry and after cleanup. Manage project and work files on their own computers.

Select a Bot to browse **Chats** and **Other sessions** and compare their content sizes. List sizes are estimates; the overview shows measured disk use. Shared content and unused database space affect how much a deletion eventually frees.

Chat archive cleanup, archive retention and clearing a chat's current session are separate actions. See [Manage session and chat storage](/docs/usage/conversations/#manage-session-and-chat-storage) for their effects and the prerequisites for permanently deleting other sessions.

**Application cleanup** lists identified upgrade backups, model-catalog caches, old temporary files, and old update packages from supported Desktop installations. Files needed for an active update or recovery show why they must be kept.

Select the items and confirm **Remove selected files**. The list and usage update after cleanup. If a file has changed or removal fails, follow that item’s refresh or retry action.

Removing a backup permanently removes that recovery copy. Existing backups can still contain history that you subsequently deleted; ordinary session or archive cleanup does not rewrite them. Keep an independent backup suitable for your recovery needs.

### Automatic database reclamation

After deleting sessions or clearing records, Astral compacts its databases in the background. Disk space may be released later; you do not need to stop the Server. Operations may briefly wait while a large database is compacted.

The page updates measured usage afterward. If reclamation fails, it shows the cause and a retry action; deleted records remain deleted. If it reports insufficient space, free the temporary space needed for compaction first.

### Offline Core maintenance

The separate command-line alternative reclaims unused Core database pages. Keep a complete backup and stop Desktop or Server before running it:

```sh
astral maintenance vacuum --data /path/to/server
```

This command compacts only the Core database, keeps session history and upgrade backups, and reports file size before and after. Keep Desktop and Server stopped while it runs.

## Update remote computers and containers

Open **Settings → Computers → computer details → Daemon maintenance**. For an online managed computer, select **Update Daemon** and confirm that running tasks will stop. Astral downloads and verifies the official version, replaces the program and checks that the same computer reconnects. If its Daemon is newer than the Server, Astral reminds you to update Desktop or Server; this does not block checking for a newer Daemon. The action checks the latest release even when the current versions match, and skips an equal or older target.

You can also copy the displayed update command. Use this route for older Daemons without the management action, or when the action is unavailable:

- Native computer: run the shell or PowerShell command on that machine to update its current-user service.
- Container: run the command on its Docker host. It replaces the Daemon and restarts the same container, preserving its identity, installed tools, writable layer and data.

Container updates keep the environment, installed tools, and data. A failed update attempts to restore the previous program; check the result and connection before resuming work. If a release requires a new supervisor, restart the same container after updating.

Base environment images are used for new containers. Use **Update Daemon** to update Astral inside an existing container. To change the base environment, back up work files before creating a new container. See [Use containers](/docs/computers/containers/).

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

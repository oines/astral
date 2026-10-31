---
title: "Updates and backups"
description: "Update Desktop, Server and connected computers while preserving history and work files."
---

Keep your existing data directories when replacing a program. Download availability is listed under [Get Astral](/docs/start/get-astral/); the controls below apply to builds that include the updater and migration tools. An older build without the updater needs one manual installation before it can update in the App.

## Update Desktop

Open **Settings → About & diagnostics** in the App or when signed in to its Server in a browser. The software update row has one button: **Check for updates**, **Update and restart** when a newer version is available, or the appropriate retry action after an error. Supported Desktop installations also check for version metadata after launch and periodically while running. Checking never downloads an installer.

Selecting **Update and restart** checks the latest release again, downloads and verifies the complete update, then installs it and restarts. A browser request goes to the running Desktop App's update manager; it does not install a separate Server over the App.

| Installation | Update method |
| --- | --- |
| Official signed macOS App | Select **Update and restart** in the App or its signed-in Web UI |
| Windows EXE installer for the current user | Select **Update and restart** in the App or its signed-in Web UI |
| Linux AppImage | Select **Update and restart** in the App or its signed-in Web UI |
| Linux DEB, Windows ZIP, unsigned Mac or development build | Download and install the newer package manually |

You can keep using Astral while the update downloads. When the download finishes, installation stops the local Server and interrupts its running tasks and computer commands. Choose **Update and restart** when you are ready for that interruption. Ordinary quit does not install an update. If a check fails, retry the check; if an update fails, retry the update. Checking never starts a download or installation. The local computer service updates with Desktop; remote computers have their own update action.

The Web UI may disconnect during the restart. It waits for the Server to return and verifies the result of that update operation against the running version. A lost connection alone is not an update failure or a reason to submit it again. A failed update or a return to the previous version remains an error rather than a successful upgrade.

If a Mac release says Apple notarization is pending, its Developer ID signature is verified but Gatekeeper may still block the first launch. After trying to open the trusted official download, follow [Apple's instructions](https://support.apple.com/en-us/102445) to allow it under **System Settings → Privacy & Security → Open Anyway**. This is separate from Astral's update controls.

## Update a headless Server

An official Linux current-user service with verified installation metadata supports the same update row in **Settings → About & diagnostics**. Check for a new version, then select **Update and restart** when you are ready to interrupt current tasks. The browser does not need access to a shell.

An independent, visible systemd user job downloads the selected official release and verifies it before stopping the Server service. It keeps the same installation root, data and network settings, starts the new Server and lets its startup migration run. If activation fails, the installer finishes restoring the previous program, service and this attempt's data snapshot before the job records failure. Its operation record lives under the installation root's `updates/`, outside the Server data backup, so a rollback does not erase the update result. Reconnect to confirm the result before resuming work.

If an older official installation offers only manual downloads, rerun the [installation command](/docs/hosting/server/) once as the same user with the same root to prepare the current installation metadata and service definition. This remains the command-line update alternative; a published tag can be selected explicitly. An installation-root setting alone cannot authorize updates for a custom service.

For a foreground `serve` process, custom service or container deployment you manage directly, update through that deployment's own process: stop it, replace the program and restart with the same `--data` directory and startup options. These installations do not promise Web-triggered replacement. Updating the standalone Server also updates its local computer service. Do not point the replacement program at an empty directory by accident.

## Skipping versions and data changes

You only download the latest application. It includes all supported data migrations and applies any missing steps in order; intermediate application versions do not need to be downloaded or installed.

When a data conversion is needed, startup first takes exclusive ownership and backs up the complete Server directory. It keeps recovery information while migrating and preparing its databases, then commits before accepting browser, API or chat work. Migration or preparation failure restores that snapshot, and an interrupted pre-commit attempt is recovered at the next startup. A later failure does not automatically restore old data after new work may have been accepted. The Linux installer separately restores this attempt's snapshot if its activation check fails; Desktop does not automatically revert the App for every startup failure. These snapshots are in `upgrade-backups/` beside the Server directory.

The first supported public data baseline is Host 3 / Core 11; the current target is Host 6 / Core 11. Desktop keeps the same `schema-3-core-11/` directory when upgrading from 0.1.0 or later, preserving existing setup, history and computer identities. Missing migrations run in order: step 4 restores visibility of Sessions archived while bound to IM; step 5 initializes existing Sessions as non-private; step 6 unifies stored outgoing-message content and preserves send outcomes and idempotency. It also preserves the original activity order for Sessions only changed by private-setting initialization. Earlier Session revisions, IM events and Core history remain intact; ordinary archived Sessions stay archived.

Earlier private Core 10 or older data is not imported; those directories, including `schema-3/`, remain untouched. Older private development formats and unknown newer formats are rejected when explicitly selected. Installing an old executable is not a data downgrade: restoring older data requires a matching backup and program version. Keep independent backups, including Computer files, rather than relying only on upgrade snapshots.

## Storage maintenance

Astral 0.1.8 adds Core SQLite schema 12 while keeping Core logical format 11 and the existing Desktop data directory. Its automatic migration shares repeated large content and replaces input/queue copies with event references. Messages, timestamps, attachments already present in history and model-request boundaries retain their logical values; media is not fetched again.

Before conversion, Astral checks free space for the complete backup, database work, temporary files and recovery. A smaller final database does not mean the migration needs less working space. If preflight fails, free space or move the complete stopped Server directory to a suitable volume, then retry. Completed upgrade backups are retained.

Desktop displays a data-preparation window if startup maintenance takes more than 300ms. It waits while the matching Server process prepares data; close the window to cancel. A cancelled or interrupted conversion is recovered on the next launch. The updated Linux installer also waits for data preparation instead of applying its ordinary service-start timeout. An older installer already running keeps its old timeout; use the offline migration command when upgrading a large database through that route.

### Review and clean storage

Open **Settings → Storage** in Desktop or its signed-in Web UI. The overview shows the data volume's capacity and measured Astral files, including database sidecar files, upgrade backups and identified cache or temporary files. The application database contains IM archives as well as settings and other application records. Files on connected computers are separate.

Below the overview, select a Bot to see its **Chats** and **Other sessions**. A chat contains its IM archive and its currently bound Astral session; previous sessions that were unbound remain under Other sessions. The size bars in these lists are logical estimates of archived content, session records, state and model attempts. They are not physical disk ownership or the space a deletion will free. Shared content, indexes and unused database pages make these estimates differ from file sizes. Refresh the estimate when needed; a partial or failed estimate remains labeled.

Chat archive cleanup, archive retention and clearing a chat's current session are separate actions. See [Manage session and chat storage](/docs/usage/conversations/#manage-session-and-chat-storage) for their effects and the prerequisites for permanently deleting other sessions.

**Application cleanup** lists files Astral can identify and attribute to this Server: eligible upgrade backups, its model-directory cache and recognized old temporary files. Packaged Desktop installations also list old update packages when their updater configuration and download metadata establish ownership. Pending newer updates, packages needed for recovery and updates being downloaded or installed are protected. Unidentified cache files are left out; a standalone Server does not claim access to Desktop's cache.

Select the items to remove and confirm **Remove selected files**. Latest recovery points and snapshots needed by an active upgrade or recovery are protected. A changed file or recovery dependency requires refreshing the inventory before removal. This control does not remove unknown logs or computer work files.

Removing a backup permanently removes that recovery copy. Existing backups can still contain history that you subsequently deleted; ordinary session or archive cleanup does not rewrite them. Keep an independent backup suitable for your recovery needs.

### Automatic database reclamation

After session deletion or cleanup, Astral queues background disk-space reclamation and waits for an idle opportunity. This works in the running Desktop Server and a foreground `serve` deployment; you do not have to stop the Server yourself. The job compacts the Core, application and existing recall databases with full VACUUM and completes their WAL checkpoints. This operation adds no schema migration.

Full compaction needs free working space and can temporarily delay storage requests. Deleting content does not promise an immediate reduction in file size. The page distinguishes queued, waiting, running and failed reclamation; a busy database is retried later, and other failures offer a retry after their cause is addressed. A reclamation failure does not reverse completed cleanup. Disk usage is measured again after maintenance, rather than inferred from the logical size of deleted records.

### Offline Core maintenance

The separate command-line alternative reclaims unused Core database pages. Keep a complete backup and stop Desktop or Server before running it:

```sh
astral maintenance vacuum --data /path/to/server
```

This offline operation checks free space and database integrity. It handles only the Core database, reports its main file size before and after, and retains all upgrade snapshots. Reclaiming database pages does not remove backups or change conversation history. Do not use this command while another program is using this Server data directory.

## Update remote computers and containers

Open **Settings → Computers → computer details → Daemon maintenance**. For an online managed computer, select **Update Daemon** and confirm that running tasks will stop. Astral downloads and verifies the official version, replaces the program and checks that the same computer reconnects. If its Daemon is newer than the Server, Astral reminds you to update Desktop or Server; this does not block checking for a newer Daemon. The action checks the latest release even when the current versions match, and skips an equal or older target.

You can also copy the displayed update command. Use this route for older Daemons without the management action, or when the action is unavailable:

- Native computer: run the shell or PowerShell command on that machine to update its current-user service.
- Container: run the command on its Docker host. It replaces the Daemon and restarts the same container, preserving its identity, installed tools, writable layer and data.

Managed updates in new containers use their in-container supervisor and restart the Daemon without recreating the environment. Updating a container's Daemon does not change its base image. Failed updates attempt to restore the previous program and may restart that version; check the reported state and computer connection before resuming work. When a release changes the supervisor itself, restart the same container once after updating to load those changes; you do not need to recreate the environment.

The prebuilt Ubuntu environment is versioned independently from the application. New containers pull the exact image digest selected by that Astral version and receive the Daemon separately. An application update neither rebuilds that image on your computer nor recreates existing containers. Their original image, writable layer, installed tools, data volume and pairing mount remain in place; use the separate Daemon action when its program needs updating. See [Use containers](/docs/computers/containers/) for preparation and persistence.

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

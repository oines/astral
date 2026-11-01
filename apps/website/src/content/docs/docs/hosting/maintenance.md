---
title: "Updates and backups"
description: "Update Astral, back up your account and work, and restore them when needed."
---

Keep the same data directories when updating Astral. Your account, conversations, and computer identities live there; replacing the program does not require recreating them. Keep an independent backup before important work. Updates and recovery restart services, so finish running tasks first.

## Update Desktop

Check for updates in **Settings → About & diagnostics**, then follow the page to install and restart. Official macOS builds, Windows EXE installations, and Linux AppImage support in-app updates.

For Linux DEB, Windows ZIP, or a custom build, get the new version from [Downloads](/docs/start/get-astral/), replace the program, and retain its data. When accessing Desktop through a browser, the web update action updates the desktop app on that computer.

## Update Server

Official Linux installations can update through **Settings → About & diagnostics**. Alternatively, rerun the [installer](/docs/hosting/server/#update) as the original OS account. Keep the original `--root` for a custom directory.

Wait for the service to restart and reconnect, then check the version and task history. Retry download errors as directed. If activation fails, the installer attempts to restore the previous program and that update's data backup. An older installation offering only manual updates can first be updated with the current installer.

For manual `serve`, a custom service, or a custom container, stop it using your deployment method, replace the program, and restart with the **same data directory and startup configuration**.

## Update remote computers and containers

Open the computer under **Settings → Computers** and use the update action in **Daemon maintenance**. The Daemon is the Astral connection program installed on that computer. Updating interrupts its running tasks; afterward, the same computer should reconnect.

If the button is unavailable, use the complete update command provided in its details. Run native-computer commands on that computer and container commands on its Docker host. The server machine's connection program updates with Server or Desktop.

Updating Astral inside a container retains the container, files, and installed software. To replace its base environment, back up your work and create a new container.

## What to back up

Astral's records and the files a Bot works on may be on different computers:

| What to keep | Back up |
| --- | --- |
| Account, Bots, model connections, conversation history, and computer trust | The complete Server data directory, including databases, `master.key`, `computer-identity/`, and configuration |
| Work on a local or remote computer | That computer's Astral data, Bot working directories, and actual projects or files used by tasks |
| Work inside a container | Data volumes; preserve the writable layer or a rebuild method for installed software and other changes |

Copying the full Server directory preserves the key needed to read saved credentials and the Server identity trusted by connected computers. Store this sensitive backup where only you can access it.

### Back up an official Linux installation

This example uses the default installation. Run the steps in the same terminal as the OS account running Astral. If you used `--root`, set `astral_install_root` to that original absolute path.

**Stop the service and verify that it stopped:**

```sh
systemctl --user stop astral-server.service
systemctl --user show astral-server.service --property=ActiveState,MainPID
```

Continue only when the state is `inactive` or `failed` and `MainPID=0`. This avoids copying a database while it is still being written.

**Copy the complete `data/` directory and record the version and service configuration:**

```sh
astral_install_root="${XDG_DATA_HOME:-$HOME/.local/share}/astral-server"
astral_backup_dir="$HOME/astral-backups/$(date +%Y%m%d-%H%M%S)"
mkdir -p "$astral_backup_dir"
chmod 700 "$astral_backup_dir"
cp -a "$astral_install_root/data" "$astral_backup_dir/data"
cp -a "$astral_install_root/installation.json" "$astral_backup_dir/installation.json"
cp -a "${XDG_CONFIG_HOME:-$HOME/.config}/systemd/user/astral-server.service" "$astral_backup_dir/astral-server.service"
astral version > "$astral_backup_dir/program-version.txt"
echo "$astral_backup_dir"
```

The last line prints the backup location. It includes Server data, local computer data, and automatic update backups. Back up remote computer files and Docker volumes separately. Copy the backup to another disk or your backup service to protect against failure of the original disk.

**Start the service again after a successful copy:**

```sh
systemctl --user start astral-server.service
```

For Desktop and manual deployments, quit the application completely or stop the process before copying their actual data directories. Live backups require a consistent backup mechanism rather than copying an active database.

## Locate the data

For custom paths, use your actual startup options and configuration. Defaults are:

| Installation | Server and local computer data |
| --- | --- |
| Official Linux installer | `${XDG_DATA_HOME:-$HOME/.local/share}/astral-server/data/server/` and sibling `computers/` |
| macOS Desktop | `server/` and `computers/` under `~/Library/Application Support/astral/schema-3-core-11/` |
| Windows Desktop | `server\` and `computers\` under `%APPDATA%\astral\schema-3-core-11\` |
| Linux Desktop | Usually `server/` and `computers/` under `~/.config/astral/schema-3-core-11/` |
| Manual `serve` | `server/` and `computers/` under the system Astral configuration directory, or your explicit paths |

For manual operation, Astral's configuration directory is `~/Library/Application Support/astral` on macOS, `%APPDATA%\astral` on Windows, and `${XDG_CONFIG_HOME:-$HOME/.config}/astral` on Linux.

When a recovery command needs `--data`, supply the **Server data directory** from the table. For the official Linux installation, this ends in `…/data/server/`.

## Check after restoring

These steps restore your own complete backup under the **original OS account and original paths**. Check the recorded program version and use a matching program. Moving to another machine or installation path requires a separate deployment plan; moving data while retaining the old startup path is incomplete.

1. Quit Desktop or stop Server. For an official Linux installation, use the `stop` and `show` commands above and confirm `MainPID=0`.
2. Rename the current data directory to keep it, then restore the backup to the original location, preserving ownership and access permissions. Do not merge the old and restored directory contents. For the Linux backup above, restore its `data/` as `data/` beneath the original installation root—not inside `data/server/`.
3. Check that the startup configuration still points to this installation. Restore work files or Docker volumes where needed; a Server backup does not retrieve files from other computers.
4. Start Astral. Confirm that sign-in works, history is present, model connections work, and computers reconnect before resuming tasks.

Do not let two Server processes use the same data directory. Automatic upgrade snapshots contain a manifest and `files/`; they differ from the manual backup above. Do not copy an entire snapshot directory over the Server data directory. Follow the program's recovery instructions after a failed update.

## Failed updates or insufficient disk space

When a data conversion is required, Astral makes a backup first. After a failure or interrupted startup, the next start attempts recovery. Automatic backups live in `upgrade-backups/` beside Server data: `<installation-root>/data/upgrade-backups/` for the official Linux installer.

If space runs out, free space on the data and backup disks before retrying. Large databases need additional room for backups and temporary files. Once the service has started and accepted new tasks, do not overwrite that new work with an old backup.

## Review and clean storage

Use **Settings → Data & storage** to review usage and follow the page's cleanup actions. Deleting a backup permanently removes that recovery copy. Keep independent backups you still need.

Conversation cleanup and backup cleanup have different effects; see [Conversations and history](/docs/usage/conversations/). Astral reclaims free database space in the background, so disk usage can fall later.

For offline database compaction, back up and stop Astral first, then run:

```sh
astral maintenance vacuum --data /actual/server/data-directory
```

This retains conversations and update backups. Start Astral again after it finishes.

export const computersEn = {
  computerInfo: {
    type: 'Type', kind: { local: 'Server machine', remote: 'Remote machine', container: 'Container' },
    hostname: 'Hostname', environment: 'Environment',
    daemonVersion: 'Daemon version', unknownVersion: 'Not reported',
    storage: 'Storage', dataDirectory: 'Data directory', userDirectory: 'User directory',
    container: 'Container', containerName: 'Container name', host: 'Host machine', image: 'Image', volume: 'Data volume', mountPath: 'Mount path',
  },
  computerCommand: {
    serverAddress: 'Server address', serverAddressHint: 'Choose a server address reachable from the target Computer.',
    targetPlatform: 'Target operating system', hostPlatform: 'Container host operating system',
    shell: 'macOS / Linux · Shell', powershell: 'Windows · PowerShell',
  },
  computerMaintenance: {
    updateDaemon: 'Update Daemon', confirmUpdate: 'Update this Daemon to the latest release? Running tasks will be interrupted.', updateServerHint: 'This Daemon is newer than the Server. Update Desktop or Server to use matching versions.',
    states: { checking: 'Checking the official release…', downloading: 'Downloading and verifying…', restarting: 'Restarting and waiting for reconnection…', current: 'Daemon is up to date.', complete: 'Daemon is updated and connected.', failed: 'Daemon update failed. Retry or use the update command.' },
    title: 'Daemon maintenance', updateMethod: 'Update method', withServer: 'Update with Astral Desktop or Server', updateCommand: 'Update command',
    localHint: 'This Daemon runs inside this Astral Server. Updating and restarting Desktop or Server updates it too.',
    remoteHint: 'Update the connected Daemon here, or run the command on this Computer. Running tasks will be interrupted; identity and data are preserved.',
    containerHint: 'Update the connected Daemon here, or run the command on the container host. Running tasks will be interrupted; the container, software environment and data are preserved.',
    managers: {
      desktop: 'Managed by Astral Desktop or Server.', docker: 'Use Docker on the container host to manage the container.',
      launchd: 'Manage the user service in Login Items on the target Mac.', 'systemd-user': 'Use systemctl --user on the target Computer to manage the service.',
      'task-scheduler': 'Manage the current user’s Astral Daemon task in Windows Task Scheduler.', unknown: 'Inspect process management on the target Computer.',
    },
  },
}
export const computersZh: typeof computersEn = {
  computerInfo: {
    type: '类型', kind: { local: '服务所在电脑', remote: '远程电脑', container: '容器' },
    hostname: '主机名', environment: '运行环境',
    daemonVersion: 'Daemon 版本', unknownVersion: '尚未上报',
    storage: '存储', dataDirectory: '数据目录', userDirectory: '用户目录',
    container: '容器', containerName: '容器名', host: '所在电脑', image: '镜像', volume: '数据卷', mountPath: '挂载路径',
  },
  computerCommand: {
    serverAddress: '服务器地址', serverAddressHint: '选择目标电脑能够访问的服务器地址。',
    targetPlatform: '目标电脑系统', hostPlatform: '容器所在电脑的系统',
    shell: 'macOS / Linux · Shell', powershell: 'Windows · PowerShell',
  },
  computerMaintenance: {
    updateDaemon: '更新 Daemon', confirmUpdate: '将这台电脑的 Daemon 更新到最新版本？正在运行的任务会中断。', updateServerHint: '此 Daemon 比 Server 新，建议更新 Desktop 或 Server。',
    states: { checking: '正在检查官方版本…', downloading: '正在下载并校验…', restarting: '正在重启并等待重新连接…', current: 'Daemon 已是最新版本。', complete: 'Daemon 已更新并重新连接。', failed: 'Daemon 更新失败，请重试或使用更新命令。' },
    title: 'Daemon 维护', updateMethod: '更新方式', withServer: '随 Astral Desktop 或 Server 更新', updateCommand: '更新命令',
    localHint: '本机 Daemon 运行在 Astral Server 内，升级并重启 Desktop 或 Server 后一同更新。',
    remoteHint: '直接更新已连接的 Daemon，也可以在这台电脑运行更新命令。正在运行的任务会中断，电脑身份和数据保留。',
    containerHint: '直接更新已连接的 Daemon，也可以在容器所在电脑运行命令。正在运行的任务会中断，容器、已安装环境和数据保留。',
    managers: {
      desktop: '由 Astral Desktop 或 Server 管理。', docker: '在容器所在电脑使用 Docker 管理容器。',
      launchd: '在目标 Mac 的“登录项”中管理用户后台服务。', 'systemd-user': '在目标电脑使用 systemctl --user 管理用户服务。',
      'task-scheduler': '在 Windows“任务计划程序”中管理当前用户的 Astral Daemon 任务。', unknown: '请在目标电脑查看进程托管方式。',
    },
  },
}

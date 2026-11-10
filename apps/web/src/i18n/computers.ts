export const computersEn = {
  containerCheck: {
    missingCLI: 'The Docker command was not found on this Server. Install Docker, or make its existing command available to Astral, then check again.',
    notRunning: 'The selected Docker engine is stopped or unreachable. Start it, then check again.',
    permissionDenied: 'Astral Server runs as “{user}”, which cannot access Docker. Configure Docker access for this account, then check again.',
    failed: 'Docker could not be checked. Review the error details, then check again.',
    requestFailed: 'The Server could not complete the Docker check. Check the Server connection and try again.',
    checkAgain: 'Check again', setupGuide: 'Set up Docker', errorDetails: 'Error details',
  },
  computerInfo: {
    type: 'Type', kind: { local: 'Server machine', remote: 'Remote machine', container: 'Container' },
    hostname: 'Hostname', environment: 'Environment',
    daemonVersion: 'Daemon version', unknownVersion: '—', connectionIP: 'Connection IP',
    storage: 'Storage', dataDirectory: 'Data directory', userDirectory: 'User directory',
    container: 'Container', containerName: 'Container name', host: 'Host machine', image: 'Image', volume: 'Data volume', mountPath: 'Mount path',
  },
  computerCommand: {
    serverAddress: 'Server address', serverAddressHint: 'Choose a server address reachable from the target Computer.',
    targetPlatform: 'Target operating system', hostPlatform: 'Container host operating system',
    shell: 'macOS / Linux · Shell', powershell: 'Windows · PowerShell',
    editConnection: 'Edit connection settings',
  },
  computerAPI: {
    enable: 'Enable Astral API',
    hint: 'Allows commands on this Computer to call the Astral API and local programs to deliver wake events.',
    reuseLocalHint: 'This machine uses its saved setting. You can change it in its Computer details.',
    enabled: 'Astral API enabled', disabled: 'Astral API disabled',
  },
  computerMaintenance: {
    updateDaemon: 'Update Daemon', confirmUpdate: 'Update this Daemon to the latest release? Running tasks will be interrupted.', updateServerHint: 'This Daemon is newer than the Server. Update Desktop or Server to use matching versions.',
    states: { checking: 'Checking the official release…', downloading: 'Downloading and verifying…', restarting: 'Restarting and waiting for reconnection…', current: 'Daemon is up to date.', complete: 'Daemon is updated and connected.', failed: 'Daemon update failed. Retry or use the update command.' },
    title: 'Daemon maintenance', updateMethod: 'Update method', withServer: 'Update with Astral Desktop or Server', updateCommand: 'Update command',
    localHint: 'This Daemon runs inside this Astral Server. Updating and restarting Desktop or Server updates it too.',
    remoteHint: 'Update here, or run the command below on this Computer. Updating interrupts running tasks. Your files are kept.',
    containerHint: 'Update here, or run the command below on the container host. Updating interrupts running tasks. The container, installed software and files are kept.',
    managers: {
      desktop: 'Managed by Astral Desktop or Server.', docker: 'Use Docker on the container host to manage the container.',
      launchd: 'Manage the user service in Login Items on the target Mac.', 'systemd-user': 'Use systemctl --user on the target Computer to manage the service.',
      'task-scheduler': 'Manage the current user’s Astral Daemon task in Windows Task Scheduler.', unknown: 'Inspect process management on the target Computer.',
    },
  },
}
export const computersZh: typeof computersEn = {
  containerCheck: {
    missingCLI: 'Server 找不到 Docker 命令。请安装 Docker，或让 Astral 能找到已有的 Docker 命令，再重新检测。',
    notRunning: '所选 Docker 引擎未运行或无法连接。请启动它，再重新检测。',
    permissionDenied: 'Astral Server 使用「{user}」账号运行，这个账号无法访问 Docker。请为该账号配置 Docker 访问权限，再重新检测。',
    failed: '无法检测 Docker。请查看错误详情，再重新检测。',
    requestFailed: '未能完成 Docker 检测。请检查与 Server 的连接后重试。',
    checkAgain: '重新检测', setupGuide: 'Docker 配置说明', errorDetails: '错误详情',
  },
  computerInfo: {
    type: '类型', kind: { local: '服务所在电脑', remote: '远程电脑', container: '容器' },
    hostname: '主机名', environment: '运行环境',
    daemonVersion: 'Daemon 版本', unknownVersion: '—', connectionIP: '连接 IP',
    storage: '存储', dataDirectory: '数据目录', userDirectory: '用户目录',
    container: '容器', containerName: '容器名', host: '所在电脑', image: '镜像', volume: '数据卷', mountPath: '挂载路径',
  },
  computerCommand: {
    serverAddress: '服务器地址', serverAddressHint: '选择目标电脑能够访问的服务器地址。',
    targetPlatform: '目标电脑系统', hostPlatform: '容器所在电脑的系统',
    shell: 'macOS / Linux · Shell', powershell: 'Windows · PowerShell',
    editConnection: '编辑连接设置',
  },
  computerAPI: {
    enable: '启用 Astral API',
    hint: '允许在这台电脑上执行的命令调用 Astral API，也允许本机程序投递事件唤醒。',
    reuseLocalHint: '本机沿用已保存的设置，可在电脑详情中修改。',
    enabled: '已启用 Astral API', disabled: '已关闭 Astral API',
  },
  computerMaintenance: {
    updateDaemon: '更新 Daemon', confirmUpdate: '将这台电脑的 Daemon 更新到最新版本？正在运行的任务会中断。', updateServerHint: '此 Daemon 比 Server 新，建议更新 Desktop 或 Server。',
    states: { checking: '正在检查官方版本…', downloading: '正在下载并校验…', restarting: '正在重启并等待重新连接…', current: 'Daemon 已是最新版本。', complete: 'Daemon 已更新并重新连接。', failed: 'Daemon 更新失败，请重试或使用更新命令。' },
    title: 'Daemon 维护', updateMethod: '更新方式', withServer: '随 Astral Desktop 或 Server 更新', updateCommand: '更新命令',
    localHint: '本机 Daemon 运行在 Astral Server 内，升级并重启 Desktop 或 Server 后一同更新。',
    remoteHint: '在这里更新，或在这台电脑上运行下方命令。更新会中断正在运行的任务，文件会保留。',
    containerHint: '在这里更新，或在容器所在电脑上运行下方命令。更新会中断正在运行的任务，容器、已安装的软件和文件会保留。',
    managers: {
      desktop: '由 Astral Desktop 或 Server 管理。', docker: '在容器所在电脑使用 Docker 管理容器。',
      launchd: '在目标 Mac 的“登录项”中管理用户后台服务。', 'systemd-user': '在目标电脑使用 systemctl --user 管理用户服务。',
      'task-scheduler': '在 Windows“任务计划程序”中管理当前用户的 Astral Daemon 任务。', unknown: '请在目标电脑查看进程托管方式。',
    },
  },
}

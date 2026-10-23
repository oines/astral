export const computersEn = {
  computerInfo: {
    type: 'Type', kind: { local: 'Server machine', remote: 'Remote machine', container: 'Container' },
    hostname: 'Hostname', environment: 'Environment',
    storage: 'Storage', dataDirectory: 'Data directory', userDirectory: 'User directory',
    container: 'Container', containerName: 'Container name', host: 'Host machine', image: 'Image', volume: 'Data volume', mountPath: 'Mount path',
  },
}
export const computersZh: typeof computersEn = {
  computerInfo: {
    type: '类型', kind: { local: '服务所在电脑', remote: '远程电脑', container: '容器' },
    hostname: '主机名', environment: '运行环境',
    storage: '存储', dataDirectory: '数据目录', userDirectory: '用户目录',
    container: '容器', containerName: '容器名', host: '所在电脑', image: '镜像', volume: '数据卷', mountPath: '挂载路径',
  },
}

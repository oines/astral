// Electron has its own flags. Forward only Astral's explicitly supplied network
// options; supplying defaults here would override saved App preferences.
export function networkArguments(argv: string[]): string[] {
  const result: string[] = []
  for (let index = 0; index < argv.length; index++) {
    const argument = argv[index]!
    const name = argument.split('=', 1)[0]
    if (name === '--tls-cert' || name === '--tls-key') throw new Error('Astral serves HTTP. Configure HTTPS on your reverse proxy.')
    if (name === '--allow-remote-management') {
      const next = argv[index + 1]
      if (!argument.includes('=') && (next === 'true' || next === 'false')) {
        result.push(`${argument}=${next}`)
        index++
      } else {
        if (!argument.includes('=') && next !== undefined && !next.startsWith('--')) throw new Error(`${name} accepts true or false.`)
        result.push(argument)
      }
    } else if (name === '--listen' || name === '--public-url') {
      if (argument.includes('=')) result.push(argument)
      else {
        const value = argv[++index]
        if (value === undefined || value.startsWith('--')) throw new Error(`${name} requires a value.`)
        result.push(argument, value)
      }
    }
  }
  return result
}

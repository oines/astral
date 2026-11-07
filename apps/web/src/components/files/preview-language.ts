export function codeLanguage(name: string) {
  if (/\.py$/i.test(name)) return 'python'
  if (/\.[cm]?[jt]sx?$/i.test(name)) return 'javascript'
  if (/\.(sh|bash|zsh)$/i.test(name)) return 'shell'
  return 'plaintext'
}

/** Give truncated labels a native full-name hint without repeating short text. */
export function revealOverflowLabel(event: Event, selector: string) {
  const button = event.currentTarget as HTMLElement
  const label = button.querySelector<HTMLElement>(selector)
  if (label && label.scrollWidth > label.clientWidth) button.title = label.textContent || ''
  else button.removeAttribute('title')
}

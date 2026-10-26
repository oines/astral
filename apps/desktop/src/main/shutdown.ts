/** Serialize all quit paths. A failed cleanup must never grant permission to install. */
export class DesktopShutdown {
  complete = false
  private pending?: Promise<void>
  constructor(private readonly stop: () => Promise<void>) {}
  prepare(): Promise<void> {
    if (this.complete) return Promise.resolve()
    if (!this.pending) {
      this.pending = this.stop().then(() => { this.complete = true }).finally(() => { this.pending = undefined })
    }
    return this.pending
  }
}

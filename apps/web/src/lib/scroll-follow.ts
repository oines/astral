export type ScrollMetrics = { top: number; height: number; viewport: number; width: number }

// Scroll events alone cannot distinguish reading gestures from layout changes
// or our own anchor corrections. Only actual movement through unchanged content
// changes the follow mode; upward input releases it before the next paint.
export class ScrollFollow {
  following = true
  metrics?: ScrollMetrics
  private writtenTop?: number
  private lastInputAt = -Infinity

  get distance() { return this.metrics ? Math.max(0, this.metrics.height - this.metrics.top - this.metrics.viewport) : 0 }
  get showJump() { return !this.following && this.distance > 120 }

  input(direction: 'up' | 'down' | 'pointer', metrics: ScrollMetrics, now: number) {
    this.lastInputAt = now
    this.metrics = metrics
    if (direction === 'up') this.following = false
    else if (direction === 'down' && this.distance <= 40) this.following = true
  }
  cooldown(now: number) { return Math.max(0, 150 - (now - this.lastInputAt)) }

  observe(next: ScrollMetrics) {
    const previous = this.metrics
    const ownScroll = this.writtenTop !== undefined && Math.abs(next.top - this.writtenTop) < 1
    this.writtenTop = undefined
    const sameLayout = previous && previous.height === next.height && previous.viewport === next.viewport && previous.width === next.width
    if (!ownScroll && sameLayout) {
      if (next.top < previous.top) this.following = false
      else if (next.top > previous.top && next.height - next.top - next.viewport <= 40) this.following = true
    }
    this.metrics = next
  }
  wrote(metrics: ScrollMetrics) { this.writtenTop = metrics.top; this.metrics = metrics }
  measure(metrics: ScrollMetrics) { this.metrics = metrics }
  reset() { this.following = true; this.metrics = undefined; this.writtenTop = undefined; this.lastInputAt = -Infinity }
}

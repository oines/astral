// Preserve usable space for both conversations and previews before falling back
// to a temporary overlay. The width excludes the navigation sidebar and insets.
export const surfaceMinimumWidth = 320
const chatMinimumWidth = 360
const splitGap = 8

export function surfaceLayout(availableWidth: number, preferredWidth: number) {
  const available = Math.max(0, availableWidth)
  const overlay = available < chatMinimumWidth + splitGap + surfaceMinimumWidth
  const maximumWidth = overlay ? available : available - chatMinimumWidth - splitGap
  return {
    overlay,
    maximumWidth,
    width: overlay ? available : Math.max(surfaceMinimumWidth, Math.min(preferredWidth, maximumWidth)),
  }
}

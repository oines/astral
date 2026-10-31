// A completed cleanup must still refresh sizes if an earlier snapshot is loading.
export function createStorageLoader(read: (refreshSizes: boolean) => Promise<void>) {
  let running: Promise<void> | undefined
  let refreshQueued = false
  return (refreshSizes = false) => {
    refreshQueued ||= refreshSizes
    if (!running) {
      running = (async () => {
        try {
          do {
            const refresh = refreshQueued
            refreshQueued = false
            await read(refresh)
          } while (refreshQueued)
        } finally { running = undefined }
      })()
    }
    return running
  }
}

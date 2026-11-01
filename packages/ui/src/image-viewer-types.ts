export interface ImageViewerItem {
  src: string
  originalSrc?: string
  downloadUrl?: string
  name?: string
}

export interface ImageViewerLabels {
  title: string
  close: string
  previous: string
  next: string
  zoomIn: string
  zoomOut: string
  fit: string
  download: string
  downloadPreview: string
  loading: string
  failed: string
  originalUnavailable: string
  retry: string
}

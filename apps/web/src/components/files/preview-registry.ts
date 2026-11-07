import { defineAsyncComponent, type Component } from 'vue'
import type { FilePreviewResource } from '../../composables/useFileService'
import MarkdownFilePreview from './renderers/MarkdownFilePreview.vue'
import ImageFilePreview from './renderers/ImageFilePreview.vue'
import PdfFilePreview from './renderers/PdfFilePreview.vue'
import MediaFilePreview from './renderers/MediaFilePreview.vue'
import DownloadFilePreview from './renderers/DownloadFilePreview.vue'

export interface PreviewRenderer {
  id: string
  labelKey: string
  format?: (file: FilePreviewResource) => string | undefined
  component: Component
  createComponent?: () => Component
  refreshRemount?: boolean
  matches: (file: FilePreviewResource) => boolean
}

const createCodeRenderer = () => defineAsyncComponent(() => import('./renderers/CodeFilePreview.vue'))
const code = createCodeRenderer()
export const filePreviewRenderers: readonly PreviewRenderer[] = [
  { id: 'image', refreshRemount: true, labelKey: 'filesKindImage', component: ImageFilePreview, matches: file => file.mediaType.startsWith('image/') },
  { id: 'pdf', refreshRemount: true, labelKey: 'filesKindPdf', component: PdfFilePreview, matches: file => file.mediaType === 'application/pdf' },
  { id: 'audio', refreshRemount: true, labelKey: 'filesKindAudio', component: MediaFilePreview, matches: file => file.mediaType.startsWith('audio/') },
  { id: 'video', refreshRemount: true, labelKey: 'filesKindVideo', component: MediaFilePreview, matches: file => file.mediaType.startsWith('video/') },
  { id: 'markdown', labelKey: 'filesKindMarkdown', component: MarkdownFilePreview, matches: file => file.content !== undefined && (/markdown/.test(file.mediaType) || /\.(md|markdown)$/i.test(file.name)) },
  { id: 'code', labelKey: 'filesText', format: file => file.name.includes('.') ? file.name.split('.').at(-1)?.toUpperCase() : undefined, component: code, createComponent: createCodeRenderer, matches: file => file.content !== undefined },
  { id: 'download', labelKey: 'filesKindDownload', component: DownloadFilePreview, matches: () => true },
]

export function selectFilePreview(file: FilePreviewResource, renderers = filePreviewRenderers): PreviewRenderer {
  return renderers.find(renderer => renderer.matches(file)) || filePreviewRenderers.at(-1)!
}

import type { Monaco } from '@monaco-editor/react'
import type { editor } from 'monaco-editor'

// Monaco caches glyph widths. Recompute after the bundled font loads, or
// selection/caret/wrapping can use fallback widths while text uses the real font.
export function watchEditorLayout(instance: editor.IStandaloneCodeEditor, monaco: Monaco) {
  let disposed = false
  let frame = 0
  const schedule = () => {
    if (disposed || frame) return
    frame = requestAnimationFrame(() => {
      frame = 0
      if (disposed) return
      monaco.editor.remeasureFonts()
      instance.layout()
    })
  }
  const fonts = document.fonts
  void fonts?.ready.then(schedule)
  fonts?.addEventListener('loadingdone', schedule)
  window.addEventListener('resize', schedule)
  const observer = new ResizeObserver(schedule)
  observer.observe(instance.getContainerDomNode())
  schedule()
  return () => {
    disposed = true
    cancelAnimationFrame(frame)
    observer.disconnect()
    fonts?.removeEventListener('loadingdone', schedule)
    window.removeEventListener('resize', schedule)
  }
}

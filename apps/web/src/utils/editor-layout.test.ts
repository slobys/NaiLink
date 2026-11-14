import type { Monaco } from '@monaco-editor/react'
import type { editor } from 'monaco-editor'
import { afterEach, expect, it, vi } from 'vitest'
import { watchEditorLayout } from './editor-layout'

afterEach(() => vi.unstubAllGlobals())

it('remeasures after font loading and container changes, then stops on unmount', async () => {
  const fonts = new EventTarget()
  let ready!: () => void
  Object.assign(fonts, {
    ready: new Promise<void>((resolve) => {
      ready = resolve
    }),
  })
  const viewport = new EventTarget()
  let resize!: () => void
  const disconnect = vi.fn()
  const frames = new Map<number, FrameRequestCallback>()
  let frameId = 0
  vi.stubGlobal('document', { fonts })
  vi.stubGlobal('window', viewport)
  vi.stubGlobal('requestAnimationFrame', (callback: FrameRequestCallback) => {
    frames.set(++frameId, callback)
    return frameId
  })
  vi.stubGlobal('cancelAnimationFrame', (id: number) => frames.delete(id))
  vi.stubGlobal(
    'ResizeObserver',
    class {
      constructor(callback: () => void) {
        resize = callback
      }
      observe() {}
      disconnect = disconnect
    },
  )
  const layout = vi.fn()
  const remeasure = vi.fn()
  const flush = () => {
    const pending = [...frames.values()]
    frames.clear()
    pending.forEach((callback) => callback(0))
  }
  const stop = watchEditorLayout(
    { layout, getContainerDomNode: () => ({}) } as unknown as editor.IStandaloneCodeEditor,
    { editor: { remeasureFonts: remeasure } } as unknown as Monaco,
  )
  flush()
  ready()
  await Promise.resolve()
  fonts.dispatchEvent(new Event('loadingdone'))
  resize()
  viewport.dispatchEvent(new Event('resize'))
  expect(frames.size).toBe(1)
  flush()
  expect(remeasure).toHaveBeenCalledTimes(2)
  expect(layout).toHaveBeenCalledTimes(2)
  resize()
  stop()
  fonts.dispatchEvent(new Event('loadingdone'))
  viewport.dispatchEvent(new Event('resize'))
  flush()
  expect(disconnect).toHaveBeenCalledOnce()
  expect(layout).toHaveBeenCalledTimes(2)
})

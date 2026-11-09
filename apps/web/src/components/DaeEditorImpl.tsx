import type { Monaco, OnMount } from '@monaco-editor/react'
import type * as monacoEditor from 'monaco-editor'
import type { DaeEditorProps } from './DaeEditor'
import { Editor } from '@monaco-editor/react'
import { useStore } from '@nanostores/react'
import { useCallback, useEffect, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { Button } from '~/components/ui/button'
import { EDITOR_OPTIONS, EDITOR_THEME_DARK, EDITOR_THEME_LIGHT } from '~/constants/editor'
import {
  applyShikiThemes,
  getLspClient,
  handleEditorBeforeMount,
  initLsp,
  isShikiReady,
  syncModelWithLsp,
} from '~/monaco'

import { colorSchemeAtom } from '~/store'
import { routingFormatEdits } from '~/utils/routing-format'

type CodeEditor = Parameters<OnMount>[0]

function formatRouting(editor: CodeEditor, pastedRange?: monacoEditor.Range) {
  const model = editor.getModel()
  if (!model) return

  const start = pastedRange && model.getOffsetAt(pastedRange.getStartPosition())
  const end = pastedRange && model.getOffsetAt(pastedRange.getEndPosition())
  const edits = routingFormatEdits(model.getValue(), start, end)
  if (!edits.length) return

  if (!pastedRange) editor.pushUndoStop()
  editor.executeEdits(
    'nailink.routingFormat',
    edits.map(({ start, end, text }) => {
      const from = model.getPositionAt(start)
      const to = model.getPositionAt(end)
      return {
        range: {
          startLineNumber: from.lineNumber,
          startColumn: from.column,
          endLineNumber: to.lineNumber,
          endColumn: to.column,
        },
        text,
      }
    }),
  )
  editor.pushUndoStop()
}

/**
 * DAE config editor component with LSP support
 *
 * Wraps Monaco editor with:
 * - Shiki syntax highlighting
 * - DAE LSP for completions, hover, diagnostics
 * - Context-aware completion filtering based on configType
 */
export function DaeEditor({
  value,
  onChange,
  configType = 'routing',
  height = '100%',
  disabled,
  routingTools = false,
}: DaeEditorProps) {
  const { t } = useTranslation()
  const colorScheme = useStore(colorSchemeAtom)
  const [routingEditor, setRoutingEditor] = useState<CodeEditor>()
  const [, forceUpdate] = useState({})
  const monacoRef = useRef<Monaco | null>(null)
  const lspSyncRef = useRef<{ dispose: () => void } | null>(null)
  const modelRef = useRef<monacoEditor.editor.ITextModel | null>(null)

  // Cleanup LSP sync on unmount
  useEffect(() => {
    return () => {
      lspSyncRef.current?.dispose()
      lspSyncRef.current = null
    }
  }, [])

  // Update config type context for LSP
  useEffect(() => {
    const lspClient = getLspClient()
    if (lspClient) {
      lspClient.setConfigContext(configType)
    }
  }, [configType])

  const handleEditorDidMount = useCallback(
    async (editor: CodeEditor, monacoInstance: Monaco) => {
      monacoRef.current = monacoInstance

      if (routingTools) {
        setRoutingEditor(editor)
        editor.onDidPaste(({ range }) => {
          if (editor.getSelections()?.length === 1) formatRouting(editor, range)
        })
      }

      // Apply Shiki themes if not ready
      if (!isShikiReady()) {
        await applyShikiThemes(monacoInstance)
        forceUpdate({})
      }

      // Initialize LSP client
      await initLsp(monacoInstance)

      // Set config context for completion filtering
      const lspClient = getLspClient()
      if (lspClient) {
        lspClient.setConfigContext(configType)
      }

      // Sync document with LSP
      const model = editor.getModel()
      if (model) {
        modelRef.current = model
        lspSyncRef.current?.dispose()
        lspSyncRef.current = syncModelWithLsp(model)
      }
    },
    [configType, routingTools],
  )

  const theme = isShikiReady()
    ? colorScheme === 'dark'
      ? EDITOR_THEME_DARK
      : EDITOR_THEME_LIGHT
    : colorScheme === 'dark'
      ? 'vs-dark'
      : 'vs'

  const editor = (
    <Editor
      height={height}
      theme={theme}
      options={{
        ...EDITOR_OPTIONS,
        ...(routingTools && {
          ariaLabel: t('routingEditor.label'),
          automaticLayout: true,
          formatOnPaste: false,
          lineNumbers: 'on',
          lineNumbersMinChars: 2,
          wordWrap: 'on',
          wrappingIndent: 'indent',
        }),
        readOnly: disabled,
      }}
      language={configType === 'dns' ? 'dnsA' : 'routingA'}
      value={value}
      onChange={(v) => onChange(v || '')}
      beforeMount={handleEditorBeforeMount}
      onMount={handleEditorDidMount}
    />
  )

  if (!routingTools) return editor

  return (
    <div className="flex h-full min-w-0 flex-col">
      <div className="flex shrink-0 items-center justify-between gap-2 border-b px-3 py-1">
        <p className="min-w-0 text-xs text-muted-foreground">{t('routingEditor.hint')}</p>
        <Button
          type="button"
          variant="ghost"
          size="sm"
          className="shrink-0"
          disabled={!routingEditor || disabled || !value.trim()}
          onClick={() => {
            if (!routingEditor) return
            formatRouting(routingEditor)
            routingEditor.focus()
          }}
        >
          {t('routingEditor.format')}
        </Button>
      </div>
      <div className="min-h-0 min-w-0 flex-1">{editor}</div>
    </div>
  )
}

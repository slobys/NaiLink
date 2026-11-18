import type { RoutingACompletionItem } from '@daeuniverse/dae-editor'
import type { Monaco } from '@monaco-editor/react'
import type * as monacoEditor from 'monaco-editor'
import {
  DiagnosticSeverity,
  MonacoLspClient,
  registerRoutingALanguage,
  setDynamicCompletionItems as setDynamicCompletionItemsBase,
} from '@daeuniverse/dae-editor'
// Import the browser LSP server worker
import DaeLspWorker from '@daeuniverse/dae-lsp/server/browser?worker'
import { loader } from '@monaco-editor/react'
import { textmateThemeToMonacoTheme } from '@shikijs/monaco'
import * as monaco from 'monaco-editor'
import EditorWorker from 'monaco-editor/esm/vs/editor/editor.worker?worker'
import { normalizeTheme } from 'shiki/core'
import darkTheme from 'shiki/themes/vitesse-dark.mjs'
import lightTheme from 'shiki/themes/vitesse-light.mjs'
import { EDITOR_THEME_DARK, EDITOR_THEME_LIGHT } from '~/constants/editor'
import { subscribeEditorCompletions } from '~/utils/editor-completions'

// Configure Monaco workers for Vite
globalThis.MonacoEnvironment = {
  getWorker() {
    return new EditorWorker()
  },
}

// Configure loader to use local monaco-editor package
loader.config({ monaco })

// Routing and DNS tokenize with Monarch. Only convert the theme colors and rules.
for (const [name, theme] of [
  [EDITOR_THEME_LIGHT, lightTheme],
  [EDITOR_THEME_DARK, darkTheme],
] as const) {
  // Shiki's converter types use the optional monaco-editor-core peer, which
  // isn't installed here. Its standalone theme data is the same in Monaco.
  monaco.editor.defineTheme(
    name,
    textmateThemeToMonacoTheme(normalizeTheme(theme)) as monacoEditor.editor.IStandaloneThemeData,
  )
}

// LSP Client instance (singleton)
let lspClient: MonacoLspClient | null = null
let lspInitialized = false

// Cache for dynamic completion items (set before LSP client is initialized)
let pendingDynamicCompletionItems: RoutingACompletionItem[] = []

/**
 * Register a lightweight DNS config language for Monaco.
 * dae-editor currently only ships routingA, so we define dnsA here for syntax highlighting.
 */
function registerDnsALanguage(monacoInstance: Monaco): void {
  const id = 'dnsA'
  // Avoid double registration
  if ((monacoInstance.languages as any).getLanguages?.().some((l: any) => l.id === id)) return

  monacoInstance.languages.register({ id })
  monacoInstance.languages.setMonarchTokensProvider(id, {
    tokenizer: {
      root: [
        [/#[^\n]*/, 'comment'],
        [/'[^']*'/, 'string'],
        [/"[^"]*"/, 'string'],
        [/\b(upstream|routing|request|response|fallback|accept|reject|asis)\b/, 'keyword'],
        [/\b(qname|qtype|rcode|qclass)\b/, 'keyword'],
        [/\b(geosite|geoip)\b/, 'type.identifier'],
        [/[{}()]/, '@brackets'],
        [/->/, 'operator'],
        [/:/, 'delimiter'],
        [/[\w.-]+/, 'identifier'],
        [/\s+/, 'white'],
      ],
    },
  })
}

/**
 * Initialize the DAE LSP client
 */
async function initLspClient(monacoInstance: Monaco): Promise<void> {
  if (lspInitialized) return

  try {
    // Create worker using Vite's ?worker import (returns a Worker constructor)
    const worker = new DaeLspWorker() as Worker

    // Create LSP client with the worker instance
    lspClient = new MonacoLspClient(worker)

    // Initialize the LSP connection
    await lspClient.initialize()

    // Register Monaco providers
    lspClient.registerProviders(monacoInstance as unknown as typeof monacoEditor, 'routingA')

    // Apply any pending dynamic completion items that were set before LSP was initialized
    if (pendingDynamicCompletionItems.length > 0) {
      lspClient.setDynamicCompletionItems(
        pendingDynamicCompletionItems.map((item) => ({
          label: item.label,
          kind: item.kind === 'variable' ? 6 : 14, // Variable or Keyword
          detail: item.detail,
          documentation: item.documentation,
          insertText: item.insertText,
        })),
      )
    }

    // Set up diagnostics handling
    lspClient.onDiagnostics((uri, diagnostics) => {
      const model = monacoInstance.editor
        .getModels()
        .find((m: monacoEditor.editor.ITextModel) => m.uri.toString() === uri)
      if (model) {
        const markers = diagnostics.map((d) => ({
          severity:
            d.severity === DiagnosticSeverity.Error
              ? monacoInstance.MarkerSeverity.Error
              : d.severity === DiagnosticSeverity.Warning
                ? monacoInstance.MarkerSeverity.Warning
                : monacoInstance.MarkerSeverity.Info,
          startLineNumber: d.range.start.line + 1,
          startColumn: d.range.start.character + 1,
          endLineNumber: d.range.end.line + 1,
          endColumn: d.range.end.character + 1,
          message: d.message,
          source: d.source || 'dae',
        }))
        monacoInstance.editor.setModelMarkers(model, 'dae', markers)
      }
    })

    lspInitialized = true
  } catch (error) {
    console.error('Failed to initialize DAE LSP client:', error)
  }
}

// Handler for beforeMount prop in Editor component
// Registers routingA language definition (LSP provides all providers)
export function handleEditorBeforeMount(monacoInstance: Monaco) {
  registerRoutingALanguage(monacoInstance)
  registerDnsALanguage(monacoInstance)
}

/**
 * Initialize LSP for the editor (call after editor is mounted)
 */
export async function initLsp(monacoInstance: Monaco) {
  await initLspClient(monacoInstance)
}

/**
 * Get the LSP client instance
 */
export function getLspClient(): MonacoLspClient | null {
  return lspClient
}

/**
 * Sync a Monaco model with the LSP server
 * Call this when an editor opens a routingA document
 */
export function syncModelWithLsp(model: monacoEditor.editor.ITextModel): monacoEditor.IDisposable | null {
  if (!lspClient) return null

  const uri = model.uri.toString()
  const languageId = model.getLanguageId()

  // Only sync routingA documents
  if (languageId !== 'routingA') return null

  // Open document
  lspClient.didOpen(uri, languageId, model.getVersionId(), model.getValue())

  // Listen for changes
  const changeDisposable = model.onDidChangeContent(() => {
    if (!lspClient) return
    lspClient.didChange(uri, [{ text: model.getValue() }])
  })

  // Return disposable to clean up
  return {
    dispose: () => {
      changeDisposable.dispose()
      if (lspClient) {
        lspClient.didClose(uri)
      }
    },
  }
}

/**
 * Set dynamic completion items for routingA language
 * Use this to add user-configured groups to autocomplete suggestions
 */
function setDynamicCompletionItems(items: RoutingACompletionItem[]): void {
  // Always cache the items for when LSP client is initialized later
  pendingDynamicCompletionItems = items

  setDynamicCompletionItemsBase(items)

  // Also set for LSP client if available
  if (lspClient) {
    lspClient.setDynamicCompletionItems(
      items.map((item) => ({
        label: item.label,
        kind: item.kind === 'variable' ? 6 : 14, // Variable or Keyword
        detail: item.detail,
        documentation: item.documentation,
        insertText: item.insertText,
      })),
    )
  }
}

/**
 * Convert group names to routingA completion items
 * @param groupNames - Array of group names configured by user
 * @returns Completion items for group outbounds
 */
subscribeEditorCompletions(setDynamicCompletionItems)

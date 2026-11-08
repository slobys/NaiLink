import type { ReactNode } from 'react'
import { Component, lazy, Suspense } from 'react'

export type DaeConfigType = 'routing' | 'dns'
export interface DaeEditorProps {
  value: string
  onChange: (value: string) => void
  configType?: DaeConfigType
  height?: string | number
  disabled?: boolean
  routingTools?: boolean
}

const Editor = lazy(() => import('./DaeEditorImpl').then((module) => ({ default: module.DaeEditor })))

class EditorBoundary extends Component<DaeEditorProps & { children: ReactNode }, { failed: boolean }> {
  state = { failed: false }
  static getDerivedStateFromError() {
    return { failed: true }
  }
  render() {
    if (!this.state.failed) return this.props.children
    return (
      <div className="space-y-2">
        <p role="alert" className="text-sm text-muted-foreground">
          代码编辑器加载失败，可继续使用文本模式编辑，或刷新页面后重试。
        </p>
        <textarea
          aria-label="配置代码"
          className="min-h-64 w-full rounded border bg-background p-3 font-mono text-sm"
          value={this.props.value}
          disabled={this.props.disabled}
          onChange={(event) => this.props.onChange(event.target.value)}
        />
      </div>
    )
  }
}

export function DaeEditor(props: DaeEditorProps) {
  return (
    <EditorBoundary {...props}>
      <Suspense
        fallback={
          <div role="status" className="p-4 text-sm text-muted-foreground">
            正在加载代码编辑器…
          </div>
        }
      >
        <Editor {...props} />
      </Suspense>
    </EditorBoundary>
  )
}

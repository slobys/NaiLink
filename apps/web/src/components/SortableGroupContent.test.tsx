import { DragDropContext } from '@hello-pangea/dnd'
import { renderToStaticMarkup } from 'react-dom/server'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { SortableGroupContent } from './SortableGroupContent'

const { active } = vi.hoisted(() => {
  vi.stubGlobal('location', { origin: 'http://localhost', protocol: 'http:', hostname: 'localhost' })
  return { active: new Map<string, string[]>() }
})

vi.mock('~/hooks/useRuntimeNodes', () => ({ useRuntimeNodes: () => ({ active }) }))
vi.mock('react-i18next', () => ({ useTranslation: () => ({ t: (key: string) => key }) }))

function renderGroup(groupName = 'proxy') {
  const props = {
    groupId: 'group-id',
    groupName,
    nodes: [{ id: 'shared-node', name: 'Shared node', protocol: 'vless' }],
    subscriptions: [],
    onExpand: () => {},
    onDelNode: () => {},
    onDelSubscription: () => {},
    onOpenAddNodes: () => {},
    onOpenAddSubscriptions: () => {},
  }
  return renderToStaticMarkup(
    <DragDropContext onDragEnd={() => {}}>
      <SortableGroupContent {...props} />
    </DragDropContext>,
  )
}

describe('group member current outlet markers', () => {
  beforeEach(() => active.clear())

  it('does not mark a candidate selected only by another group', () => {
    active.set('shared-node', ['AI'])
    const html = renderGroup()
    expect(html).toContain('data-active="false"')
    expect(html).not.toContain('node-active-mark')
  })

  it('shows only the containing group when a node is selected by multiple groups', () => {
    active.set('shared-node', ['proxy', 'AI'])
    const html = renderGroup()
    expect(html).toContain('data-active="true"')
    expect(html).toContain('aria-label="panel.currentOutlet: proxy"')
    expect(html).not.toContain('proxy / AI')
  })

  it('keeps an explicitly selected member marked in its own group', () => {
    active.set('shared-node', ['proxy'])
    expect(renderGroup()).toContain('aria-label="panel.currentOutlet: proxy"')
  })

  it('does not infer a selection for a renamed, unapplied group', () => {
    active.set('shared-node', ['proxy'])
    expect(renderGroup('renamed-proxy')).not.toContain('node-active-mark')
  })

  it('does not mark unknown, unavailable or cleared runtime selections', () => {
    expect(renderGroup()).not.toContain('node-active-mark')
  })
})

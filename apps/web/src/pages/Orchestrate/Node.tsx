import type { DraggableProvided, DraggableStateSnapshot } from '@hello-pangea/dnd'
import type { NodeLatencyProbeResult } from '~/apis'
import type { LatencyTests } from '~/components/LatencyStatus'
import type { QRCodeModalRef } from '~/components/QRCodeModal'
import type { LibraryNode, NodeFilters } from '~/utils/node-library'
import { Draggable, Droppable } from '@hello-pangea/dnd'
import {
  Cloud,
  CloudUpload,
  Eye,
  FileInput,
  Gauge,
  GripVertical,
  MoreHorizontal,
  Pencil,
  Plus,
  Search,
  Trash2,
} from 'lucide-react'
import { useEffect, useLayoutEffect, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { useImportNodesMutation, useRemoveNodesMutation } from '~/apis'
import { ConfigureNodeFormModal } from '~/components/ConfigureNodeFormModal'
import { EditNodeFormModal } from '~/components/EditNodeFormModal'
import { ImportResourceFormModal } from '~/components/ImportResourceFormModal'
import { LatencyBadge } from '~/components/LatencyBadge'
import { LatencyStatus } from '~/components/LatencyStatus'
import { NodeActiveMark } from '~/components/NodeActiveMark'
import { NodeName } from '~/components/NodeName'
import { NodeUDPTest } from '~/components/NodeUDPTest'
import { QRCodeModal } from '~/components/QRCodeModal'
import { ResourceEmpty } from '~/components/ResourceEmpty'
import { ResourceGroupPicker } from '~/components/ResourceGroupPicker'
import { Section } from '~/components/Section'
import { Button } from '~/components/ui/button'
import { Dialog, DialogContent, DialogFooter, DialogHeader, DialogTitle } from '~/components/ui/dialog'
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from '~/components/ui/dropdown-menu'
import { Sheet, SheetContent, SheetDescription, SheetHeader, SheetTitle } from '~/components/ui/sheet'
import { SimpleTooltip } from '~/components/ui/tooltip'
import { useRuntimeNodes } from '~/hooks/useRuntimeNodes'
import { cn } from '~/lib/utils'
import { getInstantDropStyle } from '~/utils'
import { defaultNodeFilters } from '~/utils/node-library'
import { NODE_ROW_HEIGHT, nodeWindow } from '~/utils/node-window'

export const NODE_DROPPABLE_ID = 'node-list'

type NodeAction = 'details' | 'edit' | 'qr' | 'remove' | 'join'

function NodeRow({
  entry,
  provided,
  snapshot,
  latency,
  onAction,
  tests,
  activeGroups,
}: {
  tests: LatencyTests
  entry: LibraryNode
  provided: DraggableProvided
  snapshot: DraggableStateSnapshot
  latency?: NodeLatencyProbeResult
  onAction: (entry: LibraryNode, action: NodeAction) => void
  activeGroups?: string[]
}) {
  const { t } = useTranslation()
  const { node, sources, editable } = entry
  const name = node.tag || node.name || node.id
  return (
    <div
      ref={provided.innerRef}
      {...provided.draggableProps}
      style={getInstantDropStyle(provided, snapshot)}
      className={cn('panel-node-row', snapshot.isDragging && 'panel-node-dragging')}
      data-node-id={node.id}
      data-name-card
      data-active={!!activeGroups?.length}
      role="listitem"
    >
      <div
        {...provided.dragHandleProps}
        className="panel-node-handle"
        aria-label={`${t('a11y.dragToReorder')} ${name}`}
      >
        <GripVertical aria-hidden="true" className="h-4 w-4" />
      </div>
      <div className="panel-node-content min-w-0">
        <button
          type="button"
          className="block w-full min-w-0 text-left text-sm font-medium hover:text-primary"
          title={name}
          aria-label={`${t('panel.nodeDetails')} ${name}`}
          data-node-details={node.id}
          onClick={() => onAction(entry, 'details')}
        >
          <span className="node-title-line">
            <NodeName name={name} showRegion />
            <NodeActiveMark groups={activeGroups} />
          </span>
          <span className="panel-node-meta" title={sources.map((source) => source.name).join(' · ')}>
            <span className="panel-node-protocol">{node.protocol}</span>
            <span className="truncate">
              {sources.length ? sources.map((source) => source.name).join(' · ') : t('panel.manual')}
            </span>
          </span>
        </button>
      </div>
      <div className="panel-node-actions">
        <Button
          variant="ghost"
          size="sm"
          className="panel-node-probe disabled:opacity-100"
          loading={tests.pending.has(node.id)}
          disabled={tests.busy}
          aria-label={`${t('latency.testNode')} ${name}`}
          title={t('latency.testNode')}
          onClick={() => tests.test([node.id], 'manual')}
        >
          {!tests.pending.has(node.id) && !latency && <Gauge aria-hidden="true" className="size-4" />}
          {latency ? <LatencyBadge result={latency} /> : t('latency.test')}
        </Button>
        <Button
          variant="ghost"
          size="sm"
          className="panel-node-join"
          aria-label={t('panel.addToGroup')}
          onClick={() => onAction(entry, 'join')}
        >
          <Plus aria-hidden="true" className="size-3.5" />
          {t('panel.addToGroup')}
        </Button>
      </div>
      <DropdownMenu>
        <DropdownMenuTrigger asChild>
          <Button
            variant="ghost"
            size="icon"
            className="panel-node-more"
            aria-label={`${t('panel.moreActions')} ${name}`}
          >
            <MoreHorizontal aria-hidden="true" className="h-4 w-4" />
          </Button>
        </DropdownMenuTrigger>
        <DropdownMenuContent align="end">
          <DropdownMenuItem onSelect={() => onAction(entry, 'details')}>
            <Eye aria-hidden="true" />
            {t('panel.nodeDetails')}
          </DropdownMenuItem>
          <DropdownMenuItem onSelect={() => onAction(entry, 'qr')}>
            <Eye aria-hidden="true" />
            {t('actions.viewQRCode')}
          </DropdownMenuItem>
          {editable && (
            <DropdownMenuItem onSelect={() => onAction(entry, 'edit')}>
              <Pencil aria-hidden="true" />
              {t('actions.edit')}
            </DropdownMenuItem>
          )}
          {editable && <DropdownMenuSeparator />}
          {editable && (
            <DropdownMenuItem variant="destructive" onSelect={() => onAction(entry, 'remove')}>
              <Trash2 aria-hidden="true" />
              {t('actions.remove')}
            </DropdownMenuItem>
          )}
        </DropdownMenuContent>
      </DropdownMenu>
    </div>
  )
}

export function NodeResource({
  library,
  nodes,
  totalCount,
  filters,
  onFiltersChange,
  latencyTests,
  highlight,
  nodeLatencies,
  loading,
  error,
  onRetry,
  dragging,
}: {
  library: LibraryNode[]
  nodes: LibraryNode[]
  totalCount: number
  filters: NodeFilters
  onFiltersChange: (value: NodeFilters) => void
  latencyTests: LatencyTests
  highlight?: boolean
  nodeLatencies?: Record<string, NodeLatencyProbeResult>
  loading?: boolean
  error?: boolean
  onRetry: () => void
  dragging: boolean
}) {
  const { t } = useTranslation()
  const runtime = useRuntimeNodes()
  const scrollRef = useRef<HTMLDivElement | null>(null)
  const [scrollTop, setScrollTop] = useState(0)
  const [viewportHeight, setViewportHeight] = useState(456)
  const [columnCount, setColumnCount] = useState(1)
  const [rowHeight, setRowHeight] = useState(NODE_ROW_HEIGHT)
  const [focusedId, setFocusedId] = useState<string | null>(null)
  const [detailNodeId, setDetailNodeId] = useState<string | null>(null)
  const detailNode = detailNodeId ? library.find(({ node }) => node.id === detailNodeId) || null : null
  const [showLink, setShowLink] = useState(false)
  const detailOriginRef = useRef<HTMLElement | null>(null)
  const [removingNode, setRemovingNode] = useState<LibraryNode | null>(null)
  const [openedQRCodeModal, setOpenedQRCodeModal] = useState(false)
  const [openedImportNodeFormModal, setOpenedImportNodeFormModal] = useState(false)
  const [openedConfigureNodeFormModal, setOpenedConfigureNodeFormModal] = useState(false)
  const [openedEditNodeFormModal, setOpenedEditNodeFormModal] = useState(false)
  const [editingNode, setEditingNode] = useState<{ id: string; link: string; tag: string; name: string }>()
  const [addingNode, setAddingNode] = useState<{ id: string; name: string; type: 'node' } | null>(null)
  const qrCodeModalRef = useRef<QRCodeModalRef>(null)
  const removeNodesMutation = useRemoveNodesMutation()
  const importNodesMutation = useImportNodesMutation()
  const filterKey = `${filters.search}\n${filters.source}\n${filters.sort}`
  useLayoutEffect(() => {
    const element = scrollRef.current
    if (!element) return
    const observer = new ResizeObserver(() => {
      setViewportHeight(element.clientHeight)
      // Each column remains a vertical droppable, preserving keyboard and pointer sorting.
      const columns = element.clientWidth >= 560 ? 2 : 1
      setColumnCount(columns)
      setRowHeight(
        Number.parseFloat(getComputedStyle(element).getPropertyValue('--node-row-height')) || NODE_ROW_HEIGHT,
      )
    })
    observer.observe(element)
    return () => observer.disconnect()
  }, [loading, error])
  useEffect(() => {
    if (scrollRef.current) scrollRef.current.scrollTop = 0
    setScrollTop(0)
    setFocusedId(null)
  }, [filterKey])
  const split = columnCount === 2 ? Math.ceil(nodes.length / 2) : nodes.length
  const columns = columnCount === 2 ? [nodes.slice(0, split), nodes.slice(split)] : [nodes]
  const onAction = (entry: LibraryNode, action: NodeAction) => {
    const { node } = entry
    const name = node.tag || node.name || ''
    if (action === 'details') {
      detailOriginRef.current = document.activeElement as HTMLElement | null
      setShowLink(false)
      setDetailNodeId(entry.node.id)
    } else if (action === 'edit') {
      setEditingNode({ id: node.id, link: node.link, tag: node.tag || '', name: node.name || '' })
      setOpenedEditNodeFormModal(true)
    } else if (action === 'qr') {
      qrCodeModalRef.current?.setProps({ name, link: node.link })
      setOpenedQRCodeModal(true)
    } else if (action === 'remove') {
      removeNodesMutation.reset()
      setRemovingNode(entry)
    } else setAddingNode({ id: node.id, name, type: 'node' })
  }
  return (
    <>
      <Section
        title={t('node')}
        count={totalCount}
        createLabel={t('panel.importNode')}
        icon={<Cloud className="h-5 w-5" />}
        iconPlus={<CloudUpload className="h-4 w-4" />}
        onCreate={() => setOpenedImportNodeFormModal(true)}
        loading={loading}
        error={error}
        onRetry={onRetry}
        bordered
        highlight={highlight}
        className="panel-node-section"
        contentClassName="min-h-0 flex-1"
        actions={
          <>
            <Button
              variant="ghost"
              size="icon"
              aria-label={t('latency.testManual')}
              disabled={latencyTests.busy || !library.length}
              onClick={() =>
                latencyTests.test(
                  library.map(({ node }) => node.id),
                  'manual',
                )
              }
            >
              <Gauge />
            </Button>
            <SimpleTooltip label={t('actions.configureNode')}>
              <Button
                variant="ghost"
                size="icon"
                aria-label={t('actions.configureNode')}
                onClick={() => setOpenedConfigureNodeFormModal(true)}
              >
                <FileInput aria-hidden="true" className="h-4 w-4" />
              </Button>
            </SimpleTooltip>
          </>
        }
      >
        <p className="shrink-0 text-xs text-muted-foreground">{t('panel.manualNodesHint')}</p>
        <LatencyStatus tests={latencyTests} scope="manual" />
        {totalCount > 0 && (
          <div className="shrink-0 space-y-2" data-node-toolbar>
            <label className="flex items-center gap-2 rounded-lg border bg-background px-3 focus-within:ring-2 focus-within:ring-ring">
              <Search aria-hidden="true" className="h-4 w-4 shrink-0 text-muted-foreground" />
              <span className="sr-only">{t('panel.searchNodes')}</span>
              <input
                type="search"
                autoComplete="off"
                spellCheck={false}
                name="node-search"
                value={filters.search}
                disabled={dragging}
                onChange={(event) => onFiltersChange({ ...filters, search: event.target.value })}
                placeholder={t('panel.searchPlaceholder')}
                className="h-10 w-full min-w-0 bg-transparent text-sm outline-none"
              />
            </label>
            <div className="grid gap-2">
              <label className="min-w-0">
                <span className="sr-only">{t('panel.sort')}</span>
                <select
                  className="panel-select"
                  value={filters.sort}
                  disabled={dragging}
                  onChange={(event) => onFiltersChange({ ...filters, sort: event.target.value as NodeFilters['sort'] })}
                >
                  <option value="custom">{t('panel.defaultSort')}</option>
                  <option value="latency">{t('panel.latencySort')}</option>
                </select>
              </label>
            </div>
            <div
              className="flex flex-wrap justify-between gap-1 text-xs text-muted-foreground"
              role="status"
              aria-live="polite"
            >
              <span>{t('panel.filteredNodes', { count: nodes.length, total: totalCount })}</span>
              {nodes.length > 0 && (filters.search || filters.source !== 'all') ? (
                <button
                  type="button"
                  className="text-primary hover:underline"
                  onClick={() => onFiltersChange(defaultNodeFilters)}
                >
                  {t('panel.clearFilters')}
                </button>
              ) : null}
            </div>
            {filters.sort === 'latency' && <p className="text-xs text-muted-foreground">{t('panel.filtersReorder')}</p>}
          </div>
        )}
        <div
          ref={scrollRef}
          data-workspace-scroll
          data-node-scroll
          tabIndex={0}
          onScroll={(event) => {
            if (event.currentTarget.clientHeight) setScrollTop(event.currentTarget.scrollTop)
          }}
          onFocusCapture={(event) =>
            setFocusedId((event.target as HTMLElement).closest('[data-node-id]')?.getAttribute('data-node-id') || null)
          }
          className="min-h-0 flex-1 overflow-y-auto overscroll-contain rounded-lg outline-offset-2"
        >
          <div
            className="manual-node-columns"
            data-columns={columnCount}
            style={{ gridTemplateColumns: `repeat(${columnCount}, minmax(0, 1fr))` }}
          >
            {columns.map((entries, column) => {
              const offset = column * split
              const range = nodeWindow(entries.length, scrollTop, viewportHeight, rowHeight)
              const indices = Array.from({ length: range.end - range.start }, (_, index) => range.start + index)
              const focusedIndex = entries.findIndex(({ node }) => node.id === focusedId)
              if (focusedIndex >= 0 && !indices.includes(focusedIndex)) indices.push(focusedIndex)
              indices.sort((a, b) => a - b)
              return (
                <Droppable
                  key={column}
                  droppableId={column ? `${NODE_DROPPABLE_ID}-column-1` : NODE_DROPPABLE_ID}
                  type="NODE"
                  mode="virtual"
                  renderClone={(provided, snapshot, rubric) => (
                    <NodeRow
                      entry={entries[rubric.source.index]}
                      activeGroups={runtime.active.get(entries[rubric.source.index].node.id)}
                      provided={provided}
                      snapshot={snapshot}
                      latency={nodeLatencies?.[entries[rubric.source.index].node.id]}
                      onAction={onAction}
                      tests={latencyTests}
                    />
                  )}
                >
                  {(provided, snapshot) => (
                    <div
                      ref={provided.innerRef}
                      {...provided.droppableProps}
                      data-group-column-offset={offset}
                      role="list"
                      aria-label={t('panel.nodeList')}
                      className={cn('relative min-w-0', snapshot.isDraggingOver && 'bg-primary/5')}
                      style={{
                        height: Math.max(1, entries.length + (snapshot.isUsingPlaceholder ? 1 : 0)) * rowHeight,
                      }}
                    >
                      {indices.map((index) => (
                        <div
                          key={entries[index].node.id}
                          className="absolute inset-x-0"
                          style={{ top: index * rowHeight, height: rowHeight }}
                        >
                          <Draggable draggableId={`node-${entries[index].node.id}`} index={index}>
                            {(rowProvided, rowSnapshot) => (
                              <NodeRow
                                entry={entries[index]}
                                activeGroups={runtime.active.get(entries[index].node.id)}
                                provided={rowProvided}
                                snapshot={rowSnapshot}
                                latency={nodeLatencies?.[entries[index].node.id]}
                                onAction={onAction}
                                tests={latencyTests}
                              />
                            )}
                          </Draggable>
                        </div>
                      ))}
                    </div>
                  )}
                </Droppable>
              )
            })}
          </div>
          {!loading && !error && !totalCount && (
            <ResourceEmpty
              title={t('panel.noNodes')}
              description={t('panel.noNodesDescription')}
              action={t('panel.importNode')}
              onAction={() => setOpenedImportNodeFormModal(true)}
            />
          )}
          {!loading && !error && totalCount > 0 && !nodes.length && (
            <ResourceEmpty
              title={t('panel.noMatches')}
              description={t('panel.noMatchesDescription')}
              action={t('panel.clearFilters')}
              onAction={() => onFiltersChange(defaultNodeFilters)}
            />
          )}
        </div>
      </Section>
      <Sheet
        open={!!detailNode}
        onOpenChange={(open) => {
          if (!open) setDetailNodeId(null)
        }}
      >
        <SheetContent
          className="w-full overflow-y-auto overscroll-contain sm:max-w-lg"
          onCloseAutoFocus={(event) => {
            event.preventDefault()
            if (detailOriginRef.current?.isConnected) detailOriginRef.current.focus({ preventScroll: true })
            else scrollRef.current?.focus({ preventScroll: true })
          }}
        >
          <SheetHeader className="pr-12">
            <SheetTitle>{t('panel.nodeDetails')}</SheetTitle>
            <SheetDescription className="break-words">{detailNode?.node.tag || detailNode?.node.name}</SheetDescription>
          </SheetHeader>
          {detailNode && (
            <div className="space-y-5 px-4 pb-6">
              <dl className="space-y-4 text-sm">
                <div>
                  <dt className="text-muted-foreground">{t('name')}</dt>
                  <dd className="mt-1 break-words">{detailNode.node.name}</dd>
                </div>
                {detailNode.node.tag && (
                  <div>
                    <dt className="text-muted-foreground">{t('tag')}</dt>
                    <dd className="mt-1 break-words">{detailNode.node.tag}</dd>
                  </div>
                )}
                <div>
                  <dt className="text-muted-foreground">{t('panel.protocol')}</dt>
                  <dd className="mt-1">{detailNode.node.protocol}</dd>
                </div>
                <div>
                  <dt className="text-muted-foreground">{t('panel.source')}</dt>
                  <dd className="mt-1 break-words">
                    {detailNode.sources.map((source) => source.name).join(' · ') || t('panel.manual')}
                  </dd>
                </div>
                <div>
                  <dt className="text-muted-foreground">{t('panel.nodeLatency')}</dt>
                  <dd className="mt-1">
                    <LatencyBadge result={nodeLatencies?.[detailNode.node.id]} />
                  </dd>
                </div>
              </dl>
              <NodeUDPTest key={detailNode.node.id} id={detailNode.node.id} />
              <div className="flex flex-wrap gap-2">
                <Button variant="outline" onClick={() => onAction(detailNode, 'join')}>
                  {t('panel.addToGroup')}
                </Button>
                {detailNode.editable && (
                  <Button variant="outline" onClick={() => onAction(detailNode, 'edit')}>
                    {t('actions.edit')}
                  </Button>
                )}
                <Button variant="outline" onClick={() => onAction(detailNode, 'qr')}>
                  {t('actions.viewQRCode')}
                </Button>
              </div>
              <div className="space-y-2">
                <Button variant="ghost" onClick={() => setShowLink((value) => !value)} aria-expanded={showLink}>
                  {showLink ? t('actions.hide') : t('actions.show sensitive')}
                </Button>
                {showLink && (
                  <p className="break-all rounded-lg bg-muted p-3 font-mono text-xs">{detailNode.node.link}</p>
                )}
              </div>
            </div>
          )}
        </SheetContent>
      </Sheet>
      <Dialog
        open={!!removingNode}
        onOpenChange={(open) => {
          if (!open && !removeNodesMutation.isPending) setRemovingNode(null)
        }}
      >
        <DialogContent>
          <DialogHeader>
            <DialogTitle>
              {t('actions.remove')} {removingNode?.node.tag || removingNode?.node.name}
            </DialogTitle>
          </DialogHeader>
          <p className="text-sm text-muted-foreground">{t('confirmModal.removeConfirmDescription')}</p>
          {removeNodesMutation.isError && (
            <p role="alert" className="text-sm text-destructive">
              {t('panel.saveFailed')}
            </p>
          )}
          <DialogFooter>
            <Button variant="outline" disabled={removeNodesMutation.isPending} onClick={() => setRemovingNode(null)}>
              {t('confirmModal.cancel')}
            </Button>
            <Button
              variant="destructive"
              loading={removeNodesMutation.isPending}
              onClick={async () => {
                if (!removingNode) return
                try {
                  await removeNodesMutation.mutateAsync([removingNode.node.id])
                  setRemovingNode(null)
                } catch {
                  /* Keep confirmation open for retry. */
                }
              }}
            >
              {t('confirmModal.confirm')}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
      <ResourceGroupPicker resource={addingNode} onClose={() => setAddingNode(null)} />
      <QRCodeModal ref={qrCodeModalRef} opened={openedQRCodeModal} onClose={() => setOpenedQRCodeModal(false)} />
      <ImportResourceFormModal
        resourceKind="node"
        title={t('node')}
        opened={openedImportNodeFormModal}
        onClose={() => setOpenedImportNodeFormModal(false)}
        handleSubmit={async (values) => {
          await importNodesMutation.mutateAsync(
            values.resources.map(({ link, tag }) => ({ link, tag: tag.trim() || undefined })),
          )
        }}
      />
      <ConfigureNodeFormModal
        opened={openedConfigureNodeFormModal}
        onClose={() => setOpenedConfigureNodeFormModal(false)}
      />
      <EditNodeFormModal
        opened={openedEditNodeFormModal}
        onClose={() => setOpenedEditNodeFormModal(false)}
        node={editingNode}
      />
    </>
  )
}

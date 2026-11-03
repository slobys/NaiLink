import type { NodeLatencyProbeResult } from '~/apis'
import type { LatencyTests } from '~/components/LatencyStatus'
import type { QRCodeModalRef } from '~/components/QRCodeModal'
import type { SubscriptionsQuery } from '~/schemas/gql/graphql'
import type { LibraryNode } from '~/utils/node-library'
import { Eye, Gauge, MoreHorizontal, Plus, Search } from 'lucide-react'
import { memo, useCallback, useMemo, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { LatencyBadge } from '~/components/LatencyBadge'
import { LatencyStatus } from '~/components/LatencyStatus'
import { NodeName } from '~/components/NodeName'
import { NodeUDPTest } from '~/components/NodeUDPTest'
import { QRCodeModal } from '~/components/QRCodeModal'
import { ResourceGroupPicker } from '~/components/ResourceGroupPicker'
import { Button } from '~/components/ui/button'
import { Dialog, DialogContent, DialogHeader, DialogTitle } from '~/components/ui/dialog'
import { DropdownMenu, DropdownMenuContent, DropdownMenuItem, DropdownMenuTrigger } from '~/components/ui/dropdown-menu'
import { Sheet, SheetContent, SheetDescription, SheetHeader, SheetTitle } from '~/components/ui/sheet'
import { buildNodeLibrary, defaultNodeFilters, filterNodeLibrary } from '~/utils/node-library'
import { nodeProtocolLabel } from '~/utils/node-protocol-label'

type SubscriptionNode = LibraryNode['node']

const SubscriptionNodeTile = memo(
  ({
    node,
    latency,
    pending,
    busy,
    onTest,
    onDetails,
    onJoin,
    onQR,
  }: {
    node: SubscriptionNode
    latency?: NodeLatencyProbeResult
    pending: boolean
    busy: boolean
    onTest: (ids: string[]) => Promise<void>
    onDetails: (node: SubscriptionNode) => void
    onJoin: (node: SubscriptionNode) => void
    onQR: (node: SubscriptionNode) => void
  }) => {
    const { t } = useTranslation()
    const name = node.tag || node.name || node.id
    return (
      <article role="listitem" data-name-card data-subscription-node={node.id} className="subscription-node-tile">
        <div className="subscription-node-heading">
          <button
            type="button"
            className="subscription-node-name min-w-0 text-left text-sm font-medium hover:text-primary"
            title={name}
            aria-label={`${t('panel.nodeDetails')} ${name}`}
            onClick={() => onDetails(node)}
          >
            <NodeName name={name} showRegion />
          </button>
          <span className="subscription-node-protocol" title={node.protocol}>
            {nodeProtocolLabel(node.protocol)}
          </span>
        </div>
        <div className="subscription-node-actions">
          <Button
            size="sm"
            variant="ghost"
            className="subscription-node-test disabled:opacity-100"
            loading={pending}
            disabled={busy}
            title={t('latency.testNode')}
            aria-label={`${t('latency.testNode')} ${name}`}
            onClick={() => onTest([node.id])}
          >
            {latency ? (
              <LatencyBadge result={latency} />
            ) : (
              <>
                <Gauge />
                <span>{t('latency.test')}</span>
              </>
            )}
          </Button>
          <Button
            size="sm"
            variant="ghost"
            className="subscription-node-secondary subscription-node-join"
            title={t('panel.addToGroup')}
            aria-label={t('panel.addToGroup')}
            onClick={() => onJoin(node)}
          >
            <Plus />
            <span className="sr-only">{t('panel.join')}</span>
          </Button>
          <DropdownMenu>
            <DropdownMenuTrigger asChild>
              <Button
                size="icon"
                variant="ghost"
                className="subscription-node-secondary"
                title={t('panel.moreActions')}
                aria-label={`${t('panel.moreActions')} ${name}`}
              >
                <MoreHorizontal />
              </Button>
            </DropdownMenuTrigger>
            <DropdownMenuContent align="end" className="subscription-actions-menu" data-subscription-node-menu>
              <DropdownMenuItem onSelect={() => onDetails(node)}>
                <Eye />
                {t('panel.nodeDetails')}
              </DropdownMenuItem>
              <DropdownMenuItem onSelect={() => onQR(node)}>
                <Eye />
                {t('actions.viewQRCode')}
              </DropdownMenuItem>
            </DropdownMenuContent>
          </DropdownMenu>
        </div>
      </article>
    )
  },
)

export function SubscriptionNodes({
  subscription,
  onClose,
  tests,
  latencies,
}: {
  subscription: SubscriptionsQuery['subscriptions'][number]
  onClose: () => void
  tests: LatencyTests
  latencies: Record<string, NodeLatencyProbeResult>
}) {
  const { t } = useTranslation()
  const scope = `subscription:${subscription.id}`
  const test = useCallback((ids: string[]) => tests.test(ids, scope), [tests.test, scope])
  const [search, setSearch] = useState('')
  const [sort, setSort] = useState<'custom' | 'latency'>('custom')
  const [adding, setAdding] = useState<{ id: string; name: string; type: 'node' } | null>(null)
  const [qrOpen, setQrOpen] = useState(false)
  const [detailId, setDetailId] = useState<string | null>(null)
  const [showLink, setShowLink] = useState(false)
  const qrRef = useRef<QRCodeModalRef>(null)
  const gridRef = useRef<HTMLDivElement>(null)
  const library = useMemo(() => buildNodeLibrary([], [subscription]), [subscription])
  const detail = library.find(({ node }) => node.id === detailId)?.node
  const nestedOpen = !!detail || qrOpen || !!adding
  const filtered = useMemo(
    () => filterNodeLibrary(library, { ...defaultNodeFilters, search, sort }, latencies),
    [library, search, sort, latencies],
  )
  const showDetails = useCallback((node: SubscriptionNode) => {
    setDetailId(node.id)
    setShowLink(false)
  }, [])
  const showQR = useCallback((node: SubscriptionNode) => {
    qrRef.current?.setProps({ name: node.tag || node.name || node.id, link: node.link })
    setQrOpen(true)
  }, [])
  const joinNode = useCallback(
    (node: SubscriptionNode) => setAdding({ id: node.id, name: node.tag || node.name || node.id, type: 'node' }),
    [],
  )
  const resetScroll = () => gridRef.current?.scrollTo({ top: 0 })
  return (
    <>
      <Dialog
        open
        onOpenChange={(open) => {
          if (open) return
          if (qrOpen) setQrOpen(false)
          else if (adding) setAdding(null)
          else if (detail) setDetailId(null)
          else {
            if (tests.busy && tests.scope === scope) tests.stop()
            onClose()
          }
        }}
      >
        <DialogContent
          className="subscription-browser"
          data-subscription-browser={subscription.id}
          onEscapeKeyDown={(event) => {
            if (document.querySelector('[data-subscription-node-menu]')) event.preventDefault()
          }}
          onInteractOutside={(event) => {
            if (
              nestedOpen ||
              (event.target as Element).closest('[data-slot="sheet-content"],[data-subscription-node-menu]')
            )
              event.preventDefault()
          }}
        >
          <DialogHeader className="shrink-0 pr-8 text-left">
            <DialogTitle className="break-words leading-snug">
              {subscription.tag || t('subscription')} · {t('panel.subscriptionNodes')}
            </DialogTitle>
            <p className="text-sm text-muted-foreground">{t('panel.subscriptionNodesHint')}</p>
          </DialogHeader>
          <div className="subscription-search-row grid shrink-0 gap-2 sm:grid-cols-[minmax(0,1fr)_auto]">
            <label className="flex min-w-0 items-center gap-2 rounded-lg border px-3">
              <Search className="h-4 w-4 shrink-0 text-muted-foreground" aria-hidden="true" />
              <span className="sr-only">{t('panel.searchNodes')}</span>
              <input
                type="search"
                value={search}
                onChange={(event) => {
                  setSearch(event.target.value)
                  resetScroll()
                }}
                placeholder={t('panel.searchPlaceholder')}
                className="h-11 w-full min-w-0 bg-transparent text-base outline-none"
              />
            </label>
            <Button
              variant="outline"
              disabled={tests.busy || !filtered.length}
              onClick={() => test(filtered.map(({ node }) => node.id))}
            >
              <Gauge />
              {t('latency.testFiltered')}
            </Button>
          </div>
          <div className="subscription-filters-row flex shrink-0 flex-wrap items-center gap-2">
            <label className="min-w-0 flex-1">
              <span className="sr-only">{t('panel.sort')}</span>
              <select
                className="panel-select"
                value={sort}
                onChange={(event) => {
                  setSort(event.target.value as typeof sort)
                  resetScroll()
                }}
              >
                <option value="custom">{t('panel.defaultSort')}</option>
                <option value="latency">{t('panel.latencySort')}</option>
              </select>
            </label>
            <span className="text-xs text-muted-foreground" role="status">
              {t('panel.filteredNodes', { count: filtered.length, total: library.length })}
            </span>
          </div>
          <LatencyStatus tests={tests} scope={scope} />
          <div
            className="subscription-node-grid"
            ref={gridRef}
            tabIndex={0}
            role="list"
            aria-label={t('panel.subscriptionNodes')}
          >
            {filtered.map(({ node }) => (
              <SubscriptionNodeTile
                key={node.id}
                node={node}
                latency={latencies[node.id]}
                pending={tests.pending.has(node.id)}
                busy={tests.busy}
                onTest={test}
                onDetails={showDetails}
                onQR={showQR}
                onJoin={joinNode}
              />
            ))}
            {!filtered.length && (
              <p className="col-span-full p-6 text-center text-sm text-muted-foreground">
                {library.length ? t('panel.noMatches') : t('panel.noNodes')}
              </p>
            )}
          </div>
        </DialogContent>
      </Dialog>
      <Sheet
        open={!!detail}
        onOpenChange={(open) => {
          if (!open) setDetailId(null)
        }}
      >
        <SheetContent className="w-full overflow-y-auto sm:max-w-lg">
          <SheetHeader className="pr-10">
            <SheetTitle>{t('panel.nodeDetails')}</SheetTitle>
            <SheetDescription className="break-words">{detail?.name}</SheetDescription>
          </SheetHeader>
          {detail && (
            <div className="space-y-4 px-4 pb-6">
              <dl className="space-y-3 text-sm">
                <div>
                  <dt className="text-muted-foreground">{t('panel.source')}</dt>
                  <dd>{subscription.tag}</dd>
                </div>
                <div>
                  <dt className="text-muted-foreground">{t('panel.protocol')}</dt>
                  <dd>{detail.protocol}</dd>
                </div>
                <div>
                  <dt className="text-muted-foreground">{t('panel.nodeLatency')}</dt>
                  <dd>
                    <LatencyBadge result={latencies[detail.id]} />
                  </dd>
                </div>
              </dl>
              <NodeUDPTest key={detail.id} id={detail.id} />
              <div className="flex flex-wrap gap-2">
                <Button
                  variant="outline"
                  disabled={tests.busy}
                  loading={tests.pending.has(detail.id)}
                  onClick={() => test([detail.id])}
                >
                  {t('latency.testNode')}
                </Button>
                <Button
                  variant="outline"
                  onClick={() => setAdding({ id: detail.id, name: detail.name || '', type: 'node' })}
                >
                  {t('panel.addToGroup')}
                </Button>
                <Button
                  variant="outline"
                  onClick={() => {
                    qrRef.current?.setProps({ name: detail.name || '', link: detail.link })
                    setQrOpen(true)
                  }}
                >
                  {t('actions.viewQRCode')}
                </Button>
              </div>
              <Button variant="ghost" aria-expanded={showLink} onClick={() => setShowLink((value) => !value)}>
                {showLink ? t('actions.hide') : t('actions.show sensitive')}
              </Button>
              {showLink && <p className="break-all rounded-lg bg-muted p-3 font-mono text-xs">{detail.link}</p>}
            </div>
          )}
        </SheetContent>
      </Sheet>
      <ResourceGroupPicker resource={adding} onClose={() => setAdding(null)} />
      <QRCodeModal ref={qrRef} opened={qrOpen} onClose={() => setQrOpen(false)} />
    </>
  )
}

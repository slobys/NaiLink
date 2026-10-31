import type { NodeLatencyProbeResult } from '~/apis'
import { Droppable } from '@hello-pangea/dnd'
import { useStore } from '@nanostores/react'
import { ChevronDown, Plus } from 'lucide-react'
import { useLayoutEffect, useMemo, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { LatencyBadge } from '~/components/LatencyBadge'
import { SortableResourceBadge } from '~/components/SortableResourceBadge'
import { Button } from '~/components/ui/button'
import { cn } from '~/lib/utils'
import { groupSortOrdersAtom } from '~/store'

interface GroupNode {
  id: string
  tag?: string | null
  name: string
  protocol?: string | null
  address?: string | null
  subscriptionID?: string | null
}

interface GroupSubscription {
  subscription: {
    id: string
    tag?: string | null
    link: string
  }
  nameFilterRegex?: string | null
  matchedCount: number
  matchedNodes: Array<{
    id: string
    name: string
    protocol?: string | null
  }>
}

interface Subscription {
  id: string
  tag?: string | null
}

function GroupDropZone({
  droppableId,
  type,
  label,
  count,
  collapsed,
  open,
  onOpen,
  onToggle,
  addButtonLabel,
  onAdd,
  children,
  showAddButtons,
  columns,
}: {
  droppableId: string
  type: 'NODE' | 'SUBSCRIPTION'
  label: string
  count: number
  collapsed?: boolean
  open: boolean
  onOpen: () => void
  onToggle: () => void
  addButtonLabel: string
  onAdd: () => void
  children: React.ReactNode[]
  showAddButtons: boolean
  columns: number
}) {
  const compact = collapsed || !open

  const split = columns === 2 && !compact ? Math.ceil(children.length / 2) : children.length
  return (
    <section className="min-w-0" data-group-section={type.toLowerCase()}>
      {(count > 0 || showAddButtons) && (
        <div className="flex min-h-8 items-center justify-between gap-2">
          <button
            type="button"
            className="flex min-h-8 min-w-0 flex-1 items-center gap-1.5 text-left text-xs font-medium"
            onClick={onToggle}
            aria-expanded={!compact}
          >
            <ChevronDown
              className={cn('h-3.5 w-3.5 shrink-0 text-muted-foreground transition-transform', compact && '-rotate-90')}
            />
            <span>
              {label} ({count})
            </span>
          </button>
          {showAddButtons && (
            <Button type="button" variant="ghost" size="xs" onClick={onAdd} aria-label={addButtonLabel}>
              <Plus className="h-3.5 w-3.5" />
            </Button>
          )}
        </div>
      )}
      <div
        className={cn('grid min-w-0 gap-1.5', columns === 2 && !compact && 'grid-cols-2')}
        data-group-columns={columns}
      >
        {[0, 1].map((column) => {
          const offset = column === 0 ? 0 : split
          const items = compact ? [] : column === 0 ? children.slice(0, split) : children.slice(split)
          return (
            <Droppable
              key={column}
              droppableId={column === 0 ? droppableId : droppableId.replace(/-(nodes|subscriptions)$/, '-column-1-$1')}
              type={type}
              isDropDisabled={column === 1 && (compact || columns === 1)}
            >
              {(provided, snapshot) => (
                <div
                  ref={provided.innerRef}
                  {...provided.droppableProps}
                  data-group-column-offset={offset}
                  data-group-column={column}
                  hidden={column === 1 && (compact || columns === 1)}
                  role={compact ? 'button' : undefined}
                  tabIndex={compact ? 0 : undefined}
                  aria-label={compact ? label : undefined}
                  className={cn(
                    'flex min-w-0 flex-col gap-1.5 rounded-md outline-none',
                    compact && 'h-1 cursor-pointer',
                    !count && 'min-h-1',
                    snapshot.isDraggingOver && 'min-h-8 bg-primary/5 ring-1 ring-primary/30',
                  )}
                  onClick={compact ? onOpen : undefined}
                  onKeyDown={
                    compact
                      ? (event) => {
                          if (event.key === 'Enter' || event.key === ' ') {
                            event.preventDefault()
                            onOpen()
                          }
                        }
                      : undefined
                  }
                >
                  {items}
                  {provided.placeholder}
                </div>
              )}
            </Droppable>
          )
        })}
      </div>
    </section>
  )
}

export function SortableGroupContent({
  groupId,
  nodes,
  subscriptions,
  nodeLatencies,
  allSubscriptions,
  autoExpandValue,
  collapsed,
  onExpand,
  onDelNode,
  onDelSubscription,
  onOpenAddNodes,
  onOpenAddSubscriptions,
  showAddButtons = true,
  disabled = false,
}: {
  groupId: string
  nodes: GroupNode[]
  subscriptions: GroupSubscription[]
  nodeLatencies?: Record<string, NodeLatencyProbeResult>
  allSubscriptions?: Subscription[]
  autoExpandValue?: string
  collapsed?: boolean
  onExpand: () => void
  onDelNode: (nodeId: string) => void
  onDelSubscription: (subscriptionId: string) => void
  onOpenAddNodes: () => void
  onOpenAddSubscriptions: () => void
  showAddButtons?: boolean
  disabled?: boolean
}) {
  const { t } = useTranslation()
  const groupSortOrders = useStore(groupSortOrdersAtom)
  const groupSortOrder = groupSortOrders[groupId] || { nodes: [], subscriptions: [] }
  const nodeSortOrder = groupSortOrder.nodes
  const subSortOrder = groupSortOrder.subscriptions

  const contentRef = useRef<HTMLDivElement>(null)
  const [columns, setColumns] = useState(1)
  useLayoutEffect(() => {
    const element = contentRef.current
    if (!element) return
    const observer = new ResizeObserver(([entry]) => setColumns(entry.contentRect.width >= 360 ? 2 : 1))
    observer.observe(element)
    return () => observer.disconnect()
  }, [])

  const [expandedSections, setExpandedSections] = useState<string[]>(['node', 'subscription'])

  const sortedNodeIds = useMemo(() => {
    const currentIds = nodes.map((node) => node.id)
    const currentIdSet = new Set(currentIds)
    const result = nodeSortOrder.filter((id) => currentIdSet.has(id))
    const resultSet = new Set(result)

    for (const id of currentIds) {
      if (!resultSet.has(id)) result.push(id)
    }

    return result
  }, [nodes, nodeSortOrder])

  const sortedSubscriptionIds = useMemo(() => {
    const currentIds = subscriptions.map((subscription) => subscription.subscription.id)
    const currentIdSet = new Set(currentIds)
    const result = subSortOrder.filter((id) => currentIdSet.has(id))
    const resultSet = new Set(result)

    for (const id of currentIds) {
      if (!resultSet.has(id)) result.push(id)
    }

    return result
  }, [subscriptions, subSortOrder])

  const effectiveExpandedSections = useMemo(() => {
    if (collapsed || !autoExpandValue || expandedSections.includes(autoExpandValue)) {
      return expandedSections
    }
    return [...expandedSections, autoExpandValue]
  }, [autoExpandValue, collapsed, expandedSections])

  const sortedNodes = useMemo(() => {
    const nodeMap = new Map(nodes.map((node) => [node.id, node]))
    return sortedNodeIds.map((id) => nodeMap.get(id)).filter(Boolean) as GroupNode[]
  }, [nodes, sortedNodeIds])

  const sortedSubscriptions = useMemo(() => {
    const subscriptionMap = new Map(subscriptions.map((subscription) => [subscription.subscription.id, subscription]))
    return sortedSubscriptionIds.map((id) => subscriptionMap.get(id)).filter(Boolean) as GroupSubscription[]
  }, [subscriptions, sortedSubscriptionIds])

  const toggleSection = (section: 'node' | 'subscription') => {
    setExpandedSections((current) =>
      current.includes(section) ? current.filter((value) => value !== section) : [...current, section],
    )
  }

  return (
    <div ref={contentRef} className="flex min-w-0 flex-col gap-1" data-group-content>
      <GroupDropZone
        showAddButtons={showAddButtons}
        droppableId={`${groupId}-nodes`}
        type="NODE"
        label={t('node')}
        count={nodes.length}
        collapsed={collapsed}
        open={effectiveExpandedSections.includes('node')}
        onOpen={() => {
          onExpand()
          setExpandedSections((current) => (current.includes('node') ? current : [...current, 'node']))
        }}
        onToggle={() => toggleSection('node')}
        addButtonLabel={t('groupPicker.openNodePicker')}
        onAdd={onOpenAddNodes}
        columns={columns}
      >
        {sortedNodes.map(({ id: nodeId, tag, name, protocol, address, subscriptionID }, index) => (
          <SortableResourceBadge
            disabled={disabled}
            key={nodeId}
            id={`${groupId}-node-${nodeId}`}
            index={columns === 2 ? index % Math.ceil(sortedNodes.length / 2) : index}
            name={tag || name}
            protocol={protocol}
            address={address}
            meta={nodeLatencies?.[nodeId] ? <LatencyBadge result={nodeLatencies[nodeId]} /> : undefined}
            onRemove={() => onDelNode(nodeId)}
          >
            {subscriptionID && allSubscriptions?.find((subscription) => subscription.id === subscriptionID)?.tag}
          </SortableResourceBadge>
        ))}
      </GroupDropZone>

      <GroupDropZone
        showAddButtons={showAddButtons}
        droppableId={`${groupId}-subscriptions`}
        type="SUBSCRIPTION"
        label={t('groupPicker.subscriptionGroupLabel')}
        count={subscriptions.length}
        collapsed={collapsed}
        open={effectiveExpandedSections.includes('subscription')}
        onOpen={() => {
          onExpand()
          setExpandedSections((current) => (current.includes('subscription') ? current : [...current, 'subscription']))
        }}
        onToggle={() => toggleSection('subscription')}
        addButtonLabel={t('groupPicker.openSubscriptionPicker')}
        onAdd={onOpenAddSubscriptions}
        columns={columns}
      >
        {sortedSubscriptions.map(({ subscription, nameFilterRegex, matchedCount, matchedNodes }, index) => (
          <SortableResourceBadge
            disabled={disabled}
            key={subscription.id}
            id={`${groupId}-sub-${subscription.id}`}
            index={columns === 2 ? index % Math.ceil(sortedSubscriptions.length / 2) : index}
            name={subscription.tag || subscription.link}
            meta={
              nameFilterRegex
                ? `${t('groupPicker.subscriptionPreviewMatchedCount', { count: matchedCount })} · /${nameFilterRegex}/`
                : t('groupPicker.subscriptionPreviewMatchedCount', { count: matchedCount })
            }
            onRemove={() => onDelSubscription(subscription.id)}
          >
            <div className="flex flex-col gap-1 text-xs">
              {nameFilterRegex && <span>{t('groupPicker.subscriptionRegexTooltip', { regex: nameFilterRegex })}</span>}
              {matchedNodes.length > 0 && (
                <span className="opacity-80">
                  {matchedNodes
                    .slice(0, 5)
                    .map((node) => node.name)
                    .join(', ')}
                  {matchedNodes.length > 5 && ` +${matchedNodes.length - 5}`}
                </span>
              )}
            </div>
          </SortableResourceBadge>
        ))}
      </GroupDropZone>
    </div>
  )
}

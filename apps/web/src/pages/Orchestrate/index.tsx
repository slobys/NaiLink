import type { DragUpdate, DropResult } from '@hello-pangea/dnd'
import type { DraggingResource } from '~/constants'
import type { GroupsQuery, SubscriptionsQuery } from '~/schemas/gql/graphql'
import { ChevronDown, SlidersHorizontal } from 'lucide-react'
import { useTranslation } from 'react-i18next'
import {
  buildNodeLibrary,
  defaultNodeFilters,
  filterNodeLibrary,
  orderNodeLibrary,
  reorderVisibleNodes,
} from '~/utils/node-library'
import type { LibraryNode } from '~/utils/node-library'
import { DragDropContext } from '@hello-pangea/dnd'
import { useStore } from '@nanostores/react'
import { useCallback, useEffect, useLayoutEffect, useMemo, useRef, useState } from 'react'
import {
  useConfigsQuery,
  useGroupAddNodesMutation,
  useGroupAddSubscriptionsMutation,
  useGroupDelNodesMutation,
  useGroupsQuery,
  useNodeLatenciesQuery,
  useNodesQuery,
  useSubscriptionsQuery,
} from '~/apis'
import type { NodeLatencyProbeResult } from '~/apis'
import { DraggableResourceType } from '~/constants'
import { appStateAtom, groupSortOrdersAtom } from '~/store'
import { deriveTime } from '~/utils'
import { groupColumnBase, groupColumnLocations } from '~/utils/group-columns'
import { useLatencyTests } from '~/hooks/useLatencyTests'
import { RouterLogs } from '~/pages/RouterLogs'
import { Config } from './Config'
import { DNS } from './DNS'
import { GroupResource } from './Group'
import { NODE_DROPPABLE_ID, NodeResource } from './Node'
import { Routing } from './Routing'
import { SubscriptionResource } from './Subscription'
import { TrafficOverview } from './TrafficOverview'
import { ConnectivityOverview } from '~/components/ConnectivityOverview'

function arrayMove<T>(array: T[], from: number, to: number): T[] {
  const newArray = [...array]
  const [removed] = newArray.splice(from, 1)
  newArray.splice(to, 0, removed)
  return newArray
}

export function OrchestratePage({ area = 'all' }: { area?: 'all' | 'dashboard' | 'resources' | 'advanced' | 'logs' }) {
  const commonVisible = area !== 'advanced' && area !== 'logs'
  const { t } = useTranslation()
  const [advancedOpen, setAdvancedOpen] = useState(false)
  const [activeArea, setActiveArea] = useState('panel-common')
  const [nodeFilters, setNodeFilters] = useState(defaultNodeFilters)
  const [mobilePanel, setMobilePanel] = useState('nodes')
  const workspaceRef = useRef<HTMLDivElement>(null)
  useLayoutEffect(() => {
    const element = workspaceRef.current
    if (!element) return
    const resize = () => {
      const top = element.getBoundingClientRect().top + window.scrollY
      const height = `${Math.max(320, Math.min(760, window.innerHeight - top - 24))}px`
      if (element.style.getPropertyValue('--workspace-height') !== height)
        element.style.setProperty('--workspace-height', height)
      const monitor = document.getElementById('panel-monitor')
      if (area === 'dashboard' && monitor) {
        const room = `${Math.max(360, window.innerHeight - monitor.getBoundingClientRect().top - 20)}px`
        if (monitor.style.getPropertyValue('--monitor-room') !== room) monitor.style.setProperty('--monitor-room', room)
      }
    }
    const observer = new ResizeObserver(resize)
    observer.observe(element.parentElement!)
    const header = document.querySelector('header')
    if (header) observer.observe(header)
    window.addEventListener('resize', resize)
    resize()
    return () => {
      observer.disconnect()
      window.removeEventListener('resize', resize)
    }
  }, [area])
  const scrollToArea = (id: string) => {
    if (id === 'panel-advanced') setAdvancedOpen(true)
    setActiveArea(id)
    requestAnimationFrame(() => {
      const element = document.getElementById(id)
      element?.scrollIntoView({
        behavior: window.matchMedia('(prefers-reduced-motion: reduce)').matches ? 'instant' : 'smooth',
        block: 'start',
      })
      element?.focus({ preventScroll: true })
    })
  }
  const { data: configsQuery } = useConfigsQuery()
  const nodesRequest = useNodesQuery(commonVisible)
  const nodesQuery = nodesRequest.data
  const { data: groupsQuery } = useGroupsQuery(commonVisible)
  const subscriptionsRequest = useSubscriptionsQuery(commonVisible)
  const subscriptionsQuery = subscriptionsRequest.data

  const groupAddNodesMutation = useGroupAddNodesMutation()
  const groupAddSubscriptionsMutation = useGroupAddSubscriptionsMutation()
  const groupDelNodesMutation = useGroupDelNodesMutation()
  const latencyTests = useLatencyTests()

  const [draggingResource, setDraggingResource] = useState<DraggingResource | null>(null)
  const [isDragging, setIsDragging] = useState(false)
  const [dragNodes, setDragNodes] = useState<LibraryNode[] | null>(null)
  const [dragDestinationDroppableId, setDragDestinationDroppableId] = useState<string | null>(null)
  const [hoveredGroupId, setHoveredGroupId] = useState<string | null>(null)
  const autoScrollFrameRef = useRef<number | null>(null)
  const draggingActiveRef = useRef(false)
  const edgeAutoScrollEnabledRef = useRef(false)
  const dragPointerRef = useRef<{ x: number; y: number } | null>(null)
  const hoveredGroupIdRef = useRef<string | null>(null)

  // Use persistent store for sort order
  const appState = useStore(appStateAtom)
  const nodeSortOrder = appState.nodeSortableKeys as string[]
  const subscriptionSortOrder = appState.subscriptionSortableKeys as string[]

  const setNodeSortOrder = useCallback((order: string[]) => {
    appStateAtom.setKey('nodeSortableKeys', order)
  }, [])

  const setSubscriptionSortOrder = useCallback((order: string[]) => {
    appStateAtom.setKey('subscriptionSortableKeys', order)
  }, [])

  const setGroupSortOrder = useCallback((order: string[]) => {
    appStateAtom.setKey('groupSortableKeys', order)
  }, [])

  // Get nodes from query (memoized to avoid dependency issues)
  const nodes = useMemo(() => nodesQuery?.nodes.edges ?? [], [nodesQuery?.nodes.edges])
  const groups = useMemo(() => groupsQuery?.groups ?? [], [groupsQuery?.groups])
  const subscriptions = useMemo(() => subscriptionsQuery?.subscriptions ?? [], [subscriptionsQuery?.subscriptions])
  const getGroupById = useCallback(
    (groupId: string) => groupsQuery?.groups.find((group: GroupsQuery['groups'][number]) => group.id === groupId),
    [groupsQuery?.groups],
  )
  const getGroupSubscriptionBinding = useCallback(
    (groupId: string, subscriptionId: string) =>
      getGroupById(groupId)?.subscriptions.find(
        (binding: GroupsQuery['groups'][number]['subscriptions'][number]) => binding.subscription.id === subscriptionId,
      ),
    [getGroupById],
  )
  const hasGroupSubscription = useCallback(
    (groupId: string, subscriptionId: string) => !!getGroupSubscriptionBinding(groupId, subscriptionId),
    [getGroupSubscriptionBinding],
  )
  const selectedConfig = useMemo(() => configsQuery?.configs.find((config) => config.selected), [configsQuery?.configs])
  const nodeLatencyRefetchIntervalMs = useMemo(() => {
    const configuredInterval = selectedConfig?.global.checkInterval
    if (!configuredInterval) return 30_000

    const ms = deriveTime(configuredInterval, 'ms')
    return Math.max(1_000, Number.isFinite(ms) ? ms : 30_000)
  }, [selectedConfig?.global.checkInterval])
  const nodeLatenciesQuery = useNodeLatenciesQuery(nodeLatencyRefetchIntervalMs, commonVisible)
  const nodeLatencies = useMemo<Record<string, NodeLatencyProbeResult>>(
    () => Object.fromEntries((nodeLatenciesQuery.data ?? []).map((result) => [result.id, result])),
    [nodeLatenciesQuery.data],
  )

  const nodeLibrary = useMemo(
    () => orderNodeLibrary(buildNodeLibrary(nodes, []), nodeSortOrder),
    [nodes, nodeSortOrder],
  )
  const sortedNodeIds = useMemo(() => nodeLibrary.map((entry) => entry.node.id), [nodeLibrary])
  const filteredNodes = useMemo(
    () => filterNodeLibrary(nodeLibrary, nodeFilters, nodeLatencies),
    [nodeLibrary, nodeFilters, nodeLatencies],
  )
  const visibleNodes = dragNodes || filteredNodes

  // Get sorted subscription IDs
  const sortedSubscriptionIds = useMemo(() => {
    if (subscriptions.length === 0) return []
    const currentIds = subscriptions.map((s: SubscriptionsQuery['subscriptions'][number]) => s.id)
    const currentIdSet = new Set(currentIds)

    const result = subscriptionSortOrder.filter((id) => currentIdSet.has(id))
    const resultSet = new Set(result)

    for (const id of currentIds) {
      if (!resultSet.has(id)) {
        result.push(id)
      }
    }

    return result
  }, [subscriptions, subscriptionSortOrder])

  // Get sorted subscriptions
  const sortedSubscriptions = useMemo(() => {
    if (subscriptions.length === 0) return []
    const subMap = new Map(subscriptions.map((s: SubscriptionsQuery['subscriptions'][number]) => [s.id, s]))
    return sortedSubscriptionIds.map((id) => subMap.get(id)).filter(Boolean) as typeof subscriptions
  }, [subscriptions, sortedSubscriptionIds])

  const groupSortOrder = appState.groupSortableKeys as string[]

  const sortedGroupIds = useMemo(() => {
    if (groups.length === 0) return []
    const currentIds = groups.map((group: GroupsQuery['groups'][number]) => group.id)
    const currentIdSet = new Set(currentIds)
    const result = groupSortOrder.filter((id) => currentIdSet.has(id))
    const resultSet = new Set(result)

    for (const id of currentIds) {
      if (!resultSet.has(id)) {
        result.push(id)
      }
    }

    return result
  }, [groupSortOrder, groups])

  // Helper to parse group item IDs (format: groupId-node-nodeId or groupId-sub-subId)
  const parseGroupItemId = useCallback(
    (id: string): { groupId: string; type: 'node' | 'sub'; itemId: string } | null => {
      const nodeMatch = id.match(/^(.+)-node-(.+)$/)
      if (nodeMatch) {
        return { groupId: nodeMatch[1], type: 'node', itemId: nodeMatch[2] }
      }
      const subMatch = id.match(/^(.+)-sub-(.+)$/)
      if (subMatch) {
        return { groupId: subMatch[1], type: 'sub', itemId: subMatch[2] }
      }
      return null
    },
    [],
  )

  // Helper to update group sort order
  const updateGroupSortOrder = useCallback((groupId: string, type: 'nodes' | 'subscriptions', newOrder: string[]) => {
    const currentOrders = groupSortOrdersAtom.get()
    groupSortOrdersAtom.set({
      ...currentOrders,
      [groupId]: {
        nodes: type === 'nodes' ? newOrder : currentOrders[groupId]?.nodes || [],
        subscriptions: type === 'subscriptions' ? newOrder : currentOrders[groupId]?.subscriptions || [],
      },
    })
  }, [])

  // Get sorted IDs for a group
  const getGroupSortedIds = useCallback((groupId: string, type: 'nodes' | 'subscriptions', currentIds: string[]) => {
    const groupSortOrders = groupSortOrdersAtom.get()
    const sortOrder = groupSortOrders[groupId]?.[type] || []
    const currentIdSet = new Set(currentIds)

    const result = sortOrder.filter((id) => currentIdSet.has(id))
    const resultSet = new Set(result)

    for (const id of currentIds) {
      if (!resultSet.has(id)) {
        result.push(id)
      }
    }

    return result
  }, [])

  const stopEdgeAutoScroll = useCallback(() => {
    if (autoScrollFrameRef.current !== null) {
      window.cancelAnimationFrame(autoScrollFrameRef.current)
      autoScrollFrameRef.current = null
    }
  }, [])

  const tickEdgeAutoScroll = useCallback(() => {
    autoScrollFrameRef.current = null

    if (!draggingActiveRef.current || !edgeAutoScrollEnabledRef.current || !dragPointerRef.current) return

    const { x, y: pointerY } = dragPointerRef.current
    const target = document
      .elementFromPoint(x, pointerY)
      ?.closest('[data-node-scroll], .panel-side-scroll') as HTMLElement | null
    const bounds = target?.getBoundingClientRect()
    const top = bounds?.top ?? 0
    const bottom = bounds?.bottom ?? window.innerHeight
    const threshold = Math.min(64, Math.max(24, (bottom - top) * 0.15))
    let delta = 0

    if (pointerY < top + threshold) {
      const intensity = Math.min(1, (top + threshold - pointerY) / threshold)
      delta = -Math.round(4 + intensity * intensity * 20)
    } else if (pointerY > bottom - threshold) {
      const intensity = Math.min(1, (pointerY - (bottom - threshold)) / threshold)
      delta = Math.round(4 + intensity * intensity * 20)
    }

    if (delta === 0) return

    if (target) target.scrollBy(0, delta)
    else window.scrollBy(0, delta)
    autoScrollFrameRef.current = window.requestAnimationFrame(tickEdgeAutoScroll)
  }, [])

  const ensureEdgeAutoScroll = useCallback(() => {
    if (autoScrollFrameRef.current === null) {
      autoScrollFrameRef.current = window.requestAnimationFrame(tickEdgeAutoScroll)
    }
  }, [tickEdgeAutoScroll])

  useEffect(() => {
    draggingActiveRef.current = isDragging

    if (!isDragging) {
      edgeAutoScrollEnabledRef.current = false
      hoveredGroupIdRef.current = null
      setHoveredGroupId(null)
      stopEdgeAutoScroll()
      return
    }

    edgeAutoScrollEnabledRef.current = true

    const handlePointerMove = (event: MouseEvent | PointerEvent) => {
      dragPointerRef.current = {
        x: event.clientX,
        y: event.clientY,
      }

      const hoveredCard = document.elementFromPoint(event.clientX, event.clientY)?.closest('[data-group-card-id]')
      const nextHoveredGroupId = hoveredCard?.getAttribute('data-group-card-id') ?? null

      if (hoveredGroupIdRef.current !== nextHoveredGroupId) {
        hoveredGroupIdRef.current = nextHoveredGroupId
        setHoveredGroupId(nextHoveredGroupId)
      }

      if (edgeAutoScrollEnabledRef.current) {
        ensureEdgeAutoScroll()
      }
    }

    window.addEventListener('mousemove', handlePointerMove, { passive: true })
    window.addEventListener('pointermove', handlePointerMove, { passive: true })

    return () => {
      window.removeEventListener('mousemove', handlePointerMove)
      window.removeEventListener('pointermove', handlePointerMove)
    }
  }, [ensureEdgeAutoScroll, isDragging, stopEdgeAutoScroll])

  useEffect(() => () => stopEdgeAutoScroll(), [stopEdgeAutoScroll])

  const onDragStart = (start: { draggableId: string; source: { droppableId: string } }) => {
    const draggableId = start.draggableId
    const droppableId = groupColumnBase(start.source.droppableId)

    setIsDragging(true)
    setDragNodes(filteredNodes)
    setDragDestinationDroppableId(null)
    dragPointerRef.current = null
    hoveredGroupIdRef.current = null
    setHoveredGroupId(null)
    edgeAutoScrollEnabledRef.current = true
    ensureEdgeAutoScroll()

    // Determine the type based on droppableId
    if (droppableId === 'group-list') {
      return
    } else if (droppableId === 'node-list') {
      const nodeId = draggableId.replace('node-', '')
      setDraggingResource({ type: DraggableResourceType.node, nodeID: nodeId })
    } else if (droppableId === 'subscription-list') {
      const subId = draggableId.replace('subscription-', '')
      setDraggingResource({ type: DraggableResourceType.subscription, subscriptionID: subId })
    } else if (droppableId.startsWith('subscription-') && droppableId.endsWith('-nodes')) {
      // Subscription node dragged from a subscription's node list
      const nodeId = draggableId.replace('subscription-node-', '')
      setDraggingResource({ type: DraggableResourceType.subscription_node, nodeID: nodeId })
    } else if (droppableId.endsWith('-nodes')) {
      const groupId = droppableId.replace('-nodes', '')
      const parsed = parseGroupItemId(draggableId)
      if (parsed) {
        setDraggingResource({ type: DraggableResourceType.groupNode, nodeID: parsed.itemId, groupID: groupId })
      }
    } else if (droppableId.endsWith('-subscriptions')) {
      const groupId = droppableId.replace('-subscriptions', '')
      const parsed = parseGroupItemId(draggableId)
      if (parsed) {
        setDraggingResource({
          type: DraggableResourceType.groupSubscription,
          subscriptionID: parsed.itemId,
          groupID: groupId,
        })
      }
    }
  }

  const onDragUpdate = useCallback(
    (update: DragUpdate) => {
      const nextDroppableId = update.destination ? groupColumnBase(update.destination.droppableId) : null
      setDragDestinationDroppableId((current) => (current === nextDroppableId ? current : nextDroppableId))
      ensureEdgeAutoScroll()
    },
    [ensureEdgeAutoScroll],
  )

  const onDragEnd = (result: DropResult) => {
    const { draggableId } = result
    const { source, destination } = groupColumnLocations(result.source, result.destination, (id) => {
      const column = document.querySelector(`[data-rfd-droppable-id="${CSS.escape(id)}"]`)
      return Number(column?.getAttribute('data-group-column-offset')) || 0
    })
    const pointer = dragPointerRef.current
    const fallbackGroupId = pointer
      ? document
          .elementFromPoint(pointer.x, pointer.y)
          ?.closest('[data-group-card-id]')
          ?.getAttribute('data-group-card-id') || null
      : hoveredGroupIdRef.current

    setIsDragging(false)
    setDragNodes(null)
    setDraggingResource(null)
    setDragDestinationDroppableId(null)
    setHoveredGroupId(null)
    hoveredGroupIdRef.current = null
    edgeAutoScrollEnabledRef.current = false
    stopEdgeAutoScroll()

    const sourceDroppableId = source.droppableId
    const destDroppableId = destination?.droppableId

    if (sourceDroppableId === 'group-list' && destDroppableId === 'group-list' && destination) {
      if (source.index !== destination.index) {
        setGroupSortOrder(arrayMove(sortedGroupIds, source.index, destination.index))
      }
      return
    }

    // A collapsed target can retain its pre-expansion dimensions in the drag
    // engine. Prefer the card actually under the pointer over that stale result.
    const pointerTargetsGroup =
      fallbackGroupId &&
      sourceDroppableId !== 'group-list' &&
      !destination?.droppableId.startsWith(`${fallbackGroupId}-`)
    if (!destination || pointerTargetsGroup) {
      if (fallbackGroupId) {
        if (sourceDroppableId === 'subscription-list') {
          const subId = draggableId.replace('subscription-', '')
          const targetGroup = getGroupById(fallbackGroupId)
          if (targetGroup && !hasGroupSubscription(fallbackGroupId, subId)) {
            groupAddSubscriptionsMutation.mutate({ id: fallbackGroupId, subscriptionIDs: [subId] })
            return
          }
        }

        if (sourceDroppableId === 'node-list') {
          const nodeId = draggableId.replace('node-', '')
          const targetGroup = groupsQuery?.groups.find(
            (group: GroupsQuery['groups'][number]) => group.id === fallbackGroupId,
          )
          if (
            targetGroup &&
            !targetGroup.nodes.find((node: GroupsQuery['groups'][number]['nodes'][number]) => node.id === nodeId)
          ) {
            groupAddNodesMutation.mutate({ id: fallbackGroupId, nodeIDs: [nodeId] })
            return
          }
        }

        if (
          sourceDroppableId.startsWith('subscription-') &&
          sourceDroppableId.endsWith('-nodes') &&
          sourceDroppableId !== 'subscription-list'
        ) {
          const nodeId = draggableId.replace('subscription-node-', '')
          const targetGroup = groupsQuery?.groups.find(
            (group: GroupsQuery['groups'][number]) => group.id === fallbackGroupId,
          )
          if (
            targetGroup &&
            !targetGroup.nodes.find((node: GroupsQuery['groups'][number]['nodes'][number]) => node.id === nodeId)
          ) {
            groupAddNodesMutation.mutate({ id: fallbackGroupId, nodeIDs: [nodeId] })
            return
          }
        }

        if (sourceDroppableId.endsWith('-nodes')) {
          const sourceGroupId = sourceDroppableId.replace('-nodes', '')
          const parsed = parseGroupItemId(draggableId)
          if (parsed && sourceGroupId !== fallbackGroupId) {
            const targetGroup = groupsQuery?.groups.find(
              (group: GroupsQuery['groups'][number]) => group.id === fallbackGroupId,
            )
            if (
              targetGroup &&
              !targetGroup.nodes.find(
                (node: GroupsQuery['groups'][number]['nodes'][number]) => node.id === parsed.itemId,
              )
            ) {
              groupAddNodesMutation.mutate({ id: fallbackGroupId, nodeIDs: [parsed.itemId] })
              return
            }
          }
        }

        if (sourceDroppableId.endsWith('-subscriptions')) {
          const sourceGroupId = sourceDroppableId.replace('-subscriptions', '')
          const parsed = parseGroupItemId(draggableId)
          if (parsed && sourceGroupId !== fallbackGroupId) {
            const targetGroup = getGroupById(fallbackGroupId)
            const sourceBinding = getGroupSubscriptionBinding(sourceGroupId, parsed.itemId)
            if (targetGroup && !hasGroupSubscription(fallbackGroupId, parsed.itemId)) {
              groupAddSubscriptionsMutation.mutate({
                id: fallbackGroupId,
                subscriptionIDs: [parsed.itemId],
                nameFilterRegex: sourceBinding?.nameFilterRegex ?? null,
              })
              return
            }
          }
        }
      }

      return
    }

    const confirmedDestDroppableId = destination.droppableId

    // Handle node list sorting
    if (sourceDroppableId === 'node-list' && confirmedDestDroppableId === 'node-list') {
      if (source.index !== destination.index) {
        if (nodeFilters.sort === 'custom')
          setNodeSortOrder(
            reorderVisibleNodes(
              sortedNodeIds,
              visibleNodes.map((entry) => entry.node.id),
              source.index,
              destination.index,
            ),
          )
      }
      return
    }

    // Handle subscription list sorting
    if (sourceDroppableId === 'subscription-list' && confirmedDestDroppableId === 'subscription-list') {
      if (source.index !== destination.index) {
        setSubscriptionSortOrder(arrayMove(sortedSubscriptionIds, source.index, destination.index))
      }
      return
    }

    // Check if source is a subscription's node list (format: subscription-{id}-nodes)
    const isFromSubscriptionNodes =
      sourceDroppableId.startsWith('subscription-') &&
      sourceDroppableId.endsWith('-nodes') &&
      sourceDroppableId !== 'subscription-list'

    // Handle dropping subscription node to group
    if (isFromSubscriptionNodes && confirmedDestDroppableId.endsWith('-nodes')) {
      const nodeId = draggableId.replace('subscription-node-', '')
      const targetGroupId = confirmedDestDroppableId.replace('-nodes', '')
      const targetGroup = groupsQuery?.groups.find((g: GroupsQuery['groups'][number]) => g.id === targetGroupId)

      if (
        targetGroup &&
        !targetGroup.nodes.find((n: GroupsQuery['groups'][number]['nodes'][number]) => n.id === nodeId)
      ) {
        groupAddNodesMutation.mutate({ id: targetGroupId, nodeIDs: [nodeId] })
      }
      return
    }

    // Handle group node sorting within same group OR cross-group drag
    if (sourceDroppableId.endsWith('-nodes') && confirmedDestDroppableId.endsWith('-nodes')) {
      const sourceGroupId = sourceDroppableId.replace('-nodes', '')
      const destGroupId = confirmedDestDroppableId.replace('-nodes', '')

      if (sourceGroupId === destGroupId) {
        // Same group sorting
        if (source.index !== destination.index) {
          const group = groupsQuery?.groups.find((g: GroupsQuery['groups'][number]) => g.id === sourceGroupId)
          if (group) {
            const currentIds = group.nodes.map((n: GroupsQuery['groups'][number]['nodes'][number]) => n.id)
            const sortedIds = getGroupSortedIds(sourceGroupId, 'nodes', currentIds)
            updateGroupSortOrder(sourceGroupId, 'nodes', arrayMove(sortedIds, source.index, destination.index))
          }
        }
      } else {
        // Cross-group drag - add node to target group
        const parsed = parseGroupItemId(draggableId)
        if (parsed) {
          const targetGroup = groupsQuery?.groups.find((g: GroupsQuery['groups'][number]) => g.id === destGroupId)
          if (
            targetGroup &&
            !targetGroup.nodes.find((n: GroupsQuery['groups'][number]['nodes'][number]) => n.id === parsed.itemId)
          ) {
            groupAddNodesMutation.mutate({ id: destGroupId, nodeIDs: [parsed.itemId] })
          }
        }
      }
      return
    }

    // Handle group subscription sorting within same group OR cross-group drag
    if (sourceDroppableId.endsWith('-subscriptions') && confirmedDestDroppableId.endsWith('-subscriptions')) {
      const sourceGroupId = sourceDroppableId.replace('-subscriptions', '')
      const destGroupId = confirmedDestDroppableId.replace('-subscriptions', '')

      if (sourceGroupId === destGroupId) {
        // Same group sorting
        if (source.index !== destination.index) {
          const group = groupsQuery?.groups.find((g: GroupsQuery['groups'][number]) => g.id === sourceGroupId)
          if (group) {
            const currentIds = group.subscriptions.map(
              (s: GroupsQuery['groups'][number]['subscriptions'][number]) => s.subscription.id,
            )
            const sortedIds = getGroupSortedIds(sourceGroupId, 'subscriptions', currentIds)
            updateGroupSortOrder(sourceGroupId, 'subscriptions', arrayMove(sortedIds, source.index, destination.index))
          }
        }
      } else {
        // Cross-group drag - add subscription to target group
        const parsed = parseGroupItemId(draggableId)
        if (parsed) {
          const targetGroup = getGroupById(destGroupId)
          const sourceBinding = getGroupSubscriptionBinding(sourceGroupId, parsed.itemId)
          if (targetGroup && !hasGroupSubscription(destGroupId, parsed.itemId)) {
            groupAddSubscriptionsMutation.mutate({
              id: destGroupId,
              subscriptionIDs: [parsed.itemId],
              nameFilterRegex: sourceBinding?.nameFilterRegex ?? null,
            })
          }
        }
      }
      return
    }

    // Handle dropping node from node-list to group
    if (sourceDroppableId === 'node-list' && confirmedDestDroppableId.endsWith('-nodes')) {
      const nodeId = draggableId.replace('node-', '')
      const targetGroupId = confirmedDestDroppableId.replace('-nodes', '')
      const targetGroup = groupsQuery?.groups.find((g: GroupsQuery['groups'][number]) => g.id === targetGroupId)

      if (
        targetGroup &&
        !targetGroup.nodes.find((n: GroupsQuery['groups'][number]['nodes'][number]) => n.id === nodeId)
      ) {
        groupAddNodesMutation.mutate({ id: targetGroupId, nodeIDs: [nodeId] })
      }
      return
    }

    // Handle dropping subscription from subscription-list to group
    if (sourceDroppableId === 'subscription-list' && confirmedDestDroppableId.endsWith('-subscriptions')) {
      const subId = draggableId.replace('subscription-', '')
      const targetGroupId = confirmedDestDroppableId.replace('-subscriptions', '')
      const targetGroup = getGroupById(targetGroupId)

      if (targetGroup && !hasGroupSubscription(targetGroupId, subId)) {
        groupAddSubscriptionsMutation.mutate({ id: targetGroupId, subscriptionIDs: [subId] })
      }
      return
    }

    // Handle dropping group node back to node list (remove from group)
    if (sourceDroppableId.endsWith('-nodes') && confirmedDestDroppableId === NODE_DROPPABLE_ID) {
      const sourceGroupId = sourceDroppableId.replace('-nodes', '')
      const parsed = parseGroupItemId(draggableId)
      if (parsed) {
        groupDelNodesMutation.mutate({ id: sourceGroupId, nodeIDs: [parsed.itemId] })
      }
    }
  }

  return (
    <div className="panel-workspace flex flex-col gap-5" data-workspace={area}>
      {area === 'logs' && <RouterLogs />}
      {area === 'dashboard' && <ConnectivityOverview />}
      {area === 'dashboard' && (
        <section id="panel-monitor">
          <TrafficOverview compact />
        </section>
      )}
      {area === 'all' && (
        <nav className="flex gap-5 overflow-x-auto border-b text-sm" aria-label={t('panel.title')}>
          {[
            ['panel-common', t('panel.common')],
            ['panel-advanced', t('panel.advanced')],
            ['panel-monitor', t('panel.monitor')],
          ].map(([id, label]) => (
            <button
              key={id}
              type="button"
              aria-controls={id}
              aria-current={activeArea === id ? 'location' : undefined}
              className={`shrink-0 border-b-2 px-1 pb-3 pt-1 ${activeArea === id ? 'border-primary font-medium text-foreground' : 'border-transparent text-muted-foreground'}`}
              onClick={() => scrollToArea(id)}
            >
              {label}
            </button>
          ))}
        </nav>
      )}
      {commonVisible && (
        <section
          id="panel-common"
          tabIndex={-1}
          className="scroll-mt-36 outline-none"
          aria-label={t(area === 'dashboard' ? 'group' : 'panel.resources')}
        >
          {area === 'all' && (
            <div className="mb-4">
              <h2 className="text-lg font-semibold">{t('panel.common')}</h2>
              <p className="mt-1 text-sm text-muted-foreground">{t('panel.commonDescription')}</p>
            </div>
          )}
          <DragDropContext onDragStart={onDragStart} onDragUpdate={onDragUpdate} onDragEnd={onDragEnd}>
            {area !== 'dashboard' && (
              <div className="panel-mobile-tabs" role="tablist" aria-label={t('panel.common')}>
                {[
                  ['groups', t('group'), groups.length],
                  ['nodes', t('node'), nodeLibrary.length],
                  ['subscriptions', t('subscription'), subscriptions.length],
                ]
                  .filter(([id]) => area === 'all' || id !== 'groups')
                  .map(([id, label, count], index, tabs) => (
                    <button
                      key={id}
                      type="button"
                      role="tab"
                      id={`workspace-tab-${id}`}
                      aria-controls={`workspace-${id}`}
                      aria-selected={mobilePanel === id}
                      tabIndex={mobilePanel === id ? 0 : -1}
                      onClick={() => setMobilePanel(String(id))}
                      onKeyDown={(event) => {
                        const keys = tabs.map(([id]) => String(id))
                        const next =
                          event.key === 'ArrowRight'
                            ? (index + 1) % keys.length
                            : event.key === 'ArrowLeft'
                              ? (index + keys.length - 1) % keys.length
                              : event.key === 'Home'
                                ? 0
                                : event.key === 'End'
                                  ? keys.length - 1
                                  : -1
                        if (next < 0) return
                        event.preventDefault()
                        setMobilePanel(keys[next])
                        document.getElementById(`workspace-tab-${keys[next]}`)?.focus()
                      }}
                    >
                      {label} <span className="tabular-nums">{count}</span>
                    </button>
                  ))}
              </div>
            )}
            <div className="panel-common-grid" ref={workspaceRef}>
              <div
                id="workspace-groups"
                hidden={area === 'resources'}
                className="panel-resource-pane"
                data-active={area === 'dashboard' || mobilePanel === 'groups'}
                role="region"
                aria-label={t('group')}
              >
                {(area === 'dashboard' || area === 'all') && (
                  <GroupResource
                    latencyTests={latencyTests}
                    quickAdd={area === 'dashboard'}
                    highlight={!!draggingResource}
                    draggingResource={draggingResource}
                    dragDestinationDroppableId={dragDestinationDroppableId}
                    hoveredGroupId={hoveredGroupId}
                    nodeLatencies={nodeLatencies}
                  />
                )}
              </div>
              <div
                id="workspace-nodes"
                hidden={area === 'dashboard'}
                className="panel-resource-pane"
                data-active={mobilePanel === 'nodes'}
                role="region"
                aria-label={t('node')}
                tabIndex={-1}
              >
                {area !== 'dashboard' && (
                  <NodeResource
                    library={nodeLibrary}
                    nodes={visibleNodes}
                    totalCount={nodeLibrary.length}
                    filters={nodeFilters}
                    onFiltersChange={setNodeFilters}
                    loading={nodesRequest.isPending}
                    error={nodesRequest.isError}
                    onRetry={() => {
                      nodesRequest.refetch()
                    }}
                    latencyTests={latencyTests}
                    dragging={isDragging}
                    highlight={draggingResource?.type === DraggableResourceType.groupNode}
                    nodeLatencies={nodeLatencies}
                  />
                )}
              </div>
              <div
                id="workspace-subscriptions"
                hidden={area === 'dashboard'}
                className="panel-resource-pane"
                data-active={mobilePanel === 'subscriptions'}
                role="region"
                aria-label={t('subscription')}
              >
                {area !== 'dashboard' && (
                  <SubscriptionResource
                    sortedSubscriptions={sortedSubscriptions}
                    nodeLatencies={nodeLatencies}
                    latencyTests={latencyTests}
                  />
                )}
              </div>
            </div>
          </DragDropContext>
        </section>
      )}

      <section
        id="panel-advanced"
        hidden={area !== 'advanced' && area !== 'all'}
        tabIndex={-1}
        className="scroll-mt-36 rounded-2xl border bg-card shadow-sm outline-none"
      >
        {area === 'all' && (
          <button
            type="button"
            aria-expanded={advancedOpen}
            aria-controls="panel-advanced-content"
            className="flex w-full items-center gap-3 p-5 text-left"
            onClick={() => setAdvancedOpen((value) => !value)}
          >
            <SlidersHorizontal aria-hidden="true" className="h-5 w-5 shrink-0 text-muted-foreground" />
            <span className="min-w-0 flex-1">
              <span className="block text-base font-semibold">{t('panel.advanced')}</span>
              <span className="mt-1 block text-sm text-muted-foreground">{t('panel.advancedDescription')}</span>
            </span>
            <ChevronDown
              aria-hidden="true"
              className={`h-4 w-4 transition-transform motion-reduce:transition-none ${advancedOpen ? 'rotate-180' : ''}`}
            />
          </button>
        )}
        <div
          id="panel-advanced-content"
          hidden={area !== 'advanced' && !advancedOpen}
          className="panel-advanced-grid grid grid-cols-1 items-start gap-4 p-4 lg:grid-cols-3"
        >
          {(area === 'advanced' || (area === 'all' && advancedOpen)) && (
            <>
              <Config />
              <DNS />
              <Routing />
            </>
          )}
        </div>
      </section>
      {area === 'all' && (
        <section id="panel-monitor" tabIndex={-1} className="scroll-mt-36 outline-none">
          <TrafficOverview />
        </section>
      )}
    </div>
  )
}

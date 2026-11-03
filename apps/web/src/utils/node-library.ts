import type { NodeLatencyProbeResult } from '~/apis'
import type { NodesQuery, SubscriptionsQuery } from '~/schemas/gql/graphql'

export interface LibraryNode {
  node: NodesQuery['nodes']['edges'][number]
  sources: Array<{ id: string; name: string }>
  editable: boolean
}
export interface NodeFilters {
  search: string
  source: string
  sort: 'custom' | 'latency'
}
export const defaultNodeFilters: NodeFilters = { search: '', source: 'all', sort: 'custom' }

export function nodeLatencyRank(probe?: NodeLatencyProbeResult): number {
  return probe?.alive && probe.latencyMs != null && Number.isFinite(probe.latencyMs) && probe.latencyMs >= 0
    ? probe.latencyMs
    : Infinity
}

export function buildNodeLibrary(
  nodes: NodesQuery['nodes']['edges'],
  subscriptions: SubscriptionsQuery['subscriptions'],
): LibraryNode[] {
  const entries = new Map<string, LibraryNode>()
  for (const node of nodes) entries.set(node.id, { node, sources: [], editable: true })
  for (const subscription of subscriptions) {
    for (const node of subscription.nodes.edges) {
      const entry = entries.get(node.id) ?? { node: { ...node, address: '', tag: '' }, sources: [], editable: false }
      if (!entry.sources.some((source) => source.id === subscription.id))
        entry.sources.push({ id: subscription.id, name: subscription.tag || subscription.id })
      entries.set(node.id, entry)
    }
  }
  return [...entries.values()]
}

export function orderNodeLibrary(library: LibraryNode[], order: string[]): LibraryNode[] {
  const entries = new Map(library.map((entry) => [entry.node.id, entry]))
  const ids = [...new Set(order)].filter((id) => entries.has(id))
  const existing = new Set(ids)
  for (const entry of library) if (!existing.has(entry.node.id)) ids.push(entry.node.id)
  return ids.map((id) => entries.get(id)!)
}

export function filterNodeLibrary(
  library: LibraryNode[],
  filters: NodeFilters,
  latencies: Record<string, NodeLatencyProbeResult>,
): LibraryNode[] {
  const search = filters.search.trim().toLocaleLowerCase()
  const result = library.filter(({ node, sources }) => {
    const inSource =
      filters.source === 'all' ||
      (filters.source === 'manual' ? !sources.length : sources.some((source) => source.id === filters.source))
    return (
      inSource &&
      [node.name, node.tag, node.protocol, ...sources.map((source) => source.name)].some((value) =>
        value?.toLocaleLowerCase().includes(search),
      )
    )
  })
  const latency = (entry: LibraryNode) => {
    return nodeLatencyRank(latencies[entry.node.id])
  }
  if (filters.sort === 'latency') result.sort((a, b) => (latency(a) === latency(b) ? 0 : latency(a) - latency(b)))
  return result
}

// Change only the visible slots, so filtering cannot reorder hidden nodes.
export function reorderVisibleNodes(all: string[], visible: string[], from: number, to: number): string[] {
  if (from < 0 || to < 0 || from >= visible.length || to >= visible.length) return all
  const reordered = [...visible]
  const [moved] = reordered.splice(from, 1)
  reordered.splice(to, 0, moved)
  const visibleSet = new Set(visible)
  let index = 0
  return all.map((id) => (visibleSet.has(id) ? reordered[index++] : id))
}

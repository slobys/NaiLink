export interface RuntimeGroups {
  core: string
  ready: boolean
  selectionAvailable: boolean
  groups: { name: string; nodes: string[]; selected?: string; selectedNodeId?: string; selectionKind?: string }[]
}

export function activeNodeGroups(runtime?: RuntimeGroups): Map<string, string[]> {
  const active = new Map<string, string[]>()
  if (!runtime?.ready || !Array.isArray(runtime.groups)) return active
  for (const group of runtime.groups) {
    if (
      !group ||
      typeof group.name !== 'string' ||
      typeof group.selectedNodeId !== 'string' ||
      !group.selectedNodeId ||
      !['native', 'single'].includes(group.selectionKind || '')
    )
      continue
    active.set(group.selectedNodeId, [...(active.get(group.selectedNodeId) || []), group.name])
  }
  return active
}

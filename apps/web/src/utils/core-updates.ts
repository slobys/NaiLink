import type { OptionalCoreStatus } from './router-maintenance'

export function coreVersionCompare(a: string, b: string) {
  const parse = (value: string) => {
    const match = value.match(/^(\d{1,5})\.(\d{1,5})\.(\d{1,5})(?:-nailink\.(\d{1,5}))?$/)
    return match ? match.slice(1).map((part) => Number(part || 0)) : undefined
  }
  const left = parse(a)
  const right = parse(b)
  if (!left || !right) return undefined
  for (let i = 0; i < left.length; i++) if (left[i] !== right[i]) return left[i] > right[i] ? 1 : -1
  return 0
}

export function coreUpdateState(core?: OptionalCoreStatus, now = Date.now()) {
  const update = core?.update
  if (update?.code) return 'error'
  if (
    !update ||
    !update.version ||
    !Number.isFinite(update.checked) ||
    update.checked <= 0 ||
    now / 1000 - update.checked >= 3600 ||
    update.checked > now / 1000 + 60
  )
    return 'unknown'
  if (!core?.installed || !core.version) return 'available'
  const comparison = coreVersionCompare(update.version, core.version)
  return comparison === 0 ? 'latest' : comparison === -1 ? 'ahead' : comparison === 1 ? 'available' : 'unknown'
}

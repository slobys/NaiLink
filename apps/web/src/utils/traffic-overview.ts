export function trafficOverviewInterval(native: boolean, connections: number, failed: boolean) {
  if (failed) return 5_000
  if (native && connections >= 8192) return 4_000
  if (native && connections >= 2048) return 2_000
  return 1_000
}

export function trafficHistoryDue(windowSec: number, fetchedAt: number | undefined, now: number) {
  const interval = windowSec >= 1800 ? 10_000 : 5_000
  return fetchedAt === undefined || now - fetchedAt >= interval
}

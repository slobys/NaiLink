import type { NodeRegion } from '~/utils/node-region'
import { Globe2 } from 'lucide-react'

const flags = import.meta.glob<string>('../assets/regions/*.svg', { eager: true, query: '?url', import: 'default' })

export function NodeRegionIcon({ region }: { region?: NodeRegion }) {
  const src = region && flags[`../assets/regions/${region.code}.svg`]
  return src ? (
    <img
      className="node-region-icon"
      data-node-region={region.code}
      src={src}
      width="20"
      height="15"
      alt={region.name}
      title={`按名称识别：${region.name}`}
      draggable={false}
    />
  ) : (
    <Globe2
      className="node-region-icon node-region-unknown"
      data-node-region="unknown"
      aria-label="未识别地区"
      role="img"
    />
  )
}

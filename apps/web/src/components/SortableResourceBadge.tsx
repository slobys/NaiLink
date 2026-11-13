import type { DraggableProvided, DraggableStateSnapshot } from '@hello-pangea/dnd'
import { Draggable } from '@hello-pangea/dnd'
import { GripVertical, X } from 'lucide-react'
import { useTranslation } from 'react-i18next'
import { NodeActiveMark } from '~/components/NodeActiveMark'
import { NodeName } from '~/components/NodeName'
import { Button } from '~/components/ui/button'
import { Popover, PopoverContent, PopoverTrigger } from '~/components/ui/popover'
import { SimpleTooltip } from '~/components/ui/tooltip'
import { useRuntimeNodes } from '~/hooks/useRuntimeNodes'
import { cn } from '~/lib/utils'
import { getInstantDropStyle } from '~/utils'
import { nodeProtocolLabel } from '~/utils/node-protocol-label'

export function SortableResourceBadge({
  id,
  index,
  name,
  protocol,
  address,
  meta,
  disabled = false,
  onRemove,
  children,
  nodeId,
  groupName,
}: {
  id: string
  index: number
  name: string
  protocol?: string | null
  address?: string | null
  meta?: React.ReactNode
  disabled?: boolean
  onRemove?: () => void
  children?: React.ReactNode
  nodeId?: string
  groupName: string
}) {
  const { t } = useTranslation()
  const runtime = useRuntimeNodes(!!nodeId)
  // This surface represents membership in one group, not use anywhere in the
  // node library. A shared candidate can be selected by a different group.
  const activeGroups = nodeId ? runtime.active.get(nodeId)?.filter((name) => name === groupName) : undefined
  const isNode = !!nodeId || !!protocol
  const card = (provided: DraggableProvided, snapshot: DraggableStateSnapshot) => (
    <div
      data-group-member
      data-member-kind={isNode ? 'node' : 'subscription'}
      data-name-card={isNode ? true : undefined}
      data-active={!!activeGroups?.length}
      data-dragging={snapshot.isDragging || snapshot.isDropAnimating}
      ref={provided.innerRef}
      {...provided.draggableProps}
      style={getInstantDropStyle(provided, snapshot)}
      className={cn(
        'group group-member-card relative flex w-full min-w-0 items-start gap-1 rounded-lg border bg-card p-2 select-none',
        'transition-[shadow,border-color,opacity,background-color] duration-200',
        'hover:shadow-sm hover:border-primary/30 hover:bg-accent/50',
        snapshot.isDragging && 'opacity-90 z-10 shadow-lg ring-2 ring-primary/20',
      )}
    >
      {/* Drag handle */}
      <div
        className="group-member-handle flex h-7 w-5 shrink-0 cursor-grab items-center justify-center rounded touch-none active:cursor-grabbing"
        {...provided.dragHandleProps}
      >
        <GripVertical className="h-4 w-4 text-muted-foreground/40 transition-colors group-hover:text-muted-foreground/70" />
      </div>

      <div className="group-member-body min-w-0 flex-1">
        {isNode ? (
          <div className="group-member-heading">
            <Popover>
              <PopoverTrigger asChild>
                <button
                  type="button"
                  data-group-member-name
                  className="group-member-name-control"
                  aria-label={`${name}，查看完整名称`}
                >
                  <NodeName name={name} showRegion />
                </button>
              </PopoverTrigger>
              <PopoverContent
                align="start"
                aria-label="节点信息"
                className="max-w-[calc(100vw-2rem)] space-y-1 text-sm"
                onClick={(event) => event.stopPropagation()}
              >
                <p className="break-words font-medium">{name}</p>
                {address && <p className="break-all text-xs text-muted-foreground">{address}</p>}
                <div className="flex items-center gap-2 text-xs text-muted-foreground">
                  {protocol && <span>{nodeProtocolLabel(protocol)}</span>}
                  {meta}
                </div>
                {children && <div className="text-xs text-muted-foreground">{children}</div>}
              </PopoverContent>
            </Popover>
            <NodeActiveMark groups={activeGroups} />
          </div>
        ) : (
          <span
            data-group-member-name
            className="flex items-start gap-1.5 text-xs font-medium leading-5 [overflow-wrap:anywhere]"
          >
            {name}
          </span>
        )}
        <div className="group-member-details mt-0.5 flex min-w-0 flex-wrap items-center gap-x-1.5 gap-y-1 text-[10px] text-muted-foreground">
          {protocol && (
            <span className="shrink-0 font-medium text-primary" title={protocol}>
              {nodeProtocolLabel(protocol)}
            </span>
          )}
          {address && (
            <span className="min-w-0 flex-1 truncate" title={address}>
              {address}
            </span>
          )}
          {meta && <span className="group-member-meta min-w-0 max-w-full [overflow-wrap:anywhere]">{meta}</span>}
        </div>
      </div>

      {/* Remove button */}
      {onRemove && (
        <Button
          variant="ghost"
          size="xs"
          disabled={disabled}
          title={disabled ? '正在应用配置，完成后可移除群组成员' : undefined}
          aria-label={`${t('actions.remove')} ${name}`}
          className="group-member-remove h-7 w-6 p-0 shrink-0 rounded text-muted-foreground hover:text-destructive hover:bg-destructive/10"
          onClick={(e) => {
            e.stopPropagation()
            onRemove()
          }}
          onPointerDown={(e) => e.stopPropagation()}
        >
          <X className="h-3 w-3" />
        </Button>
      )}
    </div>
  )

  return (
    <Draggable draggableId={id} index={index} isDragDisabled={disabled}>
      {(provided, snapshot) => {
        const cardElement = card(provided, snapshot)
        if (children && !isNode) {
          return <SimpleTooltip label={<span className="text-xs">{children}</span>}>{cardElement}</SimpleTooltip>
        }
        return cardElement
      }}
    </Draggable>
  )
}

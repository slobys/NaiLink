import type { DraggableProvided, DraggableStateSnapshot } from '@hello-pangea/dnd'
import { Draggable } from '@hello-pangea/dnd'
import { GripVertical, X } from 'lucide-react'
import { useTranslation } from 'react-i18next'
import { Button } from '~/components/ui/button'
import { SimpleTooltip } from '~/components/ui/tooltip'
import { cn } from '~/lib/utils'
import { getInstantDropStyle } from '~/utils'

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
}) {
  const { t } = useTranslation()
  const card = (provided: DraggableProvided, snapshot: DraggableStateSnapshot) => (
    <div
      data-group-member
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

      <div className="min-w-0 flex-1">
        <span data-group-member-name className="block text-xs font-medium leading-5 [overflow-wrap:anywhere]">
          {name}
        </span>
        <div className="mt-0.5 flex min-w-0 flex-wrap items-center gap-x-1.5 gap-y-1 text-[10px] text-muted-foreground">
          {protocol && <span className="shrink-0 font-medium uppercase text-primary">{protocol}</span>}
          {address && (
            <span className="min-w-0 flex-1 truncate" title={address}>
              {address}
            </span>
          )}
          {meta && <span className="min-w-0 max-w-full [overflow-wrap:anywhere]">{meta}</span>}
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
        if (children) {
          return <SimpleTooltip label={<span className="text-xs">{children}</span>}>{cardElement}</SimpleTooltip>
        }
        return cardElement
      }}
    </Draggable>
  )
}

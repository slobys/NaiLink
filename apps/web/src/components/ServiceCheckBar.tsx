import type { ServiceProfile, ServiceResult, useServiceChecks } from '~/hooks/useServiceChecks'
import { useTranslation } from 'react-i18next'
import { formatLatencyLabel } from '~/utils/latency'
export const serviceLabels: Record<string, string> = {
  service_accessible: '入口可访问',
  service_restricted: '访问受限',
  service_challenge: '需人机验证',
  service_rate_limited: '请求受限',
  service_remote_error: '服务暂时异常',
  service_unconfirmed: '尚未确认',
  timeout: '连接超时',
  cancelled: '未完成',
}
export function ServiceResultBadge({ result }: { result?: ServiceResult }) {
  const { t } = useTranslation()
  if (!result)
    return (
      <span className="service-result" data-state="unknown">
        待检测
      </span>
    )
  const text = serviceLabels[result.message || ''] || (result.alive ? '入口可访问' : formatLatencyLabel(result, t))
  return (
    <span
      className="service-result"
      data-state={
        result.alive
          ? 'pass'
          : result.message?.includes('challenge') || result.message?.includes('unconfirmed')
            ? 'unknown'
            : 'fail'
      }
      title={`${text} · ${new Date(result.testedAt).toLocaleTimeString()}；入口检测不等于登录或播放验证`}
    >
      {text}
    </span>
  )
}
export function ServiceCheckBar({
  checks,
  onSelectPassed,
}: {
  checks: ReturnType<typeof useServiceChecks>
  onSelectPassed?: () => void
}) {
  if (!checks.available) return null
  return (
    <div className="service-check-bar" data-service-check>
      <div className="service-check-controls">
        <label>
          群组用途{' '}
          <select
            aria-label="群组用途"
            value={checks.profile}
            disabled={checks.changing}
            onChange={(e) => checks.changeProfile(e.target.value as ServiceProfile)}
          >
            <option value="">不自动检测</option>
            <option value="proxy">常规连接</option>
            <option value="ai">AI · ChatGPT</option>
            <option value="netflix">Netflix</option>
          </select>
        </label>
        {checks.profile && (
          <>
            <span role="status">
              {checks.busy ? '正在检测' : '已检测'} {checks.completed}/{checks.total}
            </span>
            <button
              type="button"
              disabled={checks.busy && !checks.automatic}
              onClick={checks.busy ? checks.cancel : checks.retry}
            >
              {checks.busy ? (checks.automatic ? '暂停检测' : '正在暂停…') : '重新检测'}
            </button>
            <label>
              <input
                type="checkbox"
                checked={checks.onlyPassed}
                onChange={(e) => checks.setOnlyPassed(e.target.checked)}
              />
              只看通过
            </label>
            {onSelectPassed && (
              <button type="button" onClick={onSelectPassed}>
                勾选通过
              </button>
            )}
          </>
        )}
      </div>
      <p>
        {checks.profile === 'ai'
          ? '自动检查 ChatGPT 入口，不验证账号、登录和其他 AI 服务。'
          : checks.profile === 'netflix'
            ? '自动检查 Netflix 入口，播放权限、地区片库和账号状态仍需实际验证。'
            : checks.profile === 'proxy'
              ? '通过每个节点独立检查 HTTPS 连接；不改变当前代理和分流。'
              : '选择用途后自动检测；改名不会丢失用途。'}
      </p>
      {checks.error && <p role="status">{checks.error}</p>}
      {checks.profileError && <p role="status">群组用途暂时无法保存或读取，请重试。</p>}
    </div>
  )
}

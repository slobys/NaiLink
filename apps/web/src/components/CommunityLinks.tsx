export function CommunityLinks() {
  return (
    <nav aria-label="社区频道" className="community-links flex min-w-0 flex-nowrap items-center justify-center gap-2">
      <a
        href="https://www.youtube.com/@naiyou"
        target="_blank"
        rel="noopener noreferrer"
        className="community-link"
        aria-label="YouTube 频道（在新标签页打开）"
      >
        <svg viewBox="0 0 24 24" fill="none" className="h-5 w-5 shrink-0 text-red-500" aria-hidden="true">
          <rect x="2" y="5" width="20" height="14" rx="4" stroke="currentColor" strokeWidth="2" />
          <path d="m10 9 5 3-5 3V9Z" fill="currentColor" />
        </svg>
        <span>YouTube</span>
      </a>
      <a
        href="https://t.me/naiyous"
        target="_blank"
        rel="noopener noreferrer"
        className="community-link"
        aria-label="Telegram 群组（在新标签页打开）"
      >
        <svg viewBox="0 0 24 24" fill="none" className="h-5 w-5 shrink-0 text-sky-500" aria-hidden="true">
          <path
            d="m21 3-4 18-6-7-7-3 17-8ZM11 14 21 3"
            stroke="currentColor"
            strokeWidth="1.8"
            strokeLinejoin="round"
          />
        </svg>
        <span>Telegram</span>
      </a>
    </nav>
  )
}

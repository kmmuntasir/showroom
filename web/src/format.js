// Pure display helpers. Timestamps (unix seconds) and byte counts come from
// the API and are formatted here only — the SPA never derives infra state
// from them (docs/demos.md §Control dashboard UI).

export const humanSize = (bytes) => {
  if (!Number.isFinite(bytes) || bytes < 0) return '—'
  const units = ['B', 'KB', 'MB', 'GB', 'TB']
  let value = bytes
  let unit = 0
  while (value >= 1024 && unit < units.length - 1) {
    value /= 1024
    unit += 1
  }
  const rounded = unit === 0 ? String(value) : value.toFixed(1)
  return `${rounded} ${units[unit]}`
}

export const timeAgoOrDate = (unixSeconds) => {
  if (!Number.isFinite(unixSeconds) || unixSeconds <= 0) return '—'
  const then = new Date(unixSeconds * 1000)
  const elapsed = Date.now() - then.getTime()
  const minute = 60 * 1000
  const hour = 60 * minute
  const day = 24 * hour

  if (elapsed < minute) return 'just now'
  if (elapsed < hour) return `${Math.floor(elapsed / minute)} min ago`
  if (elapsed < day) return `${Math.floor(elapsed / hour)} h ago`
  if (elapsed < 7 * day) return `${Math.floor(elapsed / day)} d ago`
  return then.toLocaleDateString(undefined, { year: 'numeric', month: 'short', day: 'numeric' })
}

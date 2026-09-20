/**
 * Format bytes to human-readable string (B, KB, MB, GB, TB)
 */
export function formatBytes(bytes: number, decimals = 2): string {
  if (bytes === 0) return "0 B"

  const k = 1024
  const dm = decimals < 0 ? 0 : decimals
  const sizes = ["B", "KB", "MB", "GB", "TB"]

  const i = Math.floor(Math.log(bytes) / Math.log(k))

  return `${parseFloat((bytes / Math.pow(k, i)).toFixed(dm))} ${sizes[i]}`
}

/**
 * Format large numbers with comma separators
 */
export function formatNumber(num: number): string {
  return num.toLocaleString("en-US")
}

/**
 * Get color class based on percentage threshold
 */
export function getUsageColor(percentage: number): string {
  if (percentage >= 90) return "text-red-500"
  if (percentage >= 75) return "text-orange-500"
  if (percentage >= 60) return "text-yellow-500"
  return "text-green-500"
}

/**
 * Get progress bar variant based on percentage threshold
 */
export function getProgressVariant(percentage: number): "default" | "warning" | "destructive" {
  if (percentage >= 90) return "destructive"
  if (percentage >= 75) return "warning"
  return "default"
}

/**
 * Format a millisecond duration as "42s", "1m 20s", "2h 5m"
 */
export function formatDuration(ms: number): string {
  if (!ms || ms <= 0) return "0s"

  const totalSeconds = Math.floor(ms / 1000)
  const h = Math.floor(totalSeconds / 3600)
  const m = Math.floor((totalSeconds % 3600) / 60)
  const s = totalSeconds % 60

  if (h > 0) return m > 0 ? `${h}h ${m}m` : `${h}h`
  if (m > 0) return s > 0 ? `${m}m ${s}s` : `${m}m`
  return `${s}s`
}

/**
 * Format an ISO timestamp as a relative "time ago" string.
 * Null/undefined/invalid input renders as "Never".
 */
export function timeAgo(iso?: string | null): string {
  if (!iso) return "Never"

  const then = new Date(iso).getTime()
  if (Number.isNaN(then)) return "Never"

  const diffMs = Date.now() - then
  const abs = Math.abs(diffMs)
  const suffix = diffMs >= 0 ? " ago" : " from now"

  const minutes = Math.round(abs / 60000)
  if (minutes < 1) return "just now"
  if (minutes < 60) return `${minutes}m${suffix}`

  const hours = Math.round(minutes / 60)
  if (hours < 24) return `${hours}h${suffix}`

  const days = Math.round(hours / 24)
  return `${days}d${suffix}`
}

/**
 * Format a future ISO timestamp as an "in ..." string for next_run_at.
 * Past timestamps render as overdue; null/undefined/invalid as "—".
 */
export function timeUntil(iso?: string | null): string {
  if (!iso) return "—"

  const then = new Date(iso).getTime()
  if (Number.isNaN(then)) return "—"

  const diffMs = then - Date.now()
  if (diffMs <= 0) {
    const overdue = -diffMs
    if (overdue < 60000) return "now"
    return timeAgo(iso).replace(" ago", " overdue")
  }

  const minutes = Math.round(diffMs / 60000)
  if (minutes < 1) return "now"
  if (minutes < 60) return `in ${minutes}m`

  const hours = Math.round(minutes / 60)
  if (hours < 24) return `in ${hours}h`

  const days = Math.round(hours / 24)
  return `in ${days}d`
}

/**
 * Get text color class for a success-rate percentage (inverted: high = good)
 */
export function getSuccessColor(pct: number): string {
  if (pct >= 90) return "text-green-500"
  if (pct >= 75) return "text-orange-500"
  return "text-red-500"
}

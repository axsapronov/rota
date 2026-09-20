"use client"

import { Badge } from "@/components/ui/badge"
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card"
import { Progress } from "@/components/ui/progress"
import {
  Cpu,
  HardDrive,
  MemoryStick,
  Zap,
  Activity,
  Server,
  Globe,
  HeartPulse,
  Trash2,
} from "lucide-react"
import {
  formatBytes,
  formatDuration,
  formatNumber,
  getSuccessColor,
  getUsageColor,
  timeAgo,
  timeUntil,
} from "@/lib/format-utils"
import { cn } from "@/lib/utils"
import type { SystemMetrics as SystemMetricsType } from "@/lib/types"

type GeoSection = NonNullable<SystemMetricsType["geo"]>
type HealthCheckSection = NonNullable<SystemMetricsType["health_check"]>
type GlobalHCSection = NonNullable<SystemMetricsType["global_health_check"]>
type CleanupSection = NonNullable<SystemMetricsType["cleanup"]>

interface SystemMetricsProps {
  data?: SystemMetricsType
}

export function SystemMetrics({ data }: SystemMetricsProps) {
  // Mock data - will be replaced with real API data. The mock covers only the
  // base sections, so the background pipeline panels stay hidden with it.
  const metrics: SystemMetricsType = data || {
    memory: {
      total: 17179869184,
      used: 10737418240,
      available: 6442450944,
      percentage: 62.5,
    },
    cpu: {
      percentage: 35.2,
      cores: 8,
    },
    disk: {
      total: 500107862016,
      used: 320171548672,
      free: 179936313344,
      percentage: 64.0,
    },
    runtime: {
      goroutines: 42,
      threads: 8,
      gc_pause_count: 156,
      mem_alloc: 5242880,
      mem_sys: 75497472,
    },
  }

  const getProgressVariant = (percentage: number) => {
    if (percentage >= 90) return "destructive"
    if (percentage >= 75) return "warning"
    return "success"
  }

  const hasPipelines = Boolean(
    metrics.geo || metrics.health_check || metrics.global_health_check || metrics.cleanup
  )

  return (
    <div className="space-y-4">
      <div className="flex items-center justify-between">
        <div>
          <h2 className="text-2xl font-bold tracking-tight">System Metrics</h2>
          <p className="text-sm text-muted-foreground">
            Real-time system resource monitoring
          </p>
        </div>
      </div>

      <div className="grid gap-4 md:grid-cols-2 lg:grid-cols-4">
        {/* Memory Card */}
        <Card>
          <CardHeader className="flex flex-row items-center justify-between space-y-0 pb-2">
            <CardTitle className="text-sm font-medium">Memory</CardTitle>
            <MemoryStick className="h-4 w-4 text-muted-foreground" />
          </CardHeader>
          <CardContent className="space-y-3">
            <div className="space-y-1">
              <div className="flex items-baseline justify-between">
                <span
                  className={cn(
                    "text-2xl font-bold",
                    getUsageColor(metrics.memory.percentage)
                  )}
                >
                  {metrics.memory.percentage.toFixed(1)}%
                </span>
                <span className="text-xs text-muted-foreground">
                  {formatBytes(metrics.memory.used)} / {formatBytes(metrics.memory.total)}
                </span>
              </div>
              <Progress
                value={metrics.memory.percentage}
                variant={getProgressVariant(metrics.memory.percentage)}
                className="h-2"
              />
            </div>
            <div className="grid grid-cols-2 gap-2 text-xs">
              <div className="space-y-1">
                <p className="text-muted-foreground">Used</p>
                <p className="font-medium">{formatBytes(metrics.memory.used)}</p>
              </div>
              <div className="space-y-1">
                <p className="text-muted-foreground">Available</p>
                <p className="font-medium">{formatBytes(metrics.memory.available)}</p>
              </div>
            </div>
          </CardContent>
        </Card>

        {/* CPU Card */}
        <Card>
          <CardHeader className="flex flex-row items-center justify-between space-y-0 pb-2">
            <CardTitle className="text-sm font-medium">CPU</CardTitle>
            <Cpu className="h-4 w-4 text-muted-foreground" />
          </CardHeader>
          <CardContent className="space-y-3">
            <div className="space-y-1">
              <div className="flex items-baseline justify-between">
                <span
                  className={cn(
                    "text-2xl font-bold",
                    getUsageColor(metrics.cpu.percentage)
                  )}
                >
                  {metrics.cpu.percentage.toFixed(1)}%
                </span>
                <span className="text-xs text-muted-foreground">
                  {metrics.cpu.cores} cores
                </span>
              </div>
              <Progress
                value={metrics.cpu.percentage}
                variant={getProgressVariant(metrics.cpu.percentage)}
                className="h-2"
              />
            </div>
            <div className="grid grid-cols-2 gap-2 text-xs">
              <div className="space-y-1">
                <p className="text-muted-foreground">Cores</p>
                <p className="font-medium">{metrics.cpu.cores}</p>
              </div>
              <div className="space-y-1">
                <p className="text-muted-foreground">Load</p>
                <p className="font-medium">{metrics.cpu.percentage.toFixed(1)}%</p>
              </div>
            </div>
          </CardContent>
        </Card>

        {/* Disk Card */}
        <Card>
          <CardHeader className="flex flex-row items-center justify-between space-y-0 pb-2">
            <CardTitle className="text-sm font-medium">Disk</CardTitle>
            <HardDrive className="h-4 w-4 text-muted-foreground" />
          </CardHeader>
          <CardContent className="space-y-3">
            <div className="space-y-1">
              <div className="flex items-baseline justify-between">
                <span
                  className={cn(
                    "text-2xl font-bold",
                    getUsageColor(metrics.disk.percentage)
                  )}
                >
                  {metrics.disk.percentage.toFixed(1)}%
                </span>
                <span className="text-xs text-muted-foreground">
                  {formatBytes(metrics.disk.used)} / {formatBytes(metrics.disk.total)}
                </span>
              </div>
              <Progress
                value={metrics.disk.percentage}
                variant={getProgressVariant(metrics.disk.percentage)}
                className="h-2"
              />
            </div>
            <div className="grid grid-cols-2 gap-2 text-xs">
              <div className="space-y-1">
                <p className="text-muted-foreground">Used</p>
                <p className="font-medium">{formatBytes(metrics.disk.used)}</p>
              </div>
              <div className="space-y-1">
                <p className="text-muted-foreground">Free</p>
                <p className="font-medium">{formatBytes(metrics.disk.free)}</p>
              </div>
            </div>
          </CardContent>
        </Card>

        {/* Runtime Card */}
        <Card>
          <CardHeader className="flex flex-row items-center justify-between space-y-0 pb-2">
            <CardTitle className="text-sm font-medium">Runtime</CardTitle>
            <Zap className="h-4 w-4 text-muted-foreground" />
          </CardHeader>
          <CardContent className="space-y-3">
            <div className="space-y-1">
              <div className="flex items-baseline justify-between">
                <span className="text-2xl font-bold text-primary">
                  {formatNumber(metrics.runtime.goroutines)}
                </span>
                <span className="text-xs text-muted-foreground">goroutines</span>
              </div>
            </div>
            <div className="grid grid-cols-2 gap-2 text-xs">
              <div className="space-y-1">
                <p className="text-muted-foreground">Threads</p>
                <p className="font-medium">{metrics.runtime.threads}</p>
              </div>
              <div className="space-y-1">
                <p className="text-muted-foreground">GC Pauses</p>
                <p className="font-medium">{formatNumber(metrics.runtime.gc_pause_count)}</p>
              </div>
            </div>
          </CardContent>
        </Card>
      </div>

      {/* Detailed Runtime Metrics */}
      <div className="grid gap-4 md:grid-cols-2">
        <Card>
          <CardHeader>
            <CardTitle className="text-base flex items-center gap-2">
              <Activity className="h-4 w-4" />
              Go Runtime Memory
            </CardTitle>
          </CardHeader>
          <CardContent>
            <div className="space-y-3">
              <div className="flex items-center justify-between">
                <span className="text-sm text-muted-foreground">Memory Allocated</span>
                <span className="text-sm font-medium">{formatBytes(metrics.runtime.mem_alloc)}</span>
              </div>
              <div className="flex items-center justify-between">
                <span className="text-sm text-muted-foreground">System Memory</span>
                <span className="text-sm font-medium">{formatBytes(metrics.runtime.mem_sys)}</span>
              </div>
              <div className="flex items-center justify-between">
                <span className="text-sm text-muted-foreground">Memory Efficiency</span>
                <span className="text-sm font-medium">
                  {((metrics.runtime.mem_alloc / metrics.runtime.mem_sys) * 100).toFixed(1)}%
                </span>
              </div>
            </div>
          </CardContent>
        </Card>

        <Card>
          <CardHeader>
            <CardTitle className="text-base flex items-center gap-2">
              <Server className="h-4 w-4" />
              Concurrency Stats
            </CardTitle>
          </CardHeader>
          <CardContent>
            <div className="space-y-3">
              <div className="flex items-center justify-between">
                <span className="text-sm text-muted-foreground">Active Goroutines</span>
                <span className="text-sm font-medium">{formatNumber(metrics.runtime.goroutines)}</span>
              </div>
              <div className="flex items-center justify-between">
                <span className="text-sm text-muted-foreground">OS Threads</span>
                <span className="text-sm font-medium">{metrics.runtime.threads}</span>
              </div>
              <div className="flex items-center justify-between">
                <span className="text-sm text-muted-foreground">Goroutines/Thread</span>
                <span className="text-sm font-medium">
                  {(metrics.runtime.goroutines / metrics.runtime.threads).toFixed(1)}
                </span>
              </div>
            </div>
          </CardContent>
        </Card>
      </div>

      {/* Background Pipelines */}
      {hasPipelines && (
        <div className="space-y-4">
          <div>
            <h2 className="text-2xl font-bold tracking-tight">Background Pipelines</h2>
            <p className="text-sm text-muted-foreground">
              Background jobs: GeoIP enrichment, health checks, cleanup
            </p>
          </div>
          <div className="grid gap-4 md:grid-cols-2 lg:grid-cols-3">
            {metrics.geo && <GeoIPMetricsPanel geo={metrics.geo} />}
            {(metrics.health_check || metrics.global_health_check) && (
              <HealthCheckMetricsPanel
                healthCheck={metrics.health_check}
                globalHC={metrics.global_health_check}
              />
            )}
            {metrics.cleanup && <ProxyCleanupMetricsPanel cleanup={metrics.cleanup} />}
          </div>
        </div>
      )}
    </div>
  )
}

const GEO_QUEUE_CAPACITY = 1000
const HC_QUEUE_CAPACITY = 512

const getQueueVariant = (
  pct: number
): "default" | "warning" | "destructive" => {
  if (pct >= 70) return "destructive"
  if (pct >= 30) return "warning"
  return "default"
}

function statusBadge(status: "idle" | "ok" | "error") {
  if (status === "ok") return <Badge variant="success">ok</Badge>
  if (status === "error") return <Badge variant="destructive">error</Badge>
  return <Badge variant="secondary">idle</Badge>
}

function GeoIPMetricsPanel({ geo }: { geo: GeoSection }) {
  const queuePct = Math.min(100, (geo.queue_pending / GEO_QUEUE_CAPACITY) * 100)
  const atLimit = geo.usage_percent_1m >= 70

  return (
    <Card>
      <CardHeader className="flex flex-row items-center justify-between space-y-0 pb-2">
        <CardTitle className="text-sm font-medium flex items-center gap-2">
          <Globe className="h-4 w-4" />
          GeoIP Enrichment
        </CardTitle>
        {atLimit ? (
          <Badge variant="destructive">at limit</Badge>
        ) : (
          <Badge variant="success">ok</Badge>
        )}
      </CardHeader>
      <CardContent className="space-y-3">
        <div className="space-y-1">
          <div className="flex items-baseline justify-between">
            <span className="text-xs text-muted-foreground">Enrichment queue</span>
            <span className="text-xs font-medium">
              {formatNumber(geo.queue_pending)} pending · {formatNumber(geo.queued_in_memory)} in memory
            </span>
          </div>
          <Progress value={queuePct} variant={getQueueVariant(queuePct)} className="h-2" />
        </div>
        <div className="space-y-1">
          <div className="flex items-baseline justify-between">
            <span className="text-xs text-muted-foreground">Batch requests (1m)</span>
            <span className="text-xs font-medium">
              {geo.batch_requests_limit > 0
                ? `${geo.batch_requests_last_minute} / ${geo.batch_requests_limit}`
                : "—"}
            </span>
          </div>
          <Progress
            value={geo.usage_percent_1m}
            variant={getQueueVariant(geo.usage_percent_1m)}
            className="h-2"
          />
          <div className="flex items-baseline justify-between">
            <span className="text-xs text-muted-foreground">Rate limit usage</span>
            <span className={cn("text-xs font-medium", getUsageColor(geo.usage_percent_1m))}>
              {geo.usage_percent_1m.toFixed(1)}%
            </span>
          </div>
        </div>
        <div className="flex items-center justify-between text-xs">
          <span className="text-muted-foreground">IPs updated (10m)</span>
          <span className="font-medium">{formatNumber(geo.ips_updated_last_10m)}</span>
        </div>
      </CardContent>
    </Card>
  )
}

function HealthCheckMetricsPanel({
  healthCheck,
  globalHC,
}: {
  healthCheck?: HealthCheckSection | null
  globalHC?: GlobalHCSection | null
}) {
  const queuePct = healthCheck
    ? Math.min(100, (healthCheck.queue_pending / HC_QUEUE_CAPACITY) * 100)
    : 0
  const noChecks = !healthCheck || healthCheck.checks_last_minute === 0

  return (
    <Card>
      <CardHeader className="flex flex-row items-center justify-between space-y-0 pb-2">
        <CardTitle className="text-sm font-medium flex items-center gap-2">
          <HeartPulse className="h-4 w-4" />
          Health Checks
        </CardTitle>
        {globalHC ? (
          globalHC.enabled ? (
            <Badge variant="success">enabled</Badge>
          ) : (
            <Badge variant="secondary">paused</Badge>
          )
        ) : (
          <Badge variant="secondary">idle</Badge>
        )}
      </CardHeader>
      <CardContent className="space-y-3">
        {healthCheck && (
          <>
            <div className="space-y-1">
              <div className="flex items-baseline justify-between">
                <span className="text-xs text-muted-foreground">Check queue</span>
                <span className="text-xs font-medium">
                  {formatNumber(healthCheck.queue_pending)} pending
                </span>
              </div>
              <Progress value={queuePct} variant={getQueueVariant(queuePct)} className="h-2" />
            </div>
            <div className="grid grid-cols-2 gap-2 text-xs">
              <div className="space-y-1">
                <p className="text-muted-foreground">Processed (10m)</p>
                <p className="font-medium">{formatNumber(healthCheck.processed_last_10m)}</p>
              </div>
              <div className="space-y-1">
                <p className="text-muted-foreground">Checks (1m)</p>
                <p className="font-medium">{formatNumber(healthCheck.checks_last_minute)}</p>
              </div>
            </div>
            <div className="space-y-1">
              <div className="flex items-baseline justify-between">
                <span className="text-xs text-muted-foreground">Success rate (1m)</span>
                {noChecks ? (
                  <span className="text-xs font-medium text-muted-foreground">—</span>
                ) : (
                  <span
                    className={cn(
                      "text-xs font-medium",
                      getSuccessColor(healthCheck.success_percent_1m)
                    )}
                  >
                    {healthCheck.success_percent_1m.toFixed(1)}%
                  </span>
                )}
              </div>
              {!noChecks && (
                <Progress
                  value={healthCheck.success_percent_1m}
                  variant={
                    healthCheck.success_percent_1m >= 75
                      ? "success"
                      : healthCheck.success_percent_1m >= 50
                        ? "warning"
                        : "destructive"
                  }
                  className="h-2"
                />
              )}
            </div>
          </>
        )}
        {globalHC && (
          <div className="space-y-1 border-t pt-2 text-xs">
            <div className="flex items-center justify-between">
              <span className="text-muted-foreground">Interval</span>
              <span className="font-medium">{globalHC.interval_minutes}m</span>
            </div>
            <div className="flex items-center justify-between">
              <span className="text-muted-foreground">Last run</span>
              <span className="flex items-center gap-1.5">
                <span className="font-medium">{timeAgo(globalHC.last_finished_at)}</span>
                {statusBadge(globalHC.last_status)}
              </span>
            </div>
            <div className="flex items-center justify-between">
              <span className="text-muted-foreground">Next run</span>
              <span className="font-medium">{timeUntil(globalHC.next_run_at)}</span>
            </div>
            {globalHC.last_status === "error" && globalHC.last_error && (
              <p className="text-xs text-red-500">{globalHC.last_error}</p>
            )}
          </div>
        )}
      </CardContent>
    </Card>
  )
}

function ProxyCleanupMetricsPanel({ cleanup }: { cleanup: CleanupSection }) {
  const hasError =
    cleanup.log?.last_status === "error" || cleanup.proxy?.last_status === "error"

  return (
    <Card>
      <CardHeader className="flex flex-row items-center justify-between space-y-0 pb-2">
        <CardTitle className="text-sm font-medium flex items-center gap-2">
          <Trash2 className="h-4 w-4" />
          Cleanup
        </CardTitle>
        {hasError ? (
          <Badge variant="destructive">error</Badge>
        ) : (
          <Badge variant="success">ok</Badge>
        )}
      </CardHeader>
      <CardContent className="space-y-3">
        {cleanup.log && (
          <div className="space-y-1 text-xs">
            <div className="flex items-center justify-between">
              <span className="font-medium">Log cleanup</span>
              <span className="flex items-center gap-1.5">
                {cleanup.log.enabled ? (
                  <Badge variant="success">on</Badge>
                ) : (
                  <Badge variant="secondary">off</Badge>
                )}
                {statusBadge(cleanup.log.last_status)}
              </span>
            </div>
            <div className="flex items-center justify-between">
              <span className="text-muted-foreground">Last run</span>
              <span className="font-medium">
                {timeAgo(cleanup.log.last_run_at)} · {formatDuration(cleanup.log.last_duration_ms)}
              </span>
            </div>
            <div className="flex items-center justify-between">
              <span className="text-muted-foreground">Next run</span>
              <span className="font-medium">{timeUntil(cleanup.log.next_run_at)}</span>
            </div>
            <div className="flex items-center justify-between">
              <span className="text-muted-foreground">Retention / compression</span>
              <span className="font-medium">
                {cleanup.log.retention_days}d / {cleanup.log.compression_after_days}d
              </span>
            </div>
            {cleanup.log.last_status === "error" && cleanup.log.last_error && (
              <p className="text-xs text-red-500">{cleanup.log.last_error}</p>
            )}
          </div>
        )}
        {cleanup.proxy && (
          <div className="space-y-1 border-t pt-2 text-xs">
            <div className="flex items-center justify-between">
              <span className="font-medium">Proxy cleanup</span>
              <span className="flex items-center gap-1.5">
                {cleanup.proxy.enabled ? (
                  <Badge variant="success">on</Badge>
                ) : (
                  <Badge variant="secondary">off</Badge>
                )}
                {statusBadge(cleanup.proxy.last_status)}
              </span>
            </div>
            <div className="flex items-center justify-between">
              <span className="text-muted-foreground">Last run</span>
              <span className="font-medium">
                {timeAgo(cleanup.proxy.last_run_at)} · {formatDuration(cleanup.proxy.last_duration_ms)}
              </span>
            </div>
            <div className="flex items-center justify-between">
              <span className="text-muted-foreground">Next run</span>
              <span className="font-medium">{timeUntil(cleanup.proxy.next_run_at)}</span>
            </div>
            <div className="flex items-center justify-between">
              <span className="text-muted-foreground">Deleted / max failed days</span>
              <span className="font-medium">
                {formatNumber(cleanup.proxy.deleted_proxies)} / {cleanup.proxy.max_failed_days}d
              </span>
            </div>
            <div className="flex items-center justify-between">
              <span className="text-muted-foreground">Min success rate / interval</span>
              <span className="font-medium">
                {cleanup.proxy.min_success_rate}% / {cleanup.proxy.cleanup_interval_hours}h
              </span>
            </div>
            {cleanup.proxy.last_status === "error" && cleanup.proxy.last_error && (
              <p className="text-xs text-red-500">{cleanup.proxy.last_error}</p>
            )}
          </div>
        )}
      </CardContent>
    </Card>
  )
}

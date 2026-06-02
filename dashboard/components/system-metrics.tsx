"use client"

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
} from "lucide-react"
import { formatBytes, formatNumber, getUsageColor } from "@/lib/format-utils"
import { cn } from "@/lib/utils"
import type { GeoIPMetrics, HealthCheckMetrics } from "@/lib/types"

interface SystemMetricsProps {
  data?: {
    memory: {
      total: number
      used: number
      available: number
      percentage: number
    }
    cpu: {
      percentage: number
      cores: number
    }
    disk: {
      total: number
      used: number
      free: number
      percentage: number
    }
    runtime: {
      goroutines: number
      threads: number
      gc_pause_count: number
      mem_alloc: number
      mem_sys: number
    }
    geoip?: GeoIPMetrics
    health_check?: HealthCheckMetrics
  }
}

export function SystemMetrics({ data }: SystemMetricsProps) {
  // Mock data - will be replaced with real API data
  const metrics = data || {
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

      {(metrics.geoip || metrics.health_check) && (
        <div className="grid gap-4 md:grid-cols-2">
          {metrics.geoip && <GeoIPMetricsPanel geo={metrics.geoip} />}
          {metrics.health_check && (
            <HealthCheckMetricsPanel hc={metrics.health_check} />
          )}
        </div>
      )}
    </div>
  )
}

const QUEUE_WARN = 300

function GeoIPMetricsPanel({ geo }: { geo: GeoIPMetrics }) {
  const getProgressVariant = (percentage: number) => {
    if (percentage >= 90) return "destructive"
    if (percentage >= 75) return "warning"
    return "success"
  }

  const queueBar = Math.min(100, (geo.queue_pending / QUEUE_WARN) * 100)
  const queueVariant =
    geo.queue_pending >= QUEUE_WARN
      ? "destructive"
      : geo.queue_pending >= QUEUE_WARN / 2
        ? "warning"
        : "success"

  return (
    <Card>
      <CardHeader>
        <CardTitle className="text-base flex items-center gap-2">
          <Globe className="h-4 w-4" />
          GeoIP enrichment
        </CardTitle>
      </CardHeader>
      <CardContent className="space-y-6">
        <div className="space-y-2">
          <div className="flex items-baseline justify-between gap-2">
            <span className="text-sm font-medium">Queue</span>
            <span className="text-sm font-semibold tabular-nums">
              {formatNumber(geo.queue_pending)} in queue
            </span>
          </div>
          <Progress value={queueBar} variant={queueVariant} className="h-2" />
          <p className="text-xs text-muted-foreground">
            {formatNumber(geo.processed_last_10m)} processed in the last 10 minutes
          </p>
        </div>

        <div className="space-y-2">
          <div className="flex items-baseline justify-between gap-2">
            <span className="text-sm font-medium">Rate limit (1m)</span>
            <span
              className={cn(
                "text-sm font-semibold tabular-nums",
                getUsageColor(geo.usage_percent_1m),
              )}
            >
              {geo.lookups_last_minute} / {geo.queries_per_minute}
            </span>
          </div>
          <Progress
            value={geo.usage_percent_1m}
            variant={getProgressVariant(geo.usage_percent_1m)}
            className="h-2"
          />
        </div>
      </CardContent>
    </Card>
  )
}

function HealthCheckMetricsPanel({ hc }: { hc: HealthCheckMetrics }) {
  const getProgressVariant = (percentage: number) => {
    if (percentage < 50) return "destructive"
    if (percentage < 80) return "warning"
    return "success"
  }

  const queueBar = Math.min(100, (hc.queue_pending / QUEUE_WARN) * 100)
  const queueVariant =
    hc.queue_pending >= QUEUE_WARN
      ? "destructive"
      : hc.queue_pending >= QUEUE_WARN / 2
        ? "warning"
        : "success"

  return (
    <Card>
      <CardHeader>
        <CardTitle className="text-base flex items-center gap-2">
          <HeartPulse className="h-4 w-4" />
          Proxy health checks
        </CardTitle>
      </CardHeader>
      <CardContent className="space-y-6">
        <div className="space-y-2">
          <div className="flex items-baseline justify-between gap-2">
            <span className="text-sm font-medium">Queue</span>
            <span className="text-sm font-semibold tabular-nums">
              {formatNumber(hc.queue_pending)} in queue
            </span>
          </div>
          <Progress value={queueBar} variant={queueVariant} className="h-2" />
          <p className="text-xs text-muted-foreground">
            {formatNumber(hc.processed_last_10m)} checked in the last 10 minutes
          </p>
        </div>

        <div className="space-y-2">
          <div className="flex items-baseline justify-between gap-2">
            <span className="text-sm font-medium">Success rate (1m)</span>
            <span
              className={cn(
                "text-sm font-semibold tabular-nums",
                getUsageColor(100 - hc.success_percent_1m),
              )}
            >
              {hc.success_percent_1m.toFixed(0)}% · {formatNumber(hc.checks_last_minute)} checks
            </span>
          </div>
          <Progress
            value={hc.success_percent_1m}
            variant={getProgressVariant(hc.success_percent_1m)}
            className="h-2"
          />
        </div>
      </CardContent>
    </Card>
  )
}

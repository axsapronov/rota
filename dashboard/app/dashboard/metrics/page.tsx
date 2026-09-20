"use client"

import * as React from "react"
import { Globe, HeartPulse, Trash2 } from "lucide-react"
import { PageHeader, Section, LoadingLine } from "@/components/page-header"
import { StatStrip, type StatTone } from "@/components/stat-strip"
import { DefinitionList } from "@/components/definition-list"
import { UsageBar } from "@/components/usage-bar"
import { LiveRefresh } from "@/components/controls"
import { ErrorStatus, HealthLine, OkStatus, OnOff, PendingStatus, Tag } from "@/components/status"
import { api } from "@/lib/api"
import { SystemMetrics } from "@/lib/types"
import { bytes, count, percent, relative, seconds } from "@/lib/format"
import { cn } from "@/lib/utils"

// Resource pressure thresholds: only these earn a status color.
function usageTone(pct: number): StatTone {
  if (pct >= 90) return "critical"
  if (pct >= 75) return "warning"
  return "default"
}

export default function MetricsPage() {
  const [metrics, setMetrics] = React.useState<SystemMetrics | null>(null)
  const [updatedAt, setUpdatedAt] = React.useState<Date | null>(null)

  const fetchMetrics = React.useCallback(async () => {
    try {
      const data = await api.getSystemMetrics()
      setMetrics(data)
      setUpdatedAt(new Date())
    } catch (error) {
      console.error("Failed to fetch system metrics:", error)
    }
  }, [])

  React.useEffect(() => {
    fetchMetrics()
  }, [fetchMetrics])

  if (!metrics) return <LoadingLine />

  const { memory, cpu, disk, runtime, geo, health_check, global_health_check, cleanup } = metrics
  const worst = Math.max(memory.percentage, cpu.percentage, disk.percentage)
  const hasPipelines = Boolean(geo || health_check || global_health_check || cleanup)
  const pressure =
    worst >= 90
      ? { tone: "critical" as const, text: "A resource is above 90% — the core may start refusing work." }
      : worst >= 75
        ? { tone: "warning" as const, text: "A resource is above 75%; fine for now, worth watching." }
        : { tone: "good" as const, text: "Every resource is below 75%." }

  return (
    <>
      <PageHeader
        title="System"
        description="Host resources and Go runtime counters for the core process. Percentages are of the host, not of the container limit."
      >
        {updatedAt && (
          <span className="text-muted-foreground num" suppressHydrationWarning>
            as of {updatedAt.toLocaleTimeString("en-GB")}
          </span>
        )}
        <LiveRefresh onTick={fetchMetrics} defaultSeconds={5} />
      </PageHeader>

      <StatStrip
        columns={4}
        stats={[
          { label: "Memory", value: percent(memory.percentage), hint: `${bytes(memory.used)} of ${bytes(memory.total)}`, tone: usageTone(memory.percentage) },
          { label: "CPU", value: percent(cpu.percentage), hint: `${cpu.cores} cores`, tone: usageTone(cpu.percentage) },
          { label: "Disk", value: percent(disk.percentage), hint: `${bytes(disk.free)} free`, tone: usageTone(disk.percentage) },
          { label: "Goroutines", value: count(runtime.goroutines), hint: `on ${count(runtime.threads)} OS threads` },
        ]}
      />

      <Section title="Pressure" description="Each bar is the share of the host resource in use.">
        <div className="grid gap-6 lg:grid-cols-3">
          {[
            { label: "Memory", pct: memory.percentage, detail: `${bytes(memory.used)} used · ${bytes(memory.available)} available` },
            { label: "CPU", pct: cpu.percentage, detail: `${cpu.cores} cores` },
            { label: "Disk", pct: disk.percentage, detail: `${bytes(disk.used)} used · ${bytes(disk.free)} free` },
          ].map((r) => (
            <div key={r.label}>
              <div className="mb-2 flex items-baseline gap-2">
                <h3 className="font-medium">{r.label}</h3>
                <span className="num text-muted-foreground ml-auto">
                  <span className="text-foreground font-semibold">{percent(r.pct)}</span>
                </span>
              </div>
              <UsageBar value={r.pct} tone={usageTone(r.pct) === "default" ? "default" : usageTone(r.pct)} />
              <p className="text-muted-foreground mt-1.5 text-[0.6875rem] leading-4">{r.detail}</p>
            </div>
          ))}
        </div>
        <div className="mt-6">
          <HealthLine tone={pressure.tone}>{pressure.text}</HealthLine>
        </div>
      </Section>

      <Section title="Go runtime" description="Heap and scheduler counters from the process itself.">
        <DefinitionList
          columns={4}
          items={[
            { label: "Heap allocated", value: bytes(runtime.mem_alloc) },
            { label: "Reserved from OS", value: bytes(runtime.mem_sys) },
            { label: "Heap in use", value: runtime.mem_sys > 0 ? percent((runtime.mem_alloc / runtime.mem_sys) * 100) : "—" },
            { label: "GC pauses", value: count(runtime.gc_pause_count) },
            { label: "Goroutines", value: count(runtime.goroutines) },
            { label: "OS threads", value: count(runtime.threads) },
            { label: "Goroutines per thread", value: runtime.threads > 0 ? (runtime.goroutines / runtime.threads).toFixed(1) : "—" },
          ]}
        />
      </Section>

      {hasPipelines && (
        <Section title="Background pipelines" description="Background jobs: GeoIP enrichment, health checks, cleanup." className="border-b-0">
          <div className="grid gap-6 md:grid-cols-2 lg:grid-cols-3">
            {geo && <GeoPanel geo={geo} />}
            {(health_check || global_health_check) && (
              <HealthCheckPanel healthCheck={health_check} globalHC={global_health_check} />
            )}
            {cleanup && <CleanupPanel cleanup={cleanup} />}
          </div>
        </Section>
      )}
    </>
  )
}

// ── Background pipeline panels ─────────────────────────────────────────────

type GeoSection = NonNullable<SystemMetrics["geo"]>
type HealthCheckSection = NonNullable<SystemMetrics["health_check"]>
type GlobalHCSection = NonNullable<SystemMetrics["global_health_check"]>
type CleanupSection = NonNullable<SystemMetrics["cleanup"]>

const GEO_QUEUE_CAPACITY = 1000
const HC_QUEUE_CAPACITY = 512

function queueTone(pct: number): "default" | "warning" | "critical" {
  if (pct >= 70) return "critical"
  if (pct >= 30) return "warning"
  return "default"
}

function successTone(pct: number): "good" | "warning" | "critical" {
  if (pct >= 90) return "good"
  if (pct >= 75) return "warning"
  return "critical"
}

function runStatus(status: "idle" | "ok" | "error") {
  if (status === "ok") return <OkStatus>ok</OkStatus>
  if (status === "error") return <ErrorStatus>error</ErrorStatus>
  return <PendingStatus>idle</PendingStatus>
}

const panelHeader = (Icon: typeof Globe, title: React.ReactNode, status: React.ReactNode) => (
  <div className="flex items-center gap-2">
    <Icon className="text-muted-foreground size-4" aria-hidden />
    <h3 className="font-medium">{title}</h3>
    <span className="ml-auto">{status}</span>
  </div>
)

function GeoPanel({ geo }: { geo: GeoSection }) {
  const queuePct = Math.min(100, (geo.queue_pending / GEO_QUEUE_CAPACITY) * 100)
  const atLimit = geo.usage_percent_1m >= 70
  return (
    <div className="border-border rounded-md p-4">
      {panelHeader(
        Globe,
        <span className="flex items-center gap-1.5">
          GeoIP Enrichment
          {geo.provider && <Tag mono>{geo.provider}</Tag>}
        </span>,
        atLimit ? <ErrorStatus>at limit</ErrorStatus> : <OkStatus>ok</OkStatus>
      )}
      <div className="mt-3 space-y-3">
        <div>
          <div className="mb-1 flex items-baseline justify-between text-xs">
            <span className="text-muted-foreground">Enrichment queue</span>
            <span className="num">{count(geo.queue_pending)} pending · {count(geo.queued_in_memory)} in memory</span>
          </div>
          <UsageBar value={queuePct} tone={queueTone(queuePct)} />
        </div>
        <div>
          <div className="mb-1 flex items-baseline justify-between text-xs">
            <span className="text-muted-foreground">Batch requests (1m)</span>
            <span className="num">{geo.batch_requests_limit > 0 ? `${count(geo.batch_requests_last_minute)} / ${count(geo.batch_requests_limit)}` : "—"}</span>
          </div>
          <UsageBar value={geo.usage_percent_1m} tone={queueTone(geo.usage_percent_1m)} />
          <div className="mt-1 flex items-baseline justify-between text-xs">
            <span className="text-muted-foreground">Rate limit usage</span>
            <span className="num font-medium">{percent(geo.usage_percent_1m)}</span>
          </div>
        </div>
        <div className="flex items-baseline justify-between text-xs">
          <span className="text-muted-foreground">IPs updated (10m)</span>
          <span className="num font-medium">{count(geo.ips_updated_last_10m)}</span>
        </div>
      </div>
    </div>
  )
}

function HealthCheckPanel({
  healthCheck,
  globalHC,
}: {
  healthCheck?: HealthCheckSection | null
  globalHC?: GlobalHCSection | null
}) {
  const queuePct = healthCheck ? Math.min(100, (healthCheck.queue_pending / HC_QUEUE_CAPACITY) * 100) : 0
  const noChecks = !healthCheck || healthCheck.checks_last_minute === 0
  return (
    <div className="border-border rounded-md p-4">
      {panelHeader(
        HeartPulse,
        "Health Checks",
        globalHC
          ? globalHC.enabled
            ? <OkStatus>enabled</OkStatus>
            : <PendingStatus>paused</PendingStatus>
          : <PendingStatus>idle</PendingStatus>
      )}
      {healthCheck && (
        <div className="mt-3 space-y-3">
          <div>
            <div className="mb-1 flex items-baseline justify-between text-xs">
              <span className="text-muted-foreground">Check queue</span>
              <span className="num">{count(healthCheck.queue_pending)} pending</span>
            </div>
            <UsageBar value={queuePct} tone={queueTone(queuePct)} />
          </div>
          <div className="grid grid-cols-2 gap-2 text-xs">
            <div>
              <p className="text-muted-foreground">Processed (10m)</p>
              <p className="num font-medium">{count(healthCheck.processed_last_10m)}</p>
            </div>
            <div>
              <p className="text-muted-foreground">Checks (1m)</p>
              <p className="num font-medium">{count(healthCheck.checks_last_minute)}</p>
            </div>
          </div>
          <div>
            <div className="mb-1 flex items-baseline justify-between text-xs">
              <span className="text-muted-foreground">Success rate (1m)</span>
              {noChecks ? (
                <span className="text-muted-foreground">—</span>
              ) : (
                <span className={cn("num font-medium", { good: "text-good", warning: "text-warning", critical: "text-critical" }[successTone(healthCheck.success_percent_1m)])}>
                  {percent(healthCheck.success_percent_1m)}
                </span>
              )}
            </div>
            {!noChecks && <UsageBar value={healthCheck.success_percent_1m} tone={successTone(healthCheck.success_percent_1m)} />}
          </div>
        </div>
      )}
      {globalHC && (
        <div className="mt-3 border-t pt-3">
          <DefinitionList
            columns={2}
            items={[
              { label: "Interval", value: `${globalHC.interval_minutes}m` },
              { label: "Last run", value: <span className="flex items-center gap-1.5"><span className="num">{relative(globalHC.last_finished_at)}</span>{runStatus(globalHC.last_status)}</span> },
              { label: "Next run", value: relative(globalHC.next_run_at) },
              { label: "Checked proxies", value: count(globalHC.last_checked_proxies) },
            ]}
          />
          {globalHC.last_status === "error" && globalHC.last_error && (
            <p className="text-critical mt-2 text-xs">{globalHC.last_error}</p>
          )}
        </div>
      )}
    </div>
  )
}

function CleanupPanel({ cleanup }: { cleanup: CleanupSection }) {
  const hasError = cleanup.log?.last_status === "error" || cleanup.proxy?.last_status === "error"
  return (
    <div className="border-border rounded-md p-4">
      {panelHeader(Trash2, "Cleanup", hasError ? <ErrorStatus>error</ErrorStatus> : <OkStatus>ok</OkStatus>)}
      {cleanup.log && (
        <div className="mt-3">
          <div className="mb-2 flex items-center justify-between text-xs">
            <span className="font-medium">Log cleanup</span>
            <span className="flex items-center gap-2">
              <OnOff on={cleanup.log.enabled} onLabel="on" offLabel="off" />
              {runStatus(cleanup.log.last_status)}
            </span>
          </div>
          <DefinitionList
            columns={2}
            items={[
              { label: "Last run", value: `${relative(cleanup.log.last_run_at)} · ${seconds(cleanup.log.last_duration_ms)}` },
              { label: "Next run", value: relative(cleanup.log.next_run_at) },
              { label: "Retention / compression", value: `${cleanup.log.retention_days}d / ${cleanup.log.compression_after_days}d` },
            ]}
          />
          {cleanup.log.last_status === "error" && cleanup.log.last_error && (
            <p className="text-critical mt-2 text-xs">{cleanup.log.last_error}</p>
          )}
        </div>
      )}
      {cleanup.proxy && (
        <div className="border-t mt-3 pt-3">
          <div className="mb-2 flex items-center justify-between text-xs">
            <span className="font-medium">Proxy cleanup</span>
            <span className="flex items-center gap-2">
              <OnOff on={cleanup.proxy.enabled} onLabel="on" offLabel="off" />
              {runStatus(cleanup.proxy.last_status)}
            </span>
          </div>
          <DefinitionList
            columns={2}
            items={[
              { label: "Last run", value: `${relative(cleanup.proxy.last_run_at)} · ${seconds(cleanup.proxy.last_duration_ms)}` },
              { label: "Next run", value: relative(cleanup.proxy.next_run_at) },
              { label: "Deleted / max failed days", value: `${count(cleanup.proxy.deleted_proxies)} / ${cleanup.proxy.max_failed_days}d` },
              { label: "Min success / interval", value: `${cleanup.proxy.min_success_rate}% / ${cleanup.proxy.cleanup_interval_hours}h` },
            ]}
          />
          {cleanup.proxy.last_status === "error" && cleanup.proxy.last_error && (
            <p className="text-critical mt-2 text-xs">{cleanup.proxy.last_error}</p>
          )}
        </div>
      )}
    </div>
  )
}

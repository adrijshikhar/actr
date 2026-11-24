import { useState, useEffect } from 'react';
import {
  Activity,
  Cpu,
  HardDrive,
  Network,
  Server,
  RefreshCw,
} from 'lucide-react';
import type { StatusResponse, RunnerInfo, TelemetrySample, RunnerMetrics } from '@/lib/api';
import { subscribeMetrics } from '@/lib/sse';
import { formatBytes, formatDuration, getProgressWidthClass, cn } from '@/lib/utils';
import { VitalsAreaChart } from '@/components/charts/VitalsAreaChart';
import { ThroughputChart } from '@/components/charts/ThroughputChart';
import { Badge } from '@/components/ui/badge';
import { Button } from '@/components/ui/button';

export interface HostVitalsViewProps {
  status?: StatusResponse | null;
  runners?: RunnerInfo[];
  onRefresh?: () => void;
}

function formatRate(bytesPerSec?: number): string {
  if (!bytesPerSec || bytesPerSec <= 0) return '0 B/s';
  return `${formatBytes(bytesPerSec)}/s`;
}

function createHostSample(partial: Partial<TelemetrySample> & { timestamp: string }): TelemetrySample {
  return {
    pid: 0,
    pgid: 0,
    process_count: 0,
    step_cpu: 0,
    step_rss_bytes: 0,
    step_elapsed_sec: 0,
    disk_used_bytes: 0,
    disk_free_bytes: 0,
    disk_total_bytes: 0,
    disk_used_ratio: 0,
    disk_write_rate: 0,
    disk_inodes_free: 0,
    net_rx_rate: 0,
    net_tx_rate: 0,
    host_cpu: 0,
    host_mem_used: 0,
    host_mem_total: 0,
    host_load_1m: 0,
    ...partial,
  };
}

export function HostVitalsView({
  status,
  runners = [],
  onRefresh,
}: HostVitalsViewProps) {
  const [metricsHistory, setMetricsHistory] = useState<TelemetrySample[]>([]);
  const [streamActive, setStreamActive] = useState(false);
  const [lastTick, setLastTick] = useState<string | null>(null);

  // Subscribe to real-time 1s SSE metrics stream
  useEffect(() => {
    // Prime initial baseline from status if available
    if (status) {
      const initial = createHostSample({
        timestamp: new Date().toISOString(),
        host_cpu: status.host_cpu || 0,
        host_mem_used: status.host_mem_used || 0,
        host_mem_total: status.host_mem_total || 0,
      });
      setMetricsHistory((prev) => (prev.length === 0 ? [initial] : prev));
    }

    const unsub = subscribeMetrics(
      (m: RunnerMetrics) => {
        setStreamActive(true);
        setLastTick(new Date().toLocaleTimeString());
        const sample = createHostSample({
          timestamp: m.timestamp || new Date().toISOString(),
          pid: m.pid,
          pgid: m.pgid,
          host_cpu: m.host_cpu ?? 0,
          host_mem_used: m.host_mem_used ?? 0,
          host_mem_total: m.host_mem_total ?? 0,
          host_load_1m: m.host_load_1m ?? 0,
          disk_used_bytes: m.disk_used_bytes ?? 0,
          disk_free_bytes: m.disk_free_bytes ?? 0,
          disk_total_bytes: m.disk_total_bytes ?? 0,
          disk_write_rate: m.disk_write_rate ?? 0,
          net_rx_rate: m.net_rx_rate ?? 0,
          net_tx_rate: m.net_tx_rate ?? 0,
          process_count: m.process_count ?? 0,
        });

        setMetricsHistory((prev) => {
          const next = [...prev, sample];
          // Keep rolling window of 60 seconds
          return next.length > 60 ? next.slice(-60) : next;
        });
      },
      {
        onOpen: () => setStreamActive(true),
        onError: () => setStreamActive(false),
      }
    );

    return () => {
      unsub();
      setStreamActive(false);
    };
  }, [status]);

  const latest = metricsHistory[metricsHistory.length - 1];

  const currentCpu = latest?.host_cpu ?? (status?.host_cpu || 0);
  const currentMemUsed = latest?.host_mem_used ?? (status?.host_mem_used || 0);
  const currentMemTotal = latest?.host_mem_total ?? (status?.host_mem_total || 1);
  const memPercent = Math.min(Math.round((currentMemUsed / currentMemTotal) * 100), 100);

  const diskWriteRate = latest?.disk_write_rate || 0;
  const netRxRate = latest?.net_rx_rate || 0;
  const netTxRate = latest?.net_tx_rate || 0;
  const totalIoRate = netRxRate + netTxRate;

  const totalActiveSlots = runners.reduce((acc, r) => acc + (r.active_slots || 0), 0);
  const totalMaxSlots =
    runners.reduce((acc, r) => acc + (r.max_concurrency || 0), 0) ||
    Math.max(status?.total_runners || 1, 1);

  return (
    <div className="space-y-6">
      {/* View Header & Stream State Bar */}
      <div className="flex flex-col sm:flex-row sm:items-center justify-between gap-4 p-4 rounded-geist bg-geist-surface border border-geist-hairline">
        <div className="space-y-1">
          <div className="flex items-center gap-2.5">
            <Activity className="w-5 h-5 text-geist-blue" />
            <h2 className="text-base font-semibold text-white tracking-tight">Host Node Telemetry</h2>
            <Badge
              variant="outline"
              className={cn(
                'text-[10px] font-mono gap-1.5',
                streamActive
                  ? 'text-status-success-solid border-status-success-border bg-status-success-bg/20'
                  : 'text-geist-faint border-geist-hairline bg-geist-hover'
              )}
            >
              <span
                className={cn(
                  'w-1.5 h-1.5 rounded-full',
                  streamActive ? 'bg-status-success-solid animate-pulse' : 'bg-geist-faint'
                )}
              />
              {streamActive ? '1s SSE Stream (Active)' : 'Connecting...'}
            </Badge>
          </div>
          <p className="text-xs text-geist-secondary">
            Continuous real-time hardware telemetry and kernel I/O sampled at 1,000ms cadence.
          </p>
        </div>

        <div className="flex items-center gap-3 text-xs font-mono text-geist-faint">
          {lastTick && (
            <span>Last sample: <span className="text-geist-secondary">{lastTick}</span></span>
          )}
          <span>Buffer: <span className="text-geist-secondary">{metricsHistory.length}s</span></span>
          {onRefresh && (
            <Button
              variant="outline"
              size="sm"
              onClick={onRefresh}
              className="h-7 text-xs px-2.5 gap-1.5"
            >
              <RefreshCw className="w-3 h-3" />
              <span>Poll Now</span>
            </Button>
          )}
        </div>
      </div>

      {/* Top 4 Vitals Gauges */}
      <div className="grid grid-cols-1 sm:grid-cols-2 lg:grid-cols-4 gap-4">
        {/* Host CPU Card */}
        <div className="rounded-geist bg-geist-surface border border-geist-hairline p-4 flex flex-col justify-between min-h-[170px]">
          <div>
            <div className="flex items-center justify-between text-xs text-geist-secondary pb-2 border-b border-geist-hairline/60">
              <span className="font-semibold text-white">CPU Utilization</span>
              <Cpu className="w-3.5 h-3.5 text-geist-blue" />
            </div>
            <div className="mt-3 space-y-1">
              <div className="text-3xl font-bold font-mono tracking-tight text-white">
                {currentCpu.toFixed(1)}%
              </div>
              <p className="text-xs text-geist-secondary">
                Load 1m: {latest?.host_load_1m ? latest.host_load_1m.toFixed(2) : '0.00'}
              </p>
            </div>
          </div>
          <div className="space-y-1.5 pt-3 border-t border-geist-hairline/60">
            <div className="w-full bg-geist-elevated h-2 rounded-full overflow-hidden border border-geist-hairline">
              <div
                className={cn(
                  'bg-geist-blue h-full transition-all duration-500 rounded-full',
                  getProgressWidthClass(currentCpu)
                )}
              />
            </div>
            <div className="flex items-center justify-between text-[11px] font-mono text-geist-faint">
              <span>Idle: {Math.max(100 - currentCpu, 0).toFixed(1)}%</span>
              <span>Active: {currentCpu.toFixed(1)}%</span>
            </div>
          </div>
        </div>

        {/* Host Memory Card */}
        <div className="rounded-geist bg-geist-surface border border-geist-hairline p-4 flex flex-col justify-between min-h-[170px]">
          <div>
            <div className="flex items-center justify-between text-xs text-geist-secondary pb-2 border-b border-geist-hairline/60">
              <span className="font-semibold text-white">Host Memory</span>
              <HardDrive className="w-3.5 h-3.5 text-status-success-solid" />
            </div>
            <div className="mt-3 space-y-1">
              <div className="text-3xl font-bold font-mono tracking-tight text-white">
                {formatBytes(currentMemUsed)}
              </div>
              <p className="text-xs text-geist-secondary">
                of {formatBytes(currentMemTotal)} allocated
              </p>
            </div>
          </div>
          <div className="space-y-1.5 pt-3 border-t border-geist-hairline/60">
            <div className="w-full bg-geist-elevated h-2 rounded-full overflow-hidden border border-geist-hairline">
              <div
                className={cn(
                  'bg-status-success-solid h-full transition-all duration-500 rounded-full',
                  getProgressWidthClass(memPercent)
                )}
              />
            </div>
            <div className="flex items-center justify-between text-[11px] font-mono text-geist-faint">
              <span>{memPercent}% used</span>
              <span>{formatBytes(Math.max(currentMemTotal - currentMemUsed, 0))} free</span>
            </div>
          </div>
        </div>

        {/* Disk Write Activity */}
        <div className="rounded-geist bg-geist-surface border border-geist-hairline p-4 flex flex-col justify-between min-h-[170px]">
          <div>
            <div className="flex items-center justify-between text-xs text-geist-secondary pb-2 border-b border-geist-hairline/60">
              <span className="font-semibold text-white">Disk Write I/O</span>
              <HardDrive className="w-3.5 h-3.5 text-status-warning-text" />
            </div>
            <div className="mt-3 space-y-1">
              <div className="text-3xl font-bold font-mono tracking-tight text-white">
                {formatRate(diskWriteRate)}
              </div>
              <p className="text-xs text-geist-secondary">
                Real-time disk commit throughput
              </p>
            </div>
          </div>
          <div className="pt-3 border-t border-geist-hairline/60 flex items-center justify-between text-[11px] font-mono text-geist-faint">
            <span>Storage: {formatBytes(latest?.disk_used_bytes || 0)} used</span>
            <span>Ratio: {Math.round((latest?.disk_used_ratio || 0) * 100)}%</span>
          </div>
        </div>

        {/* Network Transfer */}
        <div className="rounded-geist bg-geist-surface border border-geist-hairline p-4 flex flex-col justify-between min-h-[170px]">
          <div>
            <div className="flex items-center justify-between text-xs text-geist-secondary pb-2 border-b border-geist-hairline/60">
              <span className="font-semibold text-white">Network Bandwidth</span>
              <Network className="w-3.5 h-3.5 text-geist-blue" />
            </div>
            <div className="mt-3 space-y-1">
              <div className="text-3xl font-bold font-mono tracking-tight text-white">
                {formatRate(totalIoRate)}
              </div>
              <p className="text-xs text-geist-secondary">
                RX {formatRate(netRxRate)} · TX {formatRate(netTxRate)}
              </p>
            </div>
          </div>
          <div className="pt-3 border-t border-geist-hairline/60 flex items-center justify-between text-[11px] font-mono text-geist-faint">
            <span>Sockets: Active</span>
            <span>Cadence: 1.0 Hz</span>
          </div>
        </div>
      </div>

      {/* Primary Telemetry Area Charts */}
      <div className="space-y-6">
        {/* Real-time CPU & Memory Dual Area Chart */}
        <VitalsAreaChart
          data={metricsHistory}
          height={280}
          title="Host CPU & Memory Allocation Over Time"
          description="Continuous real-time dual-series plot streaming at 1s intervals via SSE (60s buffer)"
          showCard={true}
        />

        {/* Real-time Disk & Network Throughput Chart */}
        <ThroughputChart
          data={metricsHistory}
          height={260}
          title="Host Node Storage & Network Throughput"
          description="Live disk commit rates and network interface I/O bandwidth"
          showCard={true}
        />
      </div>

      {/* Node System Details / Specifications Table */}
      <div className="rounded-geist bg-geist-surface border border-geist-hairline overflow-hidden">
        <div className="px-4 py-3 border-b border-geist-hairline flex items-center justify-between">
          <div className="flex items-center gap-2">
            <Server className="w-4 h-4 text-geist-secondary" />
            <h3 className="text-xs font-semibold text-white tracking-tight">Host Node Specification & Daemon Diagnostics</h3>
          </div>
          <Badge variant="outline" className="font-mono text-[10px] text-geist-secondary">
            {status?.version || 'v0.2.0-dev'}
          </Badge>
        </div>

        <div className="grid grid-cols-2 md:grid-cols-4 divide-y md:divide-y-0 md:divide-x divide-geist-hairline text-xs font-mono">
          <div className="p-4 space-y-1">
            <span className="text-[11px] text-geist-faint uppercase tracking-wider block font-sans">Daemon Status</span>
            <div className="flex items-center gap-1.5">
              <span className="w-2 h-2 rounded-full bg-status-success-solid" />
              <span className="font-semibold text-white">
                {status?.daemon_connected ? 'Connected' : 'Disconnected'}
              </span>
            </div>
            <span className="text-[11px] text-geist-faint block">PID {latest?.pid || '-'}</span>
          </div>

          <div className="p-4 space-y-1">
            <span className="text-[11px] text-geist-faint uppercase tracking-wider block font-sans">Daemon Uptime</span>
            <span className="font-semibold text-white block">
              {formatDuration(status?.uptime_seconds || 0)}
            </span>
            <span className="text-[11px] text-geist-faint block">Orchestrator active</span>
          </div>

          <div className="p-4 space-y-1">
            <span className="text-[11px] text-geist-faint uppercase tracking-wider block font-sans">Cluster Slots</span>
            <span className="font-semibold text-white block">
              {totalActiveSlots} of {totalMaxSlots} slots occupied
            </span>
            <span className="text-[11px] text-geist-faint block">
              {runners.length} runner{runners.length === 1 ? '' : 's'} registered
            </span>
          </div>

          <div className="p-4 space-y-1">
            <span className="text-[11px] text-geist-faint uppercase tracking-wider block font-sans">Telemetry Stream</span>
            <span className="font-semibold text-status-success-solid block">
              /api/metrics/stream
            </span>
            <span className="text-[11px] text-geist-faint block">Cadence 1s · Rolling 60s</span>
          </div>
        </div>
      </div>
    </div>
  );
}

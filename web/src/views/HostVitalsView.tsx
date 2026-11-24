import { useState, useEffect } from 'react';
import {
  Activity,
  Cpu,
  HardDrive,
  Network,
  Server,
  RefreshCw,
  LayoutGrid,
  Rows,
} from 'lucide-react';
import type { StatusResponse, RunnerInfo, TelemetrySample, RunnerMetrics } from '@/lib/api';
import { subscribeMetrics } from '@/lib/sse';
import { formatBytes, formatDuration, formatRate, getProgressWidthClass, cn } from '@/lib/utils';
import {
  CpuAreaChart,
  MemoryAreaChart,
  DiskAreaChart,
  NetworkAreaChart,
} from '@/components/charts';
import { Badge } from '@/components/ui/badge';
import { Button } from '@/components/ui/button';

export interface HostVitalsViewProps {
  status?: StatusResponse | null;
  runners?: RunnerInfo[];
  onRefresh?: () => void;
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
  const [layoutMode, setLayoutMode] = useState<'grid' | 'stacked'>('grid');

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

        <div className="flex items-center gap-2 sm:gap-3 text-xs font-mono text-geist-faint flex-wrap">
          {lastTick && (
            <span className="hidden md:inline">Last: <span className="text-geist-secondary">{lastTick}</span></span>
          )}
          <span>Buffer: <span className="text-geist-secondary">{metricsHistory.length}s</span></span>

          {/* Layout Mode Switcher */}
          <div className="flex items-center border border-geist-hairline rounded bg-geist-canvas p-0.5">
            <button
              type="button"
              onClick={() => setLayoutMode('grid')}
              className={cn(
                'flex items-center gap-1 px-2 py-1 rounded text-[11px] font-sans transition-colors cursor-pointer',
                layoutMode === 'grid'
                  ? 'bg-geist-elevated text-white font-medium shadow-sm'
                  : 'text-geist-faint hover:text-white'
              )}
              title="2x2 Grid View"
            >
              <LayoutGrid className="w-3 h-3" />
              <span>Grid</span>
            </button>
            <button
              type="button"
              onClick={() => setLayoutMode('stacked')}
              className={cn(
                'flex items-center gap-1 px-2 py-1 rounded text-[11px] font-sans transition-colors cursor-pointer',
                layoutMode === 'stacked'
                  ? 'bg-geist-elevated text-white font-medium shadow-sm'
                  : 'text-geist-faint hover:text-white'
              )}
              title="Stacked Full-Width View"
            >
              <Rows className="w-3 h-3" />
              <span>Stacked</span>
            </button>
          </div>

          {onRefresh && (
            <Button
              variant="outline"
              size="sm"
              onClick={onRefresh}
              className="h-7 text-xs px-2.5 gap-1.5"
            >
              <RefreshCw className="w-3 h-3" />
              <span className="hidden sm:inline">Poll</span>
            </Button>
          )}
        </div>
      </div>

      {/* Top 4 Vitals Gauges */}
      <div className="grid grid-cols-1 sm:grid-cols-2 lg:grid-cols-4 gap-3">
        {/* Host CPU Card */}
        <div className="rounded-geist bg-geist-surface border border-geist-hairline p-3 flex flex-col justify-between">
          <div className="flex items-center justify-between text-xs text-geist-secondary pb-1.5 border-b border-geist-hairline/60">
            <span className="font-semibold text-white">CPU Utilization</span>
            <Cpu className="w-3.5 h-3.5 text-geist-blue" />
          </div>
          <div className="my-2">
            <div className="text-2xl font-bold font-mono tracking-tight text-white">
              {currentCpu.toFixed(1)}%
            </div>
            <p className="text-[11px] text-geist-secondary">
              Load 1m: {latest?.host_load_1m ? latest.host_load_1m.toFixed(2) : '0.00'}
            </p>
          </div>
          <div className="space-y-1 pt-1.5 border-t border-geist-hairline/60">
            <div className="w-full bg-geist-elevated h-1.5 rounded-full overflow-hidden border border-geist-hairline">
              <div
                className={cn(
                  'bg-geist-blue h-full transition-all duration-500 rounded-full',
                  getProgressWidthClass(currentCpu)
                )}
              />
            </div>
            <div className="flex items-center justify-between text-[10px] font-mono text-geist-faint">
              <span>Idle: {Math.max(100 - currentCpu, 0).toFixed(1)}%</span>
              <span>Active: {currentCpu.toFixed(1)}%</span>
            </div>
          </div>
        </div>

        {/* Host Memory Card */}
        <div className="rounded-geist bg-geist-surface border border-geist-hairline p-3 flex flex-col justify-between">
          <div className="flex items-center justify-between text-xs text-geist-secondary pb-1.5 border-b border-geist-hairline/60">
            <span className="font-semibold text-white">Host Memory</span>
            <HardDrive className="w-3.5 h-3.5 text-status-success-solid" />
          </div>
          <div className="my-2">
            <div className="text-2xl font-bold font-mono tracking-tight text-white">
              {formatBytes(currentMemUsed)}
            </div>
            <p className="text-[11px] text-geist-secondary">
              of {formatBytes(currentMemTotal)} ({memPercent}%)
            </p>
          </div>
          <div className="space-y-1 pt-1.5 border-t border-geist-hairline/60">
            <div className="w-full bg-geist-elevated h-1.5 rounded-full overflow-hidden border border-geist-hairline">
              <div
                className={cn(
                  'bg-status-success-solid h-full transition-all duration-500 rounded-full',
                  getProgressWidthClass(memPercent)
                )}
              />
            </div>
            <div className="flex items-center justify-between text-[10px] font-mono text-geist-faint">
              <span>{memPercent}% used</span>
              <span>{formatBytes(Math.max(currentMemTotal - currentMemUsed, 0))} free</span>
            </div>
          </div>
        </div>

        {/* Disk Write Activity */}
        <div className="rounded-geist bg-geist-surface border border-geist-hairline p-3 flex flex-col justify-between">
          <div className="flex items-center justify-between text-xs text-geist-secondary pb-1.5 border-b border-geist-hairline/60">
            <span className="font-semibold text-white">Disk Write I/O</span>
            <HardDrive className="w-3.5 h-3.5 text-status-warning-text" />
          </div>
          <div className="my-2">
            <div className="text-2xl font-bold font-mono tracking-tight text-white">
              {formatRate(diskWriteRate)}
            </div>
            <p className="text-[11px] text-geist-secondary">
              Used: {formatBytes(latest?.disk_used_bytes || 0)}
            </p>
          </div>
          <div className="pt-1.5 border-t border-geist-hairline/60 flex items-center justify-between text-[10px] font-mono text-geist-faint">
            <span>Storage: {formatBytes(latest?.disk_used_bytes || 0)}</span>
            <span>Ratio: {Math.round((latest?.disk_used_ratio || 0) * 100)}%</span>
          </div>
        </div>

        {/* Network Transfer */}
        <div className="rounded-geist bg-geist-surface border border-geist-hairline p-3 flex flex-col justify-between">
          <div className="flex items-center justify-between text-xs text-geist-secondary pb-1.5 border-b border-geist-hairline/60">
            <span className="font-semibold text-white">Network Bandwidth</span>
            <Network className="w-3.5 h-3.5 text-geist-blue" />
          </div>
          <div className="my-2">
            <div className="text-2xl font-bold font-mono tracking-tight text-white">
              {formatRate(totalIoRate)}
            </div>
            <p className="text-[11px] text-geist-secondary">
              RX {formatRate(netRxRate)} · TX {formatRate(netTxRate)}
            </p>
          </div>
          <div className="pt-1.5 border-t border-geist-hairline/60 flex items-center justify-between text-[10px] font-mono text-geist-faint">
            <span>Sockets: Active</span>
            <span>Cadence: 1.0 Hz</span>
          </div>
        </div>
      </div>

      {/* 4 Dedicated Telemetry Area Charts (Split into CPU, Memory, Disk, Network) */}
      <div
        className={cn(
          layoutMode === 'grid'
            ? 'grid grid-cols-1 xl:grid-cols-2 gap-4'
            : 'space-y-4'
        )}
      >
        {/* 1. CPU Utilization Graph */}
        <CpuAreaChart
          data={metricsHistory}
          height={layoutMode === 'grid' ? 200 : 240}
          title="CPU Utilization"
          description="Physical processor core execution load over time (1s resolution)"
          showCard={true}
        />

        {/* 2. Memory Allocation Graph */}
        <MemoryAreaChart
          data={metricsHistory}
          height={layoutMode === 'grid' ? 200 : 240}
          totalMemoryBytes={status?.host_mem_total}
          title="Memory Allocation"
          description="Physical RAM resident set size and host allocation over time"
          showCard={true}
        />

        {/* 3. Disk Write Activity Graph */}
        <DiskAreaChart
          data={metricsHistory}
          height={layoutMode === 'grid' ? 200 : 240}
          title="Disk Write Activity"
          description="Real-time storage controller commit rate over time"
          showCard={true}
        />

        {/* 4. Network Bandwidth Graph */}
        <NetworkAreaChart
          data={metricsHistory}
          height={layoutMode === 'grid' ? 200 : 240}
          title="Network Bandwidth"
          description="Interface socket throughput (Ingress RX / Egress TX) over time"
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

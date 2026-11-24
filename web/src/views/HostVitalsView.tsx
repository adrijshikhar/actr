import { useState, useEffect, useRef, useMemo, useCallback } from 'react';
import {
  Activity,
  Cpu,
  HardDrive,
  Network,
  Server,
  RefreshCw,
  LayoutGrid,
  Rows,
  Clock,
  RotateCw,
  Pause,
  Play,
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

const MAX_BUFFER_CAPACITY = 900; // 15 minutes of 1-second samples

const TIME_WINDOWS = [
  { value: 30, label: 'Last 30s' },
  { value: 60, label: 'Last 1m' },
  { value: 120, label: 'Last 2m' },
  { value: 300, label: 'Last 5m' },
  { value: 900, label: 'Last 15m' },
];

const REFRESH_INTERVALS = [
  { value: '1', label: '1s (Live)' },
  { value: '2', label: '2s' },
  { value: '5', label: '5s' },
  { value: '10', label: '10s' },
  { value: 'off', label: 'Off (Paused)' },
];

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
  // Visible samples state (frozen when paused, or throttled to cadence)
  const [metricsHistory, setMetricsHistory] = useState<TelemetrySample[]>([]);
  const [streamActive, setStreamActive] = useState(false);
  const [lastTick, setLastTick] = useState<string | null>(null);
  const [layoutMode, setLayoutMode] = useState<'grid' | 'stacked'>('grid');

  // Grafana-style Time Window control: 30s, 60s (default 1m), 120s (2m), 300s (5m), 900s (15m)
  const [timeWindow, setTimeWindow] = useState<number>(60);

  // Grafana-style Auto-Refresh control: '1' (live), '2', '5', '10', 'off' (paused)
  const [refreshCadence, setRefreshCadence] = useState<'1' | '2' | '5' | '10' | 'off'>('1');
  const [isPaused, setIsPaused] = useState<boolean>(false);
  const [isPolling, setIsPolling] = useState<boolean>(false);

  // High-frequency mutable refs to retain full telemetry stream without dropping samples during inspection
  const allSamplesRef = useRef<TelemetrySample[]>([]);
  const isPausedRef = useRef<boolean>(false);
  const cadenceRef = useRef<'1' | '2' | '5' | '10' | 'off'>('1');
  const lastRenderMsRef = useRef<number>(0);

  useEffect(() => {
    isPausedRef.current = isPaused;
  }, [isPaused]);

  useEffect(() => {
    cadenceRef.current = refreshCadence;
  }, [refreshCadence]);

  // Subscribe to real-time 1s SSE metrics stream
  useEffect(() => {
    // Prime initial baseline from status if available
    if (status && allSamplesRef.current.length === 0) {
      const initial = createHostSample({
        timestamp: new Date().toISOString(),
        host_cpu: status.host_cpu || 0,
        host_mem_used: status.host_mem_used || 0,
        host_mem_total: status.host_mem_total || 0,
      });
      allSamplesRef.current = [initial];
      setMetricsHistory([initial]);
    }

    const unsub = subscribeMetrics(
      (m: RunnerMetrics) => {
        setStreamActive(true);
        const tickTime = new Date().toLocaleTimeString();
        setLastTick(tickTime);

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

        // Push to retained background buffer (up to 15m)
        allSamplesRef.current.push(sample);
        if (allSamplesRef.current.length > MAX_BUFFER_CAPACITY) {
          allSamplesRef.current = allSamplesRef.current.slice(-MAX_BUFFER_CAPACITY);
        }

        // When paused: freeze chart updates completely so user can inspect without layout shift
        if (isPausedRef.current) {
          return;
        }

        // Apply selected auto-refresh cadence
        const currentCadence = cadenceRef.current;
        const now = Date.now();
        const cadenceSeconds = currentCadence === 'off' ? 1 : Number(currentCadence);

        if (cadenceSeconds <= 1 || now - lastRenderMsRef.current >= cadenceSeconds * 1000 - 100) {
          lastRenderMsRef.current = now;
          setMetricsHistory([...allSamplesRef.current]);
        }
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

  // Pause / Resume toggle handler
  const togglePause = useCallback(() => {
    setIsPaused((prev) => {
      const nextPaused = !prev;
      isPausedRef.current = nextPaused;
      if (nextPaused) {
        setRefreshCadence('off');
      } else {
        setRefreshCadence('1');
        cadenceRef.current = '1';
        lastRenderMsRef.current = Date.now();
        setMetricsHistory([...allSamplesRef.current]);
      }
      return nextPaused;
    });
  }, []);

  // Cadence selector change handler
  const handleCadenceChange = useCallback((value: string) => {
    const val = value as '1' | '2' | '5' | '10' | 'off';
    setRefreshCadence(val);
    cadenceRef.current = val;
    if (val === 'off') {
      setIsPaused(true);
      isPausedRef.current = true;
    } else {
      setIsPaused(false);
      isPausedRef.current = false;
      lastRenderMsRef.current = Date.now();
      setMetricsHistory([...allSamplesRef.current]);
    }
  }, []);

  // Manual Poll / Instantaneous Snapshot handler
  const handleManualPoll = useCallback(async () => {
    setIsPolling(true);
    try {
      if (onRefresh) {
        await onRefresh();
      }
      lastRenderMsRef.current = Date.now();
      setMetricsHistory([...allSamplesRef.current]);
    } finally {
      setTimeout(() => setIsPolling(false), 400);
    }
  }, [onRefresh]);

  // Sliced time-series data matching the selected time window (30s, 1m, 2m, 5m, 15m)
  const visibleMetrics = useMemo(() => {
    if (metricsHistory.length === 0) return [];
    return metricsHistory.slice(-timeWindow);
  }, [metricsHistory, timeWindow]);

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
      <div className="p-3 sm:p-4 rounded-geist bg-geist-surface border border-geist-hairline">
        <div className="flex flex-col md:flex-row md:items-center justify-between gap-3">
          {/* Left: Identity, Stream Status & Buffer Info */}
          <div className="space-y-1 min-w-0 pr-2">
            <div className="flex items-center gap-2 min-w-0">
              <Activity className="w-4 h-4 text-geist-blue flex-shrink-0" />
              <h2 className="text-sm font-semibold text-white tracking-tight whitespace-nowrap">Host Node Telemetry</h2>
              <Badge
                variant="outline"
                className={cn(
                  'text-[10px] font-mono px-1.5 py-0.5 gap-1 whitespace-nowrap transition-colors',
                  isPaused
                    ? 'text-status-warning-text border-status-warning-border bg-status-warning-bg/20'
                    : streamActive
                    ? 'text-status-success-solid border-status-success-border bg-status-success-bg/20'
                    : 'text-geist-faint border-geist-hairline bg-geist-hover'
                )}
              >
                <span
                  className={cn(
                    'w-1.5 h-1.5 rounded-full flex-shrink-0',
                    isPaused
                      ? 'bg-status-warning-text'
                      : streamActive
                      ? 'bg-status-success-solid animate-pulse'
                      : 'bg-geist-faint'
                  )}
                />
                {isPaused
                  ? 'Paused'
                  : streamActive
                  ? refreshCadence === '1'
                    ? 'Live (1s)'
                    : `${refreshCadence}s Stream`
                  : 'Connecting'}
              </Badge>
            </div>

            <div className="flex items-center gap-2 text-[11px] font-mono tracking-tight text-geist-faint truncate tabular-nums">
              <span className="text-geist-secondary font-sans truncate">Continuous kernel I/O & hardware telemetry</span>
              <span>·</span>
              <span className="whitespace-nowrap">Window: <span className="text-white font-medium">{visibleMetrics.length}s</span></span>
              <span>·</span>
              <span className="whitespace-nowrap">Buffer: <span className="text-white font-medium">{allSamplesRef.current.length}s</span></span>
              {lastTick && (
                <>
                  <span>·</span>
                  <span className="whitespace-nowrap">Last: <span className="text-white font-medium">{lastTick}</span></span>
                </>
              )}
            </div>
          </div>

          {/* Right: Consolidated Grafana-Style Toolbar (Single Row, Never Wraps) */}
          <div className="flex items-center gap-1.5 text-xs font-mono flex-shrink-0">
            {/* 1. Time Range / Window Control */}
            <div className="flex items-center gap-1 bg-geist-canvas border border-geist-hairline rounded px-2 py-1 text-[11px] font-mono tracking-tight text-geist-secondary hover:border-geist-borderHover transition-colors">
              <Clock className="w-3 h-3 text-geist-faint flex-shrink-0" />
              <label htmlFor="time-window-select" className="sr-only">
                Time Window Range
              </label>
              <select
                id="time-window-select"
                value={timeWindow}
                onChange={(e) => setTimeWindow(Number(e.target.value))}
                className="bg-transparent text-white font-medium text-[11px] font-mono focus:outline-none cursor-pointer pr-0.5"
                title="Select rolling time window"
              >
                {TIME_WINDOWS.map((w) => (
                  <option key={w.value} value={w.value} className="bg-geist-surface text-white">
                    {w.label}
                  </option>
                ))}
              </select>
            </div>

            {/* 2. Play / Pause Freeze Toggle */}
            <button
              type="button"
              onClick={togglePause}
              aria-label={isPaused ? 'Resume live metrics stream' : 'Pause metrics stream'}
              className={cn(
                'flex items-center gap-1 px-2 py-1 rounded text-[11px] font-mono tracking-tight transition-colors border cursor-pointer select-none',
                isPaused
                  ? 'border-status-warning-border bg-status-warning-bg/30 text-status-warning-text hover:bg-status-warning-bg/50'
                  : 'border-geist-hairline bg-geist-canvas text-geist-secondary hover:text-white hover:bg-geist-hover'
              )}
              title={isPaused ? 'Resume live streaming' : 'Freeze graph to inspect data points'}
            >
              {isPaused ? (
                <>
                  <Play className="w-3 h-3 fill-status-warning-text text-status-warning-text" />
                  <span>Resume</span>
                </>
              ) : (
                <>
                  <Pause className="w-3 h-3" />
                  <span>Pause</span>
                </>
              )}
            </button>

            {/* 3. Auto-Refresh Cadence Selector */}
            <div className="flex items-center gap-1 bg-geist-canvas border border-geist-hairline rounded px-2 py-1 text-[11px] font-mono tracking-tight text-geist-secondary hover:border-geist-borderHover transition-colors">
              <RotateCw className="w-3 h-3 text-geist-faint flex-shrink-0" />
              <label htmlFor="refresh-cadence-select" className="sr-only">
                Auto-Refresh Cadence
              </label>
              <select
                id="refresh-cadence-select"
                value={refreshCadence}
                onChange={(e) => handleCadenceChange(e.target.value)}
                className="bg-transparent text-white font-medium text-[11px] font-mono focus:outline-none cursor-pointer pr-0.5"
                title="Select auto-refresh interval"
              >
                {REFRESH_INTERVALS.map((r) => (
                  <option key={r.value} value={r.value} className="bg-geist-surface text-white">
                    {r.label}
                  </option>
                ))}
              </select>
            </div>

            {/* 4. Manual Poll Button */}
            <Button
              variant="outline"
              size="sm"
              onClick={handleManualPoll}
              disabled={isPolling}
              className="h-[26px] text-[11px] font-mono tracking-tight px-2 gap-1 border-geist-hairline bg-geist-canvas hover:bg-geist-hover text-geist-secondary hover:text-white"
              title="Fetch instantaneous telemetry snapshot"
            >
              <RefreshCw className={cn('w-3 h-3', isPolling && 'animate-spin text-geist-blue')} />
              <span>Poll</span>
            </Button>

            {/* 5. Layout Mode Switcher (Icon-only compact segmented button) */}
            <div className="flex items-center border border-geist-hairline rounded bg-geist-canvas p-0.5 ml-0.5">
              <button
                type="button"
                onClick={() => setLayoutMode('grid')}
                className={cn(
                  'p-1 rounded transition-colors cursor-pointer',
                  layoutMode === 'grid'
                    ? 'bg-geist-elevated text-white shadow-sm'
                    : 'text-geist-faint hover:text-white'
                )}
                title="2x2 Grid View"
                aria-label="2x2 Grid View"
              >
                <LayoutGrid className="w-3.5 h-3.5" />
              </button>
              <button
                type="button"
                onClick={() => setLayoutMode('stacked')}
                className={cn(
                  'p-1 rounded transition-colors cursor-pointer',
                  layoutMode === 'stacked'
                    ? 'bg-geist-elevated text-white shadow-sm'
                    : 'text-geist-faint hover:text-white'
                )}
                title="Stacked View"
                aria-label="Stacked View"
              >
                <Rows className="w-3.5 h-3.5" />
              </button>
            </div>
          </div>
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
          data={visibleMetrics}
          height={layoutMode === 'grid' ? 200 : 240}
          title="CPU Utilization"
          description="Physical core execution load over time"
          showCard={true}
        />

        {/* 2. Memory Allocation Graph */}
        <MemoryAreaChart
          data={visibleMetrics}
          height={layoutMode === 'grid' ? 200 : 240}
          totalMemoryBytes={status?.host_mem_total}
          title="Memory Allocation"
          description="Physical RAM resident set size and host allocation"
          showCard={true}
        />

        {/* 3. Disk Write Activity Graph */}
        <DiskAreaChart
          data={visibleMetrics}
          height={layoutMode === 'grid' ? 200 : 240}
          title="Disk Write Activity"
          description="Real-time storage controller commit rate"
          showCard={true}
        />

        {/* 4. Network Bandwidth Graph */}
        <NetworkAreaChart
          data={visibleMetrics}
          height={layoutMode === 'grid' ? 200 : 240}
          title="Network Bandwidth"
          description="Socket throughput (RX / TX)"
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
            <span className="text-[11px] text-geist-faint block">
              Cadence {isPaused ? 'Paused' : `${refreshCadence}s`} · Window {timeWindow}s
            </span>
          </div>
        </div>
      </div>
    </div>
  );
}

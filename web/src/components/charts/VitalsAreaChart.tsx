import { useMemo, useId } from 'react';
import {
  ResponsiveContainer,
  AreaChart,
  Area,
  XAxis,
  YAxis,
  CartesianGrid,
  Tooltip,
  Legend,
} from 'recharts';
import { Activity } from 'lucide-react';
import type { TelemetrySample } from '@/lib/api';
import { cn, formatBytes } from '@/lib/utils';

export function formatElapsed(seconds: number): string {
  if (!seconds || seconds <= 0) return '00:00';
  const hours = Math.floor(seconds / 3600);
  const mins = Math.floor((seconds % 3600) / 60);
  const secs = Math.floor(seconds % 60);
  if (hours > 0) {
    return `${String(hours).padStart(2, '0')}:${String(mins).padStart(2, '0')}:${String(secs).padStart(2, '0')}`;
  }
  return `${String(mins).padStart(2, '0')}:${String(secs).padStart(2, '0')}`;
}

export interface VitalsAreaChartProps {
  data?: TelemetrySample[];
  height?: number;
  className?: string;
  title?: string;
  description?: string;
  showLegend?: boolean;
  showCard?: boolean;
}

interface ProcessedDataPoint {
  elapsedSec: number;
  elapsedLabel: string;
  timeFormatted: string;
  cpu: number;
  memory: number;
  stepName?: string;
  raw: TelemetrySample;
}

interface CustomTooltipProps {
  active?: boolean;
  payload?: Array<{
    value: number;
    payload: ProcessedDataPoint;
    dataKey: string;
    color: string;
  }>;
  label?: string;
}

function VitalsTooltip({ active, payload }: CustomTooltipProps) {
  if (!active || !payload || payload.length === 0) return null;

  const point = payload[0]?.payload;
  if (!point) return null;

  return (
    <div className="rounded-geist bg-geist-surface/95 backdrop-blur-md border border-geist-hairline p-3 shadow-2xl text-xs font-mono space-y-2 min-w-[200px]">
      <div className="flex items-center justify-between pb-1.5 border-b border-geist-hairline">
        <div className="flex items-center gap-1.5 truncate max-w-[130px]">
          <span className="w-1.5 h-1.5 rounded-full bg-geist-blue flex-shrink-0" />
          <span className="font-semibold text-white truncate text-[11px]">
            {point.stepName || 'active-step'}
          </span>
        </div>
        <div className="text-[10px] text-geist-faint flex items-center gap-1">
          <span>{point.elapsedLabel}</span>
          <span>•</span>
          <span>{point.timeFormatted}</span>
        </div>
      </div>

      <div className="space-y-1.5">
        <div className="flex items-center justify-between">
          <div className="flex items-center gap-1.5 text-geist-secondary">
            <span className="w-2 h-2 rounded-full bg-[#0070f3]" />
            <span>CPU</span>
          </div>
          <span className="font-semibold text-white">{point.cpu.toFixed(1)}%</span>
        </div>

        <div className="flex items-center justify-between">
          <div className="flex items-center gap-1.5 text-geist-secondary">
            <span className="w-2 h-2 rounded-full bg-[#30a46c]" />
            <span>Memory (RSS)</span>
          </div>
          <span className="font-semibold text-white">{formatBytes(point.memory)}</span>
        </div>

        {point.raw.step_working_set_bytes ? (
          <div className="flex items-center justify-between text-[11px] text-geist-faint pt-0.5">
            <span>Working Set</span>
            <span>{formatBytes(point.raw.step_working_set_bytes)}</span>
          </div>
        ) : null}

        {point.raw.host_cpu !== undefined && point.raw.host_cpu !== point.cpu ? (
          <div className="flex items-center justify-between text-[11px] text-geist-faint">
            <span>Host Node CPU</span>
            <span>{point.raw.host_cpu.toFixed(1)}%</span>
          </div>
        ) : null}
      </div>
    </div>
  );
}

export function VitalsAreaChart({
  data,
  height = 260,
  className,
  title = 'Telemetry Vitals',
  description,
  showLegend = true,
  showCard = true,
}: VitalsAreaChartProps) {
  const reactId = useId().replace(/:/g, '');
  const cpuGradientId = `vitals-cpu-${reactId}`;
  const memGradientId = `vitals-mem-${reactId}`;
  const heightClass = height <= 200 ? 'h-52' : 'h-60';

  const chartData = useMemo<ProcessedDataPoint[]>(() => {
    if (!data || data.length === 0) return [];

    const hasElapsed = data.some((d) => (d.step_elapsed_sec ?? 0) > 0);
    const startMs = data[0]?.timestamp ? new Date(data[0].timestamp).getTime() : 0;

    return data.map((d) => {
      let elapsedSec = d.step_elapsed_sec ?? 0;
      if (!hasElapsed && startMs > 0 && d.timestamp) {
        const currentMs = new Date(d.timestamp).getTime();
        elapsedSec = Math.max(0, Math.floor((currentMs - startMs) / 1000));
      }

      const cpu = d.step_cpu > 0 ? d.step_cpu : (d.host_cpu ?? 0);
      const memory = d.step_rss_bytes > 0 ? d.step_rss_bytes : (d.host_mem_used ?? 0);
      const timeFormatted = d.timestamp
        ? new Date(d.timestamp).toLocaleTimeString([], { hour12: false })
        : `T+${elapsedSec}s`;

      return {
        elapsedSec,
        elapsedLabel: formatElapsed(elapsedSec),
        timeFormatted,
        cpu: Number(cpu.toFixed(1)),
        memory,
        stepName: d.active_step_name || undefined,
        raw: d,
      };
    });
  }, [data]);

  const latestSample = chartData[chartData.length - 1];

  const content = (
    <>
      <div className="flex items-center justify-between mb-4">
        <div>
          <div className="flex items-center gap-2">
            <Activity className="w-4 h-4 text-geist-blue" />
            <h3 className="text-xs font-semibold text-white tracking-tight">{title}</h3>
          </div>
          {description && (
            <p className="text-[11px] text-geist-faint mt-0.5">{description}</p>
          )}
        </div>

        {latestSample && (
          <div className="flex items-center gap-3 text-xs font-mono">
            <div className="flex items-center gap-1.5">
              <span className="w-2 h-2 rounded-full bg-[#0070f3]" />
              <span className="text-geist-secondary">CPU:</span>
              <span className="text-white font-medium">{latestSample.cpu.toFixed(1)}%</span>
            </div>
            <div className="flex items-center gap-1.5">
              <span className="w-2 h-2 rounded-full bg-[#30a46c]" />
              <span className="text-geist-secondary">RSS:</span>
              <span className="text-white font-medium">{formatBytes(latestSample.memory)}</span>
            </div>
          </div>
        )}
      </div>

      {chartData.length === 0 ? (
        <div className={cn("w-full flex flex-col items-center justify-center text-center rounded-geist bg-geist-canvas border border-geist-hairlineSoft select-none", heightClass)}>
          <Activity className="w-7 h-7 text-geist-faint mb-2 opacity-50" />
          <p className="text-xs font-medium text-geist-body">No telemetry samples recorded</p>
          <p className="text-[11px] text-geist-faint mt-0.5">
            CPU & memory metrics will stream in real time during execution
          </p>
        </div>
      ) : (
        <div className={cn("w-full", heightClass)}>
          <ResponsiveContainer width="100%" height="100%" minWidth={0} minHeight={0}>
            <AreaChart data={chartData} margin={{ top: 10, right: 10, left: -15, bottom: 0 }}>
              <defs>
                <linearGradient id={cpuGradientId} x1="0" y1="0" x2="0" y2="1">
                  <stop offset="5%" stopColor="#0070f3" stopOpacity={0.35} />
                  <stop offset="95%" stopColor="#0070f3" stopOpacity={0.0} />
                </linearGradient>
                <linearGradient id={memGradientId} x1="0" y1="0" x2="0" y2="1">
                  <stop offset="5%" stopColor="#30a46c" stopOpacity={0.35} />
                  <stop offset="95%" stopColor="#30a46c" stopOpacity={0.0} />
                </linearGradient>
              </defs>

              <CartesianGrid
                stroke="#1f1f1f"
                strokeDasharray="3 3"
                vertical={false}
              />

              <XAxis
                dataKey="elapsedLabel"
                stroke="#666666"
                fontSize={11}
                fontFamily="Geist Mono, monospace"
                tickLine={false}
                axisLine={{ stroke: '#262626' }}
              />

              {/* Left Y-Axis: CPU % (0-100) */}
              <YAxis
                yAxisId="cpu"
                orientation="left"
                domain={[0, 100]}
                stroke="#666666"
                fontSize={11}
                fontFamily="Geist Mono, monospace"
                tickFormatter={(v: number) => `${Math.round(v)}%`}
                tickLine={false}
                axisLine={{ stroke: '#262626' }}
              />

              {/* Right Y-Axis: Memory RSS (MB/GB) */}
              <YAxis
                yAxisId="mem"
                orientation="right"
                domain={[0, (dataMax: number) => (dataMax > 0 ? Math.ceil(dataMax * 1.15) : 1024 * 1024 * 100)]}
                stroke="#666666"
                fontSize={11}
                fontFamily="Geist Mono, monospace"
                tickFormatter={(v: number) => formatBytes(v, 0)}
                tickLine={false}
                axisLine={{ stroke: '#262626' }}
              />

              <Tooltip content={<VitalsTooltip />} />

              {showLegend && (
                <Legend
                  verticalAlign="top"
                  align="right"
                  className="pb-2 text-[11px] font-mono"
                  formatter={(value) => <span className="text-geist-secondary text-[11px]">{value}</span>}
                />
              )}

              <Area
                yAxisId="cpu"
                type="monotone"
                dataKey="cpu"
                name="CPU %"
                stroke="#0070f3"
                strokeWidth={1.5}
                fillOpacity={1}
                fill={`url(#${cpuGradientId})`}
                isAnimationActive={false}
              />

              <Area
                yAxisId="mem"
                type="monotone"
                dataKey="memory"
                name="Memory (RSS)"
                stroke="#30a46c"
                strokeWidth={1.5}
                fillOpacity={1}
                fill={`url(#${memGradientId})`}
                isAnimationActive={false}
              />
            </AreaChart>
          </ResponsiveContainer>
        </div>
      )}
    </>
  );

  if (!showCard) {
    return <div className={className}>{content}</div>;
  }

  return (
    <div
      className={cn(
        'rounded-geist bg-geist-surface border border-geist-hairline p-4 flex flex-col',
        className
      )}
    >
      {content}
    </div>
  );
}

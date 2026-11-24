import { useMemo, useId } from 'react';
import {
  ResponsiveContainer,
  AreaChart,
  Area,
  XAxis,
  YAxis,
  CartesianGrid,
  Tooltip,
} from 'recharts';
import { HardDrive } from 'lucide-react';
import type { TelemetrySample } from '@/lib/api';
import { cn, formatBytes } from '@/lib/utils';
import { formatElapsed } from './VitalsAreaChart';

export interface MemoryAreaChartProps {
  data?: TelemetrySample[];
  height?: number;
  totalMemoryBytes?: number;
  className?: string;
  title?: string;
  description?: string;
  showCard?: boolean;
}

interface ProcessedMemoryPoint {
  elapsedSec: number;
  elapsedLabel: string;
  timeFormatted: string;
  memory: number;
  total: number;
  raw: TelemetrySample;
}

interface CustomTooltipProps {
  active?: boolean;
  payload?: Array<{
    value: number;
    payload: ProcessedMemoryPoint;
  }>;
}

function MemoryTooltip({ active, payload }: CustomTooltipProps) {
  if (!active || !payload || payload.length === 0) return null;
  const point = payload[0]?.payload;
  if (!point) return null;

  const percent = point.total > 0 ? Math.round((point.memory / point.total) * 100) : 0;
  const free = Math.max(point.total - point.memory, 0);

  return (
    <div className="rounded-geist bg-geist-surface/95 backdrop-blur-md border border-geist-hairline p-3 shadow-2xl text-xs font-mono space-y-1.5 min-w-[210px]">
      <div className="flex items-center justify-between pb-1.5 border-b border-geist-hairline text-geist-faint text-[10px]">
        <span className="text-white font-semibold flex items-center gap-1.5">
          <span className="w-1.5 h-1.5 rounded-full bg-status-success-solid" />
          Host RAM (RSS)
        </span>
        <span>{point.timeFormatted}</span>
      </div>
      <div className="flex items-center justify-between text-xs">
        <span className="text-geist-secondary">Used (RSS)</span>
        <span className="font-semibold text-white">{formatBytes(point.memory)}</span>
      </div>
      {point.total > 0 && (
        <>
          <div className="flex items-center justify-between text-[11px] text-geist-faint">
            <span>Utilization</span>
            <span>{percent}%</span>
          </div>
          <div className="flex items-center justify-between text-[11px] text-geist-faint">
            <span>Available</span>
            <span>{formatBytes(free)}</span>
          </div>
        </>
      )}
    </div>
  );
}

export function MemoryAreaChart({
  data,
  height = 220,
  totalMemoryBytes,
  className,
  title = 'Memory Allocation',
  description = 'Physical memory resident set size and host allocation',
  showCard = true,
}: MemoryAreaChartProps) {
  const reactId = useId().replace(/:/g, '');
  const gradientId = `mem-grad-${reactId}`;
  const heightClass = height <= 200 ? 'h-48' : height <= 240 ? 'h-56' : 'h-64';

  const hasElapsed = useMemo(() => {
    return Boolean(data && data.some((d) => (d.step_elapsed_sec ?? 0) > 0));
  }, [data]);

  const totalMem = useMemo(() => {
    if (totalMemoryBytes && totalMemoryBytes > 0) return totalMemoryBytes;
    const fromSample = data?.find((d) => (d.host_mem_total ?? 0) > 0)?.host_mem_total;
    return fromSample && fromSample > 0 ? fromSample : undefined;
  }, [data, totalMemoryBytes]);

  const chartData = useMemo<ProcessedMemoryPoint[]>(() => {
    if (!data || data.length === 0) return [];
    const startMs = data[0]?.timestamp ? new Date(data[0].timestamp).getTime() : 0;

    return data.map((d) => {
      let elapsedSec = d.step_elapsed_sec ?? 0;
      if (!hasElapsed && startMs > 0 && d.timestamp) {
        const currentMs = new Date(d.timestamp).getTime();
        elapsedSec = Math.max(0, Math.floor((currentMs - startMs) / 1000));
      }

      const memory = d.host_mem_used > 0 ? d.host_mem_used : (d.step_rss_bytes ?? 0);
      const total = d.host_mem_total > 0 ? d.host_mem_total : (totalMem ?? 0);
      const timeFormatted = d.timestamp
        ? new Date(d.timestamp).toLocaleTimeString([], { hour12: false })
        : `T+${elapsedSec}s`;

      return {
        elapsedSec,
        elapsedLabel: formatElapsed(elapsedSec),
        timeFormatted,
        memory,
        total,
        raw: d,
      };
    });
  }, [data, hasElapsed, totalMem]);

  const latestSample = chartData[chartData.length - 1];
  const percentUsed =
    latestSample && latestSample.total > 0
      ? Math.round((latestSample.memory / latestSample.total) * 100)
      : 0;

  const content = (
    <>
      <div className="flex items-center justify-between mb-3">
        <div>
          <div className="flex items-center gap-2">
            <HardDrive className="w-4 h-4 text-status-success-solid" />
            <h3 className="text-xs font-semibold text-white tracking-tight">{title}</h3>
          </div>
          {description && (
            <p className="text-[11px] text-geist-faint mt-0.5">{description}</p>
          )}
        </div>

        {latestSample && (
          <div className="flex items-center gap-2.5 text-xs font-mono">
            <div className="flex items-center gap-1.5">
              <span className="w-2 h-2 rounded-full bg-[#30a46c] animate-pulse" />
              <span className="text-geist-secondary">Used:</span>
              <span className="text-white font-semibold">{formatBytes(latestSample.memory)}</span>
            </div>
            {latestSample.total > 0 && (
              <span className="text-geist-faint text-[11px]">
                ({percentUsed}% of {formatBytes(latestSample.total)})
              </span>
            )}
          </div>
        )}
      </div>

      {chartData.length === 0 ? (
        <div className={cn('w-full flex flex-col items-center justify-center text-center rounded-geist bg-geist-canvas border border-geist-hairlineSoft select-none', heightClass)}>
          <HardDrive className="w-6 h-6 text-geist-faint mb-2 opacity-50" />
          <p className="text-xs font-medium text-geist-body">No memory samples recorded</p>
          <p className="text-[11px] text-geist-faint mt-0.5">Metrics stream every second via SSE</p>
        </div>
      ) : (
        <div className={cn('w-full', heightClass)}>
          <ResponsiveContainer width="100%" height="100%" minWidth={0} minHeight={0}>
            <AreaChart data={chartData} margin={{ top: 8, right: 12, left: 0, bottom: 0 }}>
              <defs>
                <linearGradient id={gradientId} x1="0" y1="0" x2="0" y2="1">
                  <stop offset="5%" stopColor="#30a46c" stopOpacity={0.35} />
                  <stop offset="95%" stopColor="#30a46c" stopOpacity={0.0} />
                </linearGradient>
              </defs>

              <CartesianGrid stroke="#1f1f1f" strokeDasharray="3 3" vertical={false} />

              <XAxis
                dataKey={hasElapsed ? 'elapsedLabel' : 'timeFormatted'}
                stroke="#666666"
                fontSize={11}
                fontFamily="Geist Mono, monospace"
                tickLine={false}
                axisLine={{ stroke: '#262626' }}
                minTickGap={45}
              />

              <YAxis
                domain={totalMem ? [0, totalMem] : [0, 'auto']}
                stroke="#666666"
                fontSize={11}
                fontFamily="Geist Mono, monospace"
                tickFormatter={(v: number) => formatBytes(v, 0)}
                tickLine={false}
                axisLine={{ stroke: '#262626' }}
                width={45}
              />

              <Tooltip content={<MemoryTooltip />} />

              <Area
                type="monotone"
                dataKey="memory"
                name="Memory"
                stroke="#30a46c"
                strokeWidth={1.5}
                fillOpacity={1}
                fill={`url(#${gradientId})`}
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
    <div className={cn('rounded-geist bg-geist-surface border border-geist-hairline p-4 flex flex-col', className)}>
      {content}
    </div>
  );
}

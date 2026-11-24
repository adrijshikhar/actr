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
import { Cpu } from 'lucide-react';
import type { TelemetrySample } from '@/lib/api';
import { cn } from '@/lib/utils';
import { formatElapsed } from './VitalsAreaChart';

export interface CpuAreaChartProps {
  data?: TelemetrySample[];
  height?: number;
  className?: string;
  title?: string;
  description?: string;
  showCard?: boolean;
}

interface ProcessedCpuPoint {
  elapsedSec: number;
  elapsedLabel: string;
  timeFormatted: string;
  cpu: number;
  load1m: number;
  raw: TelemetrySample;
}

interface CustomTooltipProps {
  active?: boolean;
  payload?: Array<{
    value: number;
    payload: ProcessedCpuPoint;
  }>;
}

function CpuTooltip({ active, payload }: CustomTooltipProps) {
  if (!active || !payload || payload.length === 0) return null;
  const point = payload[0]?.payload;
  if (!point) return null;

  return (
    <div className="rounded-geist bg-geist-surface/95 backdrop-blur-md border border-geist-hairline p-3 shadow-2xl text-xs font-mono space-y-1.5 min-w-[190px]">
      <div className="flex items-center justify-between pb-1.5 border-b border-geist-hairline text-geist-faint text-[10px]">
        <span className="text-white font-semibold flex items-center gap-1.5">
          <span className="w-1.5 h-1.5 rounded-full bg-geist-blue" />
          CPU Core
        </span>
        <span>{point.timeFormatted}</span>
      </div>
      <div className="flex items-center justify-between text-xs">
        <span className="text-geist-secondary">Utilization</span>
        <span className="font-semibold text-white">{point.cpu.toFixed(1)}%</span>
      </div>
      {point.load1m > 0 && (
        <div className="flex items-center justify-between text-[11px] text-geist-faint">
          <span>Load 1m</span>
          <span>{point.load1m.toFixed(2)}</span>
        </div>
      )}
    </div>
  );
}

export function CpuAreaChart({
  data,
  height = 220,
  className,
  title = 'CPU Utilization',
  description = 'Physical processor core execution load over time',
  showCard = true,
}: CpuAreaChartProps) {
  const reactId = useId().replace(/:/g, '');
  const gradientId = `cpu-grad-${reactId}`;
  const heightClass = height <= 200 ? 'h-48' : height <= 240 ? 'h-56' : 'h-64';

  const hasElapsed = useMemo(() => {
    return Boolean(data && data.some((d) => (d.step_elapsed_sec ?? 0) > 0));
  }, [data]);

  const chartData = useMemo<ProcessedCpuPoint[]>(() => {
    if (!data || data.length === 0) return [];
    const startMs = data[0]?.timestamp ? new Date(data[0].timestamp).getTime() : 0;

    return data.map((d) => {
      let elapsedSec = d.step_elapsed_sec ?? 0;
      if (!hasElapsed && startMs > 0 && d.timestamp) {
        const currentMs = new Date(d.timestamp).getTime();
        elapsedSec = Math.max(0, Math.floor((currentMs - startMs) / 1000));
      }

      const cpu = d.host_cpu > 0 ? d.host_cpu : (d.step_cpu ?? 0);
      const timeFormatted = d.timestamp
        ? new Date(d.timestamp).toLocaleTimeString([], { hour12: false })
        : `T+${elapsedSec}s`;

      return {
        elapsedSec,
        elapsedLabel: formatElapsed(elapsedSec),
        timeFormatted,
        cpu: Number(cpu.toFixed(1)),
        load1m: d.host_load_1m || 0,
        raw: d,
      };
    });
  }, [data, hasElapsed]);

  const latestSample = chartData[chartData.length - 1];

  const content = (
    <>
      <div className="flex items-center justify-between gap-2 mb-3 min-w-0">
        <div className="min-w-0 pr-2">
          <div className="flex items-center gap-1.5">
            <Cpu className="w-3.5 h-3.5 text-geist-blue flex-shrink-0" />
            <h3 className="text-xs font-semibold text-white tracking-tight truncate">{title}</h3>
          </div>
          {description && (
            <p className="text-[11px] text-geist-faint truncate mt-0.5">{description}</p>
          )}
        </div>

        {latestSample && (
          <div className="flex items-center gap-2 text-[11px] font-mono tracking-tight whitespace-nowrap flex-shrink-0 tabular-nums">
            <span className="inline-flex items-center gap-1 whitespace-nowrap">
              <span className="w-1.5 h-1.5 rounded-full bg-[#0070f3] animate-pulse flex-shrink-0" />
              <span className="text-geist-faint">Active:</span>
              <span className="text-white font-medium">{latestSample.cpu.toFixed(1)}%</span>
            </span>
            {latestSample.load1m > 0 && (
              <span className="text-geist-faint text-[10px] hidden sm:inline whitespace-nowrap">
                (Load: {latestSample.load1m.toFixed(2)})
              </span>
            )}
          </div>
        )}
      </div>

      {chartData.length === 0 ? (
        <div className={cn('w-full flex flex-col items-center justify-center text-center rounded-geist bg-geist-canvas border border-geist-hairlineSoft select-none', heightClass)}>
          <Cpu className="w-6 h-6 text-geist-faint mb-2 opacity-50" />
          <p className="text-xs font-medium text-geist-body">No CPU samples recorded</p>
          <p className="text-[11px] text-geist-faint mt-0.5">Metrics stream every second via SSE</p>
        </div>
      ) : (
        <div className={cn('w-full', heightClass)}>
          <ResponsiveContainer width="100%" height="100%" minWidth={0} minHeight={0}>
            <AreaChart data={chartData} margin={{ top: 8, right: 28, left: 0, bottom: 0 }}>
              <defs>
                <linearGradient id={gradientId} x1="0" y1="0" x2="0" y2="1">
                  <stop offset="5%" stopColor="#0070f3" stopOpacity={0.35} />
                  <stop offset="95%" stopColor="#0070f3" stopOpacity={0.0} />
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
                domain={[0, 100]}
                stroke="#666666"
                fontSize={11}
                fontFamily="Geist Mono, monospace"
                tickFormatter={(v: number) => `${Math.round(v)}%`}
                tickLine={false}
                axisLine={{ stroke: '#262626' }}
                width={38}
              />

              <Tooltip content={<CpuTooltip />} />

              <Area
                type="monotone"
                dataKey="cpu"
                name="CPU %"
                stroke="#0070f3"
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

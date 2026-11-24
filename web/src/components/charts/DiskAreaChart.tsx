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
import { cn, formatBytes, formatRate } from '@/lib/utils';
import { formatElapsed } from './VitalsAreaChart';

export interface DiskAreaChartProps {
  data?: TelemetrySample[];
  height?: number;
  className?: string;
  title?: string;
  description?: string;
  showCard?: boolean;
}

interface ProcessedDiskPoint {
  elapsedSec: number;
  elapsedLabel: string;
  timeFormatted: string;
  writeRate: number;
  usedBytes: number;
  raw: TelemetrySample;
}

interface CustomTooltipProps {
  active?: boolean;
  payload?: Array<{
    value: number;
    payload: ProcessedDiskPoint;
  }>;
}

function DiskTooltip({ active, payload }: CustomTooltipProps) {
  if (!active || !payload || payload.length === 0) return null;
  const point = payload[0]?.payload;
  if (!point) return null;

  return (
    <div className="rounded-geist bg-geist-surface/95 backdrop-blur-md border border-geist-hairline p-3 shadow-2xl text-xs font-mono space-y-1.5 min-w-[200px]">
      <div className="flex items-center justify-between pb-1.5 border-b border-geist-hairline text-geist-faint text-[10px]">
        <span className="text-white font-semibold flex items-center gap-1.5">
          <span className="w-1.5 h-1.5 rounded-full bg-[#f5a623]" />
          Disk I/O
        </span>
        <span>{point.timeFormatted}</span>
      </div>
      <div className="flex items-center justify-between text-xs">
        <span className="text-geist-secondary">Write Rate</span>
        <span className="font-semibold text-white">{formatRate(point.writeRate)}</span>
      </div>
      {point.usedBytes > 0 && (
        <div className="flex items-center justify-between text-[11px] text-geist-faint">
          <span>Storage Used</span>
          <span>{formatBytes(point.usedBytes)}</span>
        </div>
      )}
    </div>
  );
}

export function DiskAreaChart({
  data,
  height = 220,
  className,
  title = 'Disk Write Activity',
  description = 'Storage controller commit rate over time',
  showCard = true,
}: DiskAreaChartProps) {
  const reactId = useId().replace(/:/g, '');
  const gradientId = `disk-grad-${reactId}`;
  const heightClass = height <= 200 ? 'h-48' : height <= 240 ? 'h-56' : 'h-64';

  const hasElapsed = useMemo(() => {
    return Boolean(data && data.some((d) => (d.step_elapsed_sec ?? 0) > 0));
  }, [data]);

  const chartData = useMemo<ProcessedDiskPoint[]>(() => {
    if (!data || data.length === 0) return [];
    const startMs = data[0]?.timestamp ? new Date(data[0].timestamp).getTime() : 0;

    return data.map((d) => {
      let elapsedSec = d.step_elapsed_sec ?? 0;
      if (!hasElapsed && startMs > 0 && d.timestamp) {
        const currentMs = new Date(d.timestamp).getTime();
        elapsedSec = Math.max(0, Math.floor((currentMs - startMs) / 1000));
      }

      const timeFormatted = d.timestamp
        ? new Date(d.timestamp).toLocaleTimeString([], { hour12: false })
        : `T+${elapsedSec}s`;

      return {
        elapsedSec,
        elapsedLabel: formatElapsed(elapsedSec),
        timeFormatted,
        writeRate: d.disk_write_rate || 0,
        usedBytes: d.disk_used_bytes || 0,
        raw: d,
      };
    });
  }, [data, hasElapsed]);

  const latestSample = chartData[chartData.length - 1];

  const content = (
    <>
      <div className="flex items-center justify-between mb-3">
        <div>
          <div className="flex items-center gap-2">
            <HardDrive className="w-4 h-4 text-status-warning-text" />
            <h3 className="text-xs font-semibold text-white tracking-tight">{title}</h3>
          </div>
          {description && (
            <p className="text-[11px] text-geist-faint mt-0.5">{description}</p>
          )}
        </div>

        {latestSample && (
          <div className="flex items-center gap-2 text-xs font-mono">
            <span className="w-2 h-2 rounded-full bg-[#f5a623] animate-pulse" />
            <span className="text-geist-secondary">Write:</span>
            <span className="text-white font-semibold">{formatRate(latestSample.writeRate)}</span>
          </div>
        )}
      </div>

      {chartData.length === 0 ? (
        <div className={cn('w-full flex flex-col items-center justify-center text-center rounded-geist bg-geist-canvas border border-geist-hairlineSoft select-none', heightClass)}>
          <HardDrive className="w-6 h-6 text-geist-faint mb-2 opacity-50" />
          <p className="text-xs font-medium text-geist-body">No disk I/O recorded</p>
          <p className="text-[11px] text-geist-faint mt-0.5">Metrics stream every second via SSE</p>
        </div>
      ) : (
        <div className={cn('w-full', heightClass)}>
          <ResponsiveContainer width="100%" height="100%" minWidth={0} minHeight={0}>
            <AreaChart data={chartData} margin={{ top: 8, right: 28, left: 0, bottom: 0 }}>
              <defs>
                <linearGradient id={gradientId} x1="0" y1="0" x2="0" y2="1">
                  <stop offset="5%" stopColor="#f5a623" stopOpacity={0.35} />
                  <stop offset="95%" stopColor="#f5a623" stopOpacity={0.0} />
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
                domain={[0, (dataMax: number) => (dataMax > 0 ? Math.ceil(dataMax * 1.2) : 1024 * 1024)]}
                stroke="#666666"
                fontSize={11}
                fontFamily="Geist Mono, monospace"
                tickFormatter={(v: number) => formatRate(v, 0)}
                tickLine={false}
                axisLine={{ stroke: '#262626' }}
                width={52}
              />

              <Tooltip content={<DiskTooltip />} />

              <Area
                type="monotone"
                dataKey="writeRate"
                name="Disk Write"
                stroke="#f5a623"
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

import { useMemo, useId } from 'react';
import {
  ResponsiveContainer,
  AreaChart,
  Area,
  Line,
  XAxis,
  YAxis,
  CartesianGrid,
  Tooltip,
  Legend,
} from 'recharts';
import { HardDrive, ArrowDown, ArrowUp } from 'lucide-react';
import type { TelemetrySample } from '@/lib/api';
import { cn } from '@/lib/utils';
import { formatElapsed } from './VitalsAreaChart';

export function formatRate(bytesPerSec: number, decimals = 1): string {
  if (!bytesPerSec || bytesPerSec <= 0) return '0 B/s';
  const k = 1024;
  const sizes = ['B/s', 'KB/s', 'MB/s', 'GB/s', 'TB/s'];
  const i = Math.floor(Math.log(bytesPerSec) / Math.log(k));
  const safeI = Math.min(i, sizes.length - 1);
  return `${parseFloat((bytesPerSec / Math.pow(k, safeI)).toFixed(decimals))} ${sizes[safeI]}`;
}

export interface ThroughputChartProps {
  data?: TelemetrySample[];
  height?: number;
  className?: string;
  title?: string;
  description?: string;
  showLegend?: boolean;
  showCard?: boolean;
}

interface ProcessedThroughputPoint {
  elapsedSec: number;
  elapsedLabel: string;
  timeFormatted: string;
  diskWriteRate: number;
  netRxRate: number;
  netTxRate: number;
  stepName?: string;
  raw: TelemetrySample;
}

interface CustomTooltipProps {
  active?: boolean;
  payload?: Array<{
    value: number;
    payload: ProcessedThroughputPoint;
    dataKey: string;
    color: string;
  }>;
  label?: string;
}

function ThroughputTooltip({ active, payload }: CustomTooltipProps) {
  if (!active || !payload || payload.length === 0) return null;

  const point = payload[0]?.payload;
  if (!point) return null;

  return (
    <div className="rounded-geist bg-geist-surface/95 backdrop-blur-md border border-geist-hairline p-3 shadow-2xl text-xs font-mono space-y-2 min-w-[210px]">
      <div className="flex items-center justify-between pb-1.5 border-b border-geist-hairline">
        <div className="flex items-center gap-1.5 truncate max-w-[130px]">
          <span className="w-1.5 h-1.5 rounded-full bg-geist-warning flex-shrink-0" />
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
            <span className="w-2 h-2 rounded-full bg-[#f5a623]" />
            <span>Disk Write</span>
          </div>
          <span className="font-semibold text-white">{formatRate(point.diskWriteRate)}</span>
        </div>

        <div className="flex items-center justify-between">
          <div className="flex items-center gap-1.5 text-geist-secondary">
            <span className="w-2 h-2 rounded-full bg-[#50e3c2]" />
            <div className="flex items-center gap-0.5">
              <span>Net RX</span>
              <ArrowDown className="w-3 h-3 text-[#50e3c2]" />
            </div>
          </div>
          <span className="font-semibold text-white">{formatRate(point.netRxRate)}</span>
        </div>

        <div className="flex items-center justify-between">
          <div className="flex items-center gap-1.5 text-geist-secondary">
            <span className="w-2 h-2 rounded-full bg-[#0070f3]" />
            <div className="flex items-center gap-0.5">
              <span>Net TX</span>
              <ArrowUp className="w-3 h-3 text-[#0070f3]" />
            </div>
          </div>
          <span className="font-semibold text-white">{formatRate(point.netTxRate)}</span>
        </div>
      </div>
    </div>
  );
}

export function ThroughputChart({
  data,
  height = 260,
  className,
  title = 'I/O & Network Throughput',
  description,
  showLegend = true,
  showCard = true,
}: ThroughputChartProps) {
  const reactId = useId().replace(/:/g, '');
  const diskGradientId = `throughput-disk-${reactId}`;
  const netGradientId = `throughput-net-${reactId}`;

  const chartData = useMemo<ProcessedThroughputPoint[]>(() => {
    if (!data || data.length === 0) return [];

    const hasElapsed = data.some((d) => (d.step_elapsed_sec ?? 0) > 0);
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
        diskWriteRate: d.disk_write_rate || 0,
        netRxRate: d.net_rx_rate || 0,
        netTxRate: d.net_tx_rate || 0,
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
            <HardDrive className="w-4 h-4 text-geist-warning" />
            <h3 className="text-xs font-semibold text-white tracking-tight">{title}</h3>
          </div>
          {description && (
            <p className="text-[11px] text-geist-faint mt-0.5">{description}</p>
          )}
        </div>

        {latestSample && (
          <div className="flex items-center gap-3 text-xs font-mono">
            <div className="flex items-center gap-1.5">
              <span className="w-2 h-2 rounded-full bg-[#f5a623]" />
              <span className="text-geist-secondary">Disk:</span>
              <span className="text-white font-medium">{formatRate(latestSample.diskWriteRate)}</span>
            </div>
            <div className="flex items-center gap-1.5">
              <span className="w-2 h-2 rounded-full bg-[#50e3c2]" />
              <span className="text-geist-secondary">RX:</span>
              <span className="text-white font-medium">{formatRate(latestSample.netRxRate)}</span>
            </div>
            <div className="flex items-center gap-1.5">
              <span className="w-2 h-2 rounded-full bg-[#0070f3]" />
              <span className="text-geist-secondary">TX:</span>
              <span className="text-white font-medium">{formatRate(latestSample.netTxRate)}</span>
            </div>
          </div>
        )}
      </div>

      {chartData.length === 0 ? (
        <div
          className="flex flex-col items-center justify-center text-center rounded-geist bg-geist-canvas border border-geist-hairlineSoft select-none"
          style={{ height }}
        >
          <HardDrive className="w-7 h-7 text-geist-faint mb-2 opacity-50" />
          <p className="text-xs font-medium text-geist-body">No throughput data recorded</p>
          <p className="text-[11px] text-geist-faint mt-0.5">
            Disk and network I/O rates will appear here during runner execution
          </p>
        </div>
      ) : (
        <div style={{ width: '100%', height }}>
          <ResponsiveContainer width="100%" height="100%" minWidth={0} minHeight={0}>
            <AreaChart data={chartData} margin={{ top: 10, right: 10, left: -15, bottom: 0 }}>
              <defs>
                <linearGradient id={diskGradientId} x1="0" y1="0" x2="0" y2="1">
                  <stop offset="5%" stopColor="#f5a623" stopOpacity={0.25} />
                  <stop offset="95%" stopColor="#f5a623" stopOpacity={0.0} />
                </linearGradient>
                <linearGradient id={netGradientId} x1="0" y1="0" x2="0" y2="1">
                  <stop offset="5%" stopColor="#50e3c2" stopOpacity={0.25} />
                  <stop offset="95%" stopColor="#50e3c2" stopOpacity={0.0} />
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

              {/* Left Y-Axis: Disk Write Rate */}
              <YAxis
                yAxisId="disk"
                orientation="left"
                domain={[0, (dataMax: number) => (dataMax > 0 ? Math.ceil(dataMax * 1.15) : 1024 * 1024)]}
                stroke="#666666"
                fontSize={11}
                fontFamily="Geist Mono, monospace"
                tickFormatter={(v: number) => formatRate(v, 0)}
                tickLine={false}
                axisLine={{ stroke: '#262626' }}
              />

              {/* Right Y-Axis: Network I/O Rate */}
              <YAxis
                yAxisId="net"
                orientation="right"
                domain={[0, (dataMax: number) => (dataMax > 0 ? Math.ceil(dataMax * 1.15) : 1024 * 1024)]}
                stroke="#666666"
                fontSize={11}
                fontFamily="Geist Mono, monospace"
                tickFormatter={(v: number) => formatRate(v, 0)}
                tickLine={false}
                axisLine={{ stroke: '#262626' }}
              />

              <Tooltip content={<ThroughputTooltip />} />

              {showLegend && (
                <Legend
                  verticalAlign="top"
                  align="right"
                  wrapperStyle={{ paddingBottom: '8px', fontSize: '11px', fontFamily: 'Geist Mono, monospace' }}
                  formatter={(value) => <span className="text-geist-secondary text-[11px]">{value}</span>}
                />
              )}

              {/* Disk Write Rate step area */}
              <Area
                yAxisId="disk"
                type="stepAfter"
                dataKey="diskWriteRate"
                name="Disk Write"
                stroke="#f5a623"
                strokeWidth={1.5}
                fillOpacity={1}
                fill={`url(#${diskGradientId})`}
                isAnimationActive={false}
              />

              {/* Net RX Rate step area */}
              <Area
                yAxisId="net"
                type="stepAfter"
                dataKey="netRxRate"
                name="Net RX"
                stroke="#50e3c2"
                strokeWidth={1.5}
                fillOpacity={1}
                fill={`url(#${netGradientId})`}
                isAnimationActive={false}
              />

              {/* Net TX Rate step line */}
              <Line
                yAxisId="net"
                type="stepAfter"
                dataKey="netTxRate"
                name="Net TX"
                stroke="#0070f3"
                strokeWidth={1.5}
                strokeDasharray="3 3"
                dot={false}
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

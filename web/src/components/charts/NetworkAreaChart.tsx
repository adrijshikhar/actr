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
import { Network } from 'lucide-react';
import type { TelemetrySample } from '@/lib/api';
import { cn, formatRate } from '@/lib/utils';
import { formatElapsed } from './VitalsAreaChart';

export interface NetworkAreaChartProps {
  data?: TelemetrySample[];
  height?: number;
  className?: string;
  title?: string;
  description?: string;
  showCard?: boolean;
}

interface ProcessedNetworkPoint {
  elapsedSec: number;
  elapsedLabel: string;
  timeFormatted: string;
  netRxRate: number;
  netTxRate: number;
  raw: TelemetrySample;
}

interface CustomTooltipProps {
  active?: boolean;
  payload?: Array<{
    value: number;
    payload: ProcessedNetworkPoint;
    dataKey: string;
    color: string;
  }>;
}

function NetworkTooltip({ active, payload }: CustomTooltipProps) {
  if (!active || !payload || payload.length === 0) return null;
  const point = payload[0]?.payload;
  if (!point) return null;

  const total = point.netRxRate + point.netTxRate;

  return (
    <div className="rounded-geist bg-geist-surface/95 backdrop-blur-md border border-geist-hairline p-3 shadow-2xl text-xs font-mono space-y-1.5 min-w-[210px]">
      <div className="flex items-center justify-between pb-1.5 border-b border-geist-hairline text-geist-faint text-[10px]">
        <span className="text-white font-semibold flex items-center gap-1.5">
          <span className="w-1.5 h-1.5 rounded-full bg-geist-blue" />
          Network Socket I/O
        </span>
        <span>{point.timeFormatted}</span>
      </div>

      <div className="flex items-center justify-between text-xs">
        <div className="flex items-center gap-1.5 text-geist-secondary">
          <span className="w-2 h-2 rounded-full bg-[#50e3c2]" />
          <span>Ingress (RX)</span>
        </div>
        <span className="font-semibold text-white">{formatRate(point.netRxRate)}</span>
      </div>

      <div className="flex items-center justify-between text-xs">
        <div className="flex items-center gap-1.5 text-geist-secondary">
          <span className="w-2 h-2 rounded-full bg-[#a78bfa]" />
          <span>Egress (TX)</span>
        </div>
        <span className="font-semibold text-white">{formatRate(point.netTxRate)}</span>
      </div>

      <div className="flex items-center justify-between text-[11px] text-geist-faint pt-1 border-t border-geist-hairline/60">
        <span>Aggregate Traffic</span>
        <span className="font-semibold text-white">{formatRate(total)}</span>
      </div>
    </div>
  );
}

export function NetworkAreaChart({
  data,
  height = 220,
  className,
  title = 'Network Bandwidth',
  description = 'Network interface socket throughput (RX / TX)',
  showCard = true,
}: NetworkAreaChartProps) {
  const reactId = useId().replace(/:/g, '');
  const rxGradientId = `net-rx-${reactId}`;
  const txGradientId = `net-tx-${reactId}`;
  const heightClass = height <= 200 ? 'h-48' : height <= 240 ? 'h-56' : 'h-64';

  const hasElapsed = useMemo(() => {
    return Boolean(data && data.some((d) => (d.step_elapsed_sec ?? 0) > 0));
  }, [data]);

  const chartData = useMemo<ProcessedNetworkPoint[]>(() => {
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
        netRxRate: d.net_rx_rate || 0,
        netTxRate: d.net_tx_rate || 0,
        raw: d,
      };
    });
  }, [data, hasElapsed]);

  const latestSample = chartData[chartData.length - 1];
  const totalLatestRate = latestSample ? latestSample.netRxRate + latestSample.netTxRate : 0;

  const content = (
    <>
      <div className="flex items-center justify-between mb-3">
        <div>
          <div className="flex items-center gap-2">
            <Network className="w-4 h-4 text-geist-blue" />
            <h3 className="text-xs font-semibold text-white tracking-tight">{title}</h3>
          </div>
          {description && (
            <p className="text-[11px] text-geist-faint mt-0.5">{description}</p>
          )}
        </div>

        {latestSample && (
          <div className="flex items-center gap-3 text-xs font-mono">
            <div className="flex items-center gap-1.5">
              <span className="w-2 h-2 rounded-full bg-[#50e3c2]" />
              <span className="text-geist-secondary">RX:</span>
              <span className="text-white font-medium">{formatRate(latestSample.netRxRate)}</span>
            </div>
            <div className="flex items-center gap-1.5">
              <span className="w-2 h-2 rounded-full bg-[#a78bfa]" />
              <span className="text-geist-secondary">TX:</span>
              <span className="text-white font-medium">{formatRate(latestSample.netTxRate)}</span>
            </div>
            <span className="text-geist-faint text-[11px]">
              ({formatRate(totalLatestRate)})
            </span>
          </div>
        )}
      </div>

      {chartData.length === 0 ? (
        <div className={cn('w-full flex flex-col items-center justify-center text-center rounded-geist bg-geist-canvas border border-geist-hairlineSoft select-none', heightClass)}>
          <Network className="w-6 h-6 text-geist-faint mb-2 opacity-50" />
          <p className="text-xs font-medium text-geist-body">No network traffic recorded</p>
          <p className="text-[11px] text-geist-faint mt-0.5">Metrics stream every second via SSE</p>
        </div>
      ) : (
        <div className={cn('w-full', heightClass)}>
          <ResponsiveContainer width="100%" height="100%" minWidth={0} minHeight={0}>
            <AreaChart data={chartData} margin={{ top: 8, right: 12, left: 0, bottom: 0 }}>
              <defs>
                <linearGradient id={rxGradientId} x1="0" y1="0" x2="0" y2="1">
                  <stop offset="5%" stopColor="#50e3c2" stopOpacity={0.3} />
                  <stop offset="95%" stopColor="#50e3c2" stopOpacity={0.0} />
                </linearGradient>
                <linearGradient id={txGradientId} x1="0" y1="0" x2="0" y2="1">
                  <stop offset="5%" stopColor="#a78bfa" stopOpacity={0.25} />
                  <stop offset="95%" stopColor="#a78bfa" stopOpacity={0.0} />
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

              <Tooltip content={<NetworkTooltip />} />

              <Legend
                verticalAlign="top"
                align="right"
                className="pb-2 text-[11px] font-mono"
                formatter={(value) => <span className="text-geist-secondary text-[11px]">{value}</span>}
              />

              <Area
                type="monotone"
                dataKey="netRxRate"
                name="RX Ingress"
                stroke="#50e3c2"
                strokeWidth={1.5}
                fillOpacity={1}
                fill={`url(#${rxGradientId})`}
                isAnimationActive={false}
              />

              <Area
                type="monotone"
                dataKey="netTxRate"
                name="TX Egress"
                stroke="#a78bfa"
                strokeWidth={1.5}
                fillOpacity={1}
                fill={`url(#${txGradientId})`}
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

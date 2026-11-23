import { Cpu } from 'lucide-react';
import { cn } from '@/lib/utils';
import { Badge } from '@/components/ui/badge';

export interface SlotGaugeProps {
  active?: number;
  activeSlots?: number;
  max?: number;
  maxConcurrency?: number;
  label?: string;
  sublabel?: string;
  size?: number;
  strokeWidth?: number;
  className?: string;
  showCard?: boolean;
}

export function SlotGauge({
  active,
  activeSlots,
  max,
  maxConcurrency,
  label = 'Concurrency Capacity',
  sublabel,
  size = 140,
  strokeWidth = 10,
  className,
  showCard = true,
}: SlotGaugeProps) {
  const currentActive = active ?? activeSlots ?? 0;
  const configuredMax = max ?? maxConcurrency ?? 1;
  const safeMax = Math.max(configuredMax, 1);
  const rawPct = (currentActive / safeMax) * 100;
  const percentage = Number.isFinite(rawPct)
    ? Math.min(Math.max(Math.round(rawPct), 0), 100)
    : 0;

  // Status tiers based on capacity threshold
  let statusColor = '#30a46c'; // Geist mint/green (< 75%)
  let statusText = 'Available';
  let badgeVariant: 'success' | 'warning' | 'destructive' = 'success';

  if (percentage >= 100) {
    statusColor = '#e5484d'; // Geist red (100%)
    statusText = 'At Capacity';
    badgeVariant = 'destructive';
  } else if (percentage >= 75) {
    statusColor = '#f5a623'; // Geist amber (75-99%)
    statusText = 'Near Limit';
    badgeVariant = 'warning';
  }

  // SVG Radial Ring Geometry
  const center = size / 2;
  const radius = (size - strokeWidth) / 2;
  const circumference = 2 * Math.PI * radius;
  const strokeDashoffset = circumference - (percentage / 100) * circumference;

  const content = (
    <div className="flex flex-col items-center justify-center text-center">
      {/* Header Label */}
      {label && (
        <div className="flex items-center gap-1.5 text-xs font-semibold text-white mb-3">
          <Cpu className="w-3.5 h-3.5 text-geist-secondary" />
          <span>{label}</span>
        </div>
      )}

      {/* Radial Ring */}
      <div className="relative flex items-center justify-center" style={{ width: size, height: size }}>
        <svg
          width={size}
          height={size}
          viewBox={`0 0 ${size} ${size}`}
          className="transform -rotate-90"
        >
          {/* Subtle Background Track */}
          <circle
            cx={center}
            cy={center}
            r={radius}
            stroke="#262626"
            strokeWidth={strokeWidth}
            fill="transparent"
          />

          {/* Glowing Animated Progress Stroke */}
          <circle
            cx={center}
            cy={center}
            r={radius}
            stroke={statusColor}
            strokeWidth={strokeWidth}
            fill="transparent"
            strokeDasharray={circumference}
            strokeDashoffset={strokeDashoffset}
            strokeLinecap="round"
            style={{
              transition: 'stroke-dashoffset 0.6s cubic-bezier(0.16, 1, 0.3, 1), stroke 0.3s ease',
            }}
          />
        </svg>

        {/* Center Numbers */}
        <div className="absolute inset-0 flex flex-col items-center justify-center pointer-events-none select-none">
          <div className="flex items-baseline gap-0.5">
            <span className="text-2xl font-bold font-mono tracking-tight text-white">
              {currentActive}
            </span>
            <span className="text-sm font-mono text-geist-faint">/</span>
            <span className="text-sm font-mono text-geist-secondary">
              {configuredMax}
            </span>
          </div>
          <span className="text-[10px] uppercase font-mono tracking-wider text-geist-faint">
            slots
          </span>
          <span
            className="text-[11px] font-mono font-medium mt-0.5 transition-colors duration-300"
            style={{ color: statusColor }}
          >
            {percentage}%
          </span>
        </div>
      </div>

      {/* Status Pill & Sublabel */}
      <div className="mt-3 flex flex-col items-center gap-1">
        <Badge variant={badgeVariant} className="text-[11px] px-2 py-0.5 gap-1.5 font-sans">
          <span
            className={cn(
              'w-1.5 h-1.5 rounded-full',
              percentage >= 100 && 'animate-ping'
            )}
            style={{ backgroundColor: statusColor }}
          />
          {statusText}
        </Badge>

        <p className="text-[11px] text-geist-faint font-mono mt-0.5">
          {sublabel ||
            (currentActive === 0
              ? 'All slots available'
              : `${currentActive} active lease${currentActive === 1 ? '' : 's'}`)}
        </p>
      </div>
    </div>
  );

  if (!showCard) {
    return <div className={className}>{content}</div>;
  }

  return (
    <div
      className={cn(
        'rounded-geist bg-geist-surface border border-geist-hairline p-4 flex flex-col items-center justify-center',
        className
      )}
    >
      {content}
    </div>
  );
}

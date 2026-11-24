import { useId } from 'react';
import { cn } from '@/lib/utils';

export interface ActrLogoProps {
  /** Size in pixels (default: 28) */
  size?: number;
  /** Whether to render inside the dark squircle background (default: true) */
  showSquircle?: boolean;
  /** Additional CSS class names */
  className?: string;
  /** Whether to render subtle electric cyan/blue neon glow on the action chevron */
  showGlow?: boolean;
}

/**
 * ActrLogo - Official Brand Identity Glyph for actr.
 *
 * Geometric "a⟩" emblem fusing the actr monogram 'a' with a forward-pointing
 * action runner chevron '⟩', embodying kinetic velocity and pipeline dispatch.
 */
export function ActrLogo({
  size = 28,
  showSquircle = true,
  className,
  showGlow = false,
}: ActrLogoProps) {
  const reactId = useId().replace(/:/g, '');
  const glowId = `actr-glow-${reactId}`;
  const gradId = `actr-grad-${reactId}`;

  return (
    <svg
      width={size}
      height={size}
      viewBox="0 0 32 32"
      fill="none"
      xmlns="http://www.w3.org/2000/svg"
      role="img"
      aria-label="actr logo"
      className={cn('flex-shrink-0 select-none', className)}
    >
      <defs>
        <linearGradient id={gradId} x1="19.5" y1="10.5" x2="25" y2="21.5" gradientUnits="userSpaceOnUse">
          <stop offset="0%" stopColor="#38bdf8" />
          <stop offset="100%" stopColor="#0070f3" />
        </linearGradient>

        {showGlow && (
          <filter id={glowId} x="-20%" y="-20%" width="140%" height="140%">
            <feDropShadow dx="0" dy="0" stdDeviation="1.5" floodColor="#0070f3" floodOpacity="0.6" />
          </filter>
        )}
      </defs>

      {showSquircle && (
        <rect
          x="0.75"
          y="0.75"
          width="30.5"
          height="30.5"
          rx="7"
          fill="#141414"
          stroke="#2a2a2a"
          strokeWidth="1.2"
        />
      )}

      {/* Geometric 'a' bowl & stem */}
      <circle
        cx="12.5"
        cy="16.5"
        r="4.5"
        stroke="#FFFFFF"
        strokeWidth="2.2"
      />
      <path
        d="M17 12V21"
        stroke="#FFFFFF"
        strokeWidth="2.2"
        strokeLinecap="round"
      />

      {/* Action Runner Chevron (⟩) */}
      <path
        d="M19.5 10.5L25 16L19.5 21.5"
        stroke={`url(#${gradId})`}
        strokeWidth="2.6"
        strokeLinecap="round"
        strokeLinejoin="round"
        filter={showGlow ? `url(#${glowId})` : undefined}
      />
    </svg>
  );
}

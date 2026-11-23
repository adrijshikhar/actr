import * as React from 'react';
import { cva, type VariantProps } from 'class-variance-authority';
import { cn } from '@/lib/utils';

const badgeVariants = cva(
  'inline-flex items-center rounded-full border px-2.5 py-0.5 text-[11px] font-medium transition-colors focus:outline-none focus-visible:ring-1 focus-visible:ring-[#0070f3]',
  {
    variants: {
      variant: {
        default:
          'border-transparent bg-primary text-primary-foreground',
        secondary:
          'border-border bg-secondary text-secondary-foreground',
        destructive:
          'border-status-danger-border bg-status-danger-bg text-status-danger-text',
        outline: 'border-border text-foreground',
        success:
          'border-status-success-border bg-status-success-bg text-status-success-text',
        warning:
          'border-status-warning-border bg-status-warning-bg text-status-warning-text',
        info:
          'border-status-info-border bg-status-info-bg text-status-info-text',
      },
    },
    defaultVariants: {
      variant: 'default',
    },
  }
);

export interface BadgeProps
  extends React.HTMLAttributes<HTMLDivElement>,
    VariantProps<typeof badgeVariants> {}

function Badge({ className, variant, ...props }: BadgeProps) {
  return (
    <div className={cn(badgeVariants({ variant }), className)} {...props} />
  );
}

export { Badge, badgeVariants };

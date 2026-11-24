import {
  Server,
  RotateCcw,
  Tag,
  GitBranch,
  Cpu,
  CheckCircle2,
  Clock,
} from 'lucide-react';
import type { RunnerInfo } from '@/lib/api';
import { SlotGauge } from '@/components/charts/SlotGauge';
import { Badge } from '@/components/ui/badge';
import { Button } from '@/components/ui/button';
import { cn } from '@/lib/utils';

export interface RunnersViewProps {
  runners: RunnerInfo[];
  onRefresh: () => void;
  isLoading?: boolean;
}

export function RunnersView({
  runners = [],
  onRefresh,
  isLoading = false,
}: RunnersViewProps) {
  const totalSlots = runners.reduce((acc, r) => acc + (r.max_concurrency || 0), 0);
  const activeSlots = runners.reduce((acc, r) => acc + (r.active_slots || 0), 0);
  const totalQueued = runners.reduce((acc, r) => acc + (r.queued_jobs || 0), 0);

  return (
    <div className="space-y-6">
      {/* Top Header Card */}
      <div className="rounded-geist bg-geist-surface border border-geist-hairline p-5 flex flex-col sm:flex-row items-start sm:items-center justify-between gap-4">
        <div>
          <div className="flex items-center gap-2.5">
            <Server className="w-5 h-5 text-geist-blue" />
            <h2 className="text-base font-semibold text-white tracking-tight">
              Registered Runners
            </h2>
            <Badge variant="outline" className="font-mono text-xs">
              {runners.length}
            </Badge>
          </div>
          <p className="text-xs text-geist-secondary mt-1">
            Active multi-tenant runners registered to execute CI/CD workflow steps across repositories.
          </p>
        </div>

        <div className="flex items-center gap-3">
          <div className="text-right hidden sm:block font-mono text-xs space-y-0.5">
            <div>
              <span className="text-geist-faint">Slots: </span>
              <span className="text-white font-semibold">{activeSlots}</span>
              <span className="text-geist-faint"> / {totalSlots}</span>
            </div>
            {totalQueued > 0 && (
              <div className="text-[11px] text-status-warning-text flex items-center justify-end gap-1">
                <Clock className="w-3 h-3" />
                <span>{totalQueued} waiting in queue</span>
              </div>
            )}
          </div>

          <Button
            variant="outline"
            size="sm"
            onClick={onRefresh}
            disabled={isLoading}
            className="h-8 gap-1.5 text-xs text-geist-secondary hover:text-white"
          >
            <RotateCcw className={cn('w-3.5 h-3.5', isLoading && 'animate-spin')} />
            <span>Refresh</span>
          </Button>
        </div>
      </div>

      {/* Runners Grid */}
      {runners.length === 0 ? (
        <div className="rounded-geist bg-geist-surface border border-geist-hairline p-12 text-center text-geist-secondary text-xs space-y-3">
          <Server className="w-10 h-10 mx-auto text-geist-hairline" />
          <div className="space-y-1">
            <p className="text-sm font-semibold text-white">No Runners Configured</p>
            <p className="text-geist-faint max-w-md mx-auto">
              Runners allow <code className="inline-code">actr</code> to distribute and execute steps. Configure runners in your configuration file or run the daemon with worker processes.
            </p>
          </div>
          <Button variant="outline" size="sm" onClick={onRefresh} className="h-8 text-xs mt-2">
            Check Again
          </Button>
        </div>
      ) : (
        <div className="grid grid-cols-1 md:grid-cols-2 lg:grid-cols-3 gap-5">
          {runners.map((r, idx) => {
            const isBusy = r.status === 'busy' || (r.active_slots || 0) > 0;
            const availableSlots = Math.max((r.max_concurrency || 1) - (r.active_slots || 0), 0);

            return (
              <div
                key={`${r.name}-${r.repo}-${idx}`}
                className="rounded-geist bg-geist-surface border border-geist-hairline p-5 flex flex-col justify-between hover:border-geist-borderHover transition-all duration-150 space-y-4"
              >
                {/* Header */}
                <div>
                  <div className="flex items-start justify-between gap-2">
                    <div className="space-y-1 min-w-0">
                      <div className="flex items-center gap-2">
                        <span className="font-mono font-bold text-sm text-white truncate">
                          {r.name}
                        </span>
                      </div>
                      <div className="flex items-center gap-1.5 text-xs text-geist-secondary truncate">
                        <GitBranch className="w-3.5 h-3.5 text-geist-faint flex-shrink-0" />
                        <span className="truncate font-mono text-[11px]">{r.repo}</span>
                      </div>
                    </div>

                    <div className="flex items-center gap-1.5 flex-shrink-0">
                      {r.queued_jobs !== undefined && r.queued_jobs > 0 && (
                        <Badge
                          variant="outline"
                          className="text-[10px] font-mono px-1.5 py-0.5 text-status-warning-text border-status-warning-border/60 bg-status-warning-bg/40 gap-1"
                        >
                          <Clock className="w-2.5 h-2.5" />
                          <span>{r.queued_jobs} queued</span>
                        </Badge>
                      )}
                      <Badge
                        variant={isBusy ? 'warning' : 'success'}
                        className="text-[10px] font-mono uppercase px-2 py-0.5 flex-shrink-0 gap-1"
                      >
                        <span
                          className={cn(
                            'w-1.5 h-1.5 rounded-full',
                            isBusy ? 'bg-status-warning-solid animate-pulse' : 'bg-status-success-solid'
                          )}
                        />
                        {r.status || (isBusy ? 'busy' : 'idle')}
                      </Badge>
                    </div>
                  </div>
                </div>

                {/* Radial Concurrency SlotGauge */}
                <div className="py-2 flex justify-center">
                  <SlotGauge
                    activeSlots={r.active_slots || 0}
                    maxConcurrency={r.max_concurrency || 1}
                    size={110}
                    strokeWidth={8}
                    showCard={false}
                    label=""
                    sublabel={`${r.active_slots || 0} of ${r.max_concurrency || 1} slots`}
                  />
                </div>

                {/* Labels / Tags */}
                <div className="space-y-1.5">
                  <div className="text-[10px] font-mono uppercase tracking-wider text-geist-faint flex items-center gap-1">
                    <Tag className="w-3 h-3" />
                    <span>Labels</span>
                  </div>
                  <div className="flex flex-wrap gap-1.5">
                    {r.labels && r.labels.length > 0 ? (
                      r.labels.map((lbl, lIdx) => (
                        <span
                          key={lIdx}
                          className="text-[10px] font-mono px-1.5 py-0.5 rounded bg-geist-elevated text-geist-secondary border border-geist-hairline"
                        >
                          {lbl}
                        </span>
                      ))
                    ) : (
                      <span className="text-[10px] text-geist-faint font-mono italic">
                        default, standard
                      </span>
                    )}
                  </div>
                </div>

                {/* Footer Slot Stats */}
                <div className="pt-3 border-t border-geist-hairline/60 flex items-center justify-between text-[11px] font-mono text-geist-secondary">
                  <div className="flex items-center gap-1">
                    <Cpu className="w-3 h-3 text-geist-faint" />
                    <span>Max: {r.max_concurrency}</span>
                  </div>
                  {r.queued_jobs !== undefined && r.queued_jobs > 0 ? (
                    <div className="flex items-center gap-1 text-status-warning-text font-medium">
                      <Clock className="w-3 h-3" />
                      <span>{r.queued_jobs} queued</span>
                    </div>
                  ) : (
                    <div className="flex items-center gap-1">
                      <CheckCircle2 className="w-3 h-3 text-status-success-text" />
                      <span>{availableSlots} available</span>
                    </div>
                  )}
                </div>
              </div>
            );
          })}
        </div>
      )}
    </div>
  );
}

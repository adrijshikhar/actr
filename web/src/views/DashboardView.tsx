import {
  Power,
  Server,
  Cpu,
  HardDrive,
  History,
  Activity,
  CheckCircle2,
  XCircle,
  Clock,
  ChevronRight,
  Boxes,
  RotateCcw,
} from 'lucide-react';
import type { StatusResponse, RunnerInfo, RunRecord } from '@/lib/api';
import { SlotGauge } from '@/components/charts/SlotGauge';
import { Badge } from '@/components/ui/badge';
import { Button } from '@/components/ui/button';
import { cn, formatBytes, formatDuration, formatRelativeTime, formatAppVersion, getProgressWidthClass } from '@/lib/utils';

export interface DashboardViewProps {
  status: StatusResponse | null;
  runs: RunRecord[];
  runners: RunnerInfo[];
  onSelectRun: (id: string) => void;
  onNavigateTab?: (tab: string) => void;
  onToggleService?: () => void;
  serviceActionLoading?: boolean;
  onRefresh?: () => void;
}

export function DashboardView({
  status,
  runs = [],
  runners = [],
  onSelectRun,
  onNavigateTab,
  onToggleService: _onToggleService,
  serviceActionLoading: _serviceActionLoading = false,
  onRefresh,
}: DashboardViewProps) {
  // Aggregate runner capacity
  const totalActiveSlots = runners.reduce((acc, r) => acc + (r.active_slots || 0), 0);
  const totalMaxSlots =
    runners.reduce((acc, r) => acc + (r.max_concurrency || 0), 0) ||
    Math.max(status?.total_runners || 1, 1);

  const memUsed = status?.host_mem_used || 0;
  const memTotal = status?.host_mem_total || 1;
  const memPercent = Math.min(Math.round((memUsed / memTotal) * 100), 100);

  const activeRuns = runs.filter((r) => r.status === 'running' || r.status === 'in_progress');
  const recentRuns = runs.slice(0, 10);

  return (
    <div className="space-y-6">
      {/* Top Vitals HUD Grid */}
      <div className="grid grid-cols-1 md:grid-cols-2 lg:grid-cols-4 gap-4">
        {/* Radial Capacity Gauge Card */}
        <div className="rounded-geist bg-geist-surface border border-geist-hairline p-4 flex flex-col items-center justify-between min-h-[220px]">
          <div className="w-full flex items-center justify-between text-xs text-geist-secondary pb-2 border-b border-geist-hairline/60">
            <span className="font-semibold text-white">Cluster Capacity</span>
            <span className="font-mono text-[11px] text-geist-faint">
              {runners.length} runner{runners.length === 1 ? '' : 's'}
            </span>
          </div>

          <div className="my-2">
            <SlotGauge
              activeSlots={totalActiveSlots}
              maxConcurrency={totalMaxSlots}
              label=""
              size={120}
              strokeWidth={9}
              showCard={false}
              sublabel={`${totalActiveSlots} of ${totalMaxSlots} slots occupied`}
            />
          </div>

          <div className="w-full flex items-center justify-between text-[11px] font-mono text-geist-faint pt-2 border-t border-geist-hairline/60">
            <span>Available: {Math.max(totalMaxSlots - totalActiveSlots, 0)}</span>
            <span>Utilization: {Math.round((totalActiveSlots / totalMaxSlots) * 100)}%</span>
          </div>
        </div>

        {/* Daemon State & Quick Control Card */}
        <div className="rounded-geist bg-geist-surface border border-geist-hairline p-4 flex flex-col justify-between min-h-[220px]">
          <div>
            <div className="flex items-center justify-between text-xs text-geist-secondary pb-2 border-b border-geist-hairline/60">
              <span className="font-semibold text-white">Daemon Service</span>
              <Power className="w-3.5 h-3.5 text-geist-faint" />
            </div>

            <div className="mt-4 space-y-2">
              <div className="flex items-center gap-2">
                <span
                  className={cn(
                    'w-2.5 h-2.5 rounded-full',
                    status?.daemon_connected
                      ? 'bg-status-success-solid animate-pulse'
                      : 'bg-status-danger-solid'
                  )}
                />
                <span className="text-lg font-bold text-white tracking-tight">
                  {status?.daemon_connected ? 'Daemon Running' : 'Daemon Offline'}
                </span>
              </div>
              <p className="text-xs text-geist-secondary">
                actr orchestrator {formatAppVersion(status?.version).display}
              </p>
              <p className="text-[11px] text-geist-faint font-mono">
                Uptime: {formatDuration(status?.uptime_seconds || 0)}
              </p>
            </div>
          </div>

          <div className="pt-3 border-t border-geist-hairline/60 flex items-center justify-between text-xs font-mono text-geist-faint">
            <span>Daemon Control</span>
            <span className="text-geist-secondary">Sidebar Action</span>
          </div>
        </div>

        {/* Live Host CPU Card */}
        <div className="rounded-geist bg-geist-surface border border-geist-hairline p-4 flex flex-col justify-between min-h-[220px]">
          <div>
            <div className="flex items-center justify-between text-xs text-geist-secondary pb-2 border-b border-geist-hairline/60">
              <span className="font-semibold text-white">Host Node CPU</span>
              <Cpu className="w-3.5 h-3.5 text-geist-faint" />
            </div>

            <div className="mt-4 space-y-2">
              <div className="text-3xl font-bold font-mono tracking-tight text-white">
                {status?.host_cpu?.toFixed(1) ?? '0.0'}%
              </div>
              <p className="text-xs text-geist-secondary">Live aggregate utilization</p>
            </div>
          </div>

          <div className="space-y-2 pt-3 border-t border-geist-hairline/60">
            <div className="w-full bg-geist-elevated h-2 rounded-full overflow-hidden border border-geist-hairline">
              <div
                className={cn(
                  'bg-geist-blue h-full transition-all duration-500 rounded-full',
                  getProgressWidthClass(status?.host_cpu || 0)
                )}
              />
            </div>
            <div className="flex items-center justify-between text-[11px] font-mono text-geist-faint">
              <span>Idle: {Math.max(100 - (status?.host_cpu || 0), 0).toFixed(1)}%</span>
              {onNavigateTab ? (
                <button
                  type="button"
                  onClick={() => onNavigateTab('vitals')}
                  className="text-geist-blue hover:text-white transition-colors duration-150 cursor-pointer flex items-center gap-1"
                >
                  Live Graph →
                </button>
              ) : (
                <span>Active: {status?.host_cpu?.toFixed(1) ?? '0.0'}%</span>
              )}
            </div>
          </div>
        </div>

        {/* Live Host Memory Card */}
        <div className="rounded-geist bg-geist-surface border border-geist-hairline p-4 flex flex-col justify-between min-h-[220px]">
          <div>
            <div className="flex items-center justify-between text-xs text-geist-secondary pb-2 border-b border-geist-hairline/60">
              <span className="font-semibold text-white">Host Memory</span>
              <HardDrive className="w-3.5 h-3.5 text-geist-faint" />
            </div>

            <div className="mt-4 space-y-2">
              <div className="text-2xl font-bold font-mono tracking-tight text-white">
                {formatBytes(memUsed)}
              </div>
              <p className="text-xs text-geist-secondary">
                of {formatBytes(memTotal)} allocated
              </p>
            </div>
          </div>

          <div className="space-y-2 pt-3 border-t border-geist-hairline/60">
            <div className="w-full bg-geist-elevated h-2 rounded-full overflow-hidden border border-geist-hairline">
              <div
                className={cn(
                  'bg-status-success-solid h-full transition-all duration-500 rounded-full',
                  getProgressWidthClass(memPercent)
                )}
              />
            </div>
            <div className="flex items-center justify-between text-[11px] font-mono text-geist-faint">
              <span>{memPercent}% used</span>
              {onNavigateTab ? (
                <button
                  type="button"
                  onClick={() => onNavigateTab('vitals')}
                  className="text-geist-blue hover:text-white transition-colors duration-150 cursor-pointer flex items-center gap-1"
                >
                  Live Graph →
                </button>
              ) : (
                <span>{formatBytes(Math.max(memTotal - memUsed, 0))} free</span>
              )}
            </div>
          </div>
        </div>
      </div>

      {/* Summary KPI Strip */}
      <div className="grid grid-cols-2 sm:grid-cols-4 gap-3">
        <div className="p-3 rounded-geist bg-geist-surface/70 border border-geist-hairline flex items-center justify-between">
          <div className="space-y-0.5">
            <div className="text-[11px] font-medium text-geist-faint uppercase tracking-wider">
              Active Jobs
            </div>
            <div className="text-xl font-bold font-mono text-white">
              {Math.max(status?.active_jobs ?? 0, activeRuns.length)}
            </div>
          </div>
          <Activity className="w-5 h-5 text-status-warning-text opacity-70" />
        </div>

        <div className="p-3 rounded-geist bg-geist-surface/70 border border-geist-hairline flex items-center justify-between">
          <div className="space-y-0.5">
            <div className="text-[11px] font-medium text-geist-faint uppercase tracking-wider">
              Total Runners
            </div>
            <div className="text-xl font-bold font-mono text-white">
              {runners.length || status?.total_runners || 0}
            </div>
          </div>
          <Server className="w-5 h-5 text-geist-blue opacity-70" />
        </div>

        <div className="p-3 rounded-geist bg-geist-surface/70 border border-geist-hairline flex items-center justify-between">
          <div className="space-y-0.5">
            <div className="text-[11px] font-medium text-geist-faint uppercase tracking-wider">
              Total Runs
            </div>
            <div className="text-xl font-bold font-mono text-white">{runs.length}</div>
          </div>
          <History className="w-5 h-5 text-geist-secondary opacity-70" />
        </div>

        <div className="p-3 rounded-geist bg-geist-surface/70 border border-geist-hairline flex items-center justify-between">
          <div className="space-y-0.5">
            <div className="text-[11px] font-medium text-geist-faint uppercase tracking-wider">
              Available Slots
            </div>
            <div className="text-xl font-bold font-mono text-status-success-text">
              {Math.max(totalMaxSlots - totalActiveSlots, 0)}
            </div>
          </div>
          <Boxes className="w-5 h-5 text-status-success-text opacity-70" />
        </div>
      </div>

      {/* Active & Recent Workflow Runs Table */}
      <div className="rounded-geist bg-geist-surface border border-geist-hairline overflow-hidden">
        <div className="px-4 py-3 border-b border-geist-hairline flex items-center justify-between">
          <div className="flex items-center gap-2">
            <History className="w-4 h-4 text-geist-secondary" />
            <h2 className="text-xs font-semibold text-white">Active & Recent Workflow Runs</h2>
          </div>
          {onRefresh && (
            <Button
              variant="ghost"
              size="sm"
              onClick={onRefresh}
              className="h-7 text-xs text-geist-secondary hover:text-white gap-1"
            >
              <RotateCcw className="w-3 h-3" />
              <span>Refresh</span>
            </Button>
          )}
        </div>

        {recentRuns.length === 0 ? (
          <div className="p-10 text-center text-geist-secondary text-xs space-y-2">
            <Boxes className="w-8 h-8 mx-auto text-geist-hairline" />
            <p className="text-geist-body font-medium">No workflow runs recorded.</p>
            <p className="text-geist-faint">
              Trigger a workflow with <code className="inline-code">actr run</code> to stream execution.
            </p>
          </div>
        ) : (
          <div className="overflow-x-auto">
            <table className="w-full text-left border-collapse text-xs">
              <thead>
                <tr className="border-b border-geist-hairline bg-geist-elevated/40 text-[11px] text-geist-faint font-mono uppercase tracking-wider">
                  <th className="py-2.5 px-4 font-medium">Status</th>
                  <th className="py-2.5 px-4 font-medium">Run ID</th>
                  <th className="py-2.5 px-4 font-medium">Workflow / Target</th>
                  <th className="py-2.5 px-4 font-medium">Repository</th>
                  <th className="py-2.5 px-4 font-medium">Driver</th>
                  <th className="py-2.5 px-4 font-medium">Duration</th>
                  <th className="py-2.5 px-4 font-medium">Started</th>
                  <th className="py-2.5 px-4 font-medium text-right">Action</th>
                </tr>
              </thead>
              <tbody className="divide-y divide-geist-hairline font-mono">
                {recentRuns.map((r) => {
                  const isCompleted = r.status === 'completed' || r.status === 'success';
                  const isFailed = r.status === 'failed';
                  const isRunning = r.status === 'running' || r.status === 'in_progress';

                  return (
                    <tr
                      key={r.id}
                      onClick={() => onSelectRun(r.id)}
                      className="hover:bg-geist-hover transition-colors duration-100 cursor-pointer group"
                    >
                      <td className="py-3 px-4 whitespace-nowrap">
                        <div className="flex items-center gap-1.5">
                          {isCompleted ? (
                            <CheckCircle2 className="w-3.5 h-3.5 text-status-success-solid flex-shrink-0" />
                          ) : isFailed ? (
                            <XCircle className="w-3.5 h-3.5 text-status-danger-solid flex-shrink-0" />
                          ) : isRunning ? (
                            <Activity className="w-3.5 h-3.5 text-status-warning-solid animate-pulse flex-shrink-0" />
                          ) : (
                            <Clock className="w-3.5 h-3.5 text-geist-faint flex-shrink-0" />
                          )}
                          <Badge
                            variant={
                              isCompleted
                                ? 'success'
                                : isFailed
                                ? 'destructive'
                                : isRunning
                                ? 'warning'
                                : 'outline'
                            }
                            className="text-[10px] uppercase font-mono px-1.5 py-0"
                          >
                            {r.status}
                          </Badge>
                        </div>
                      </td>

                      <td className="py-3 px-4 whitespace-nowrap font-semibold text-white">
                        {r.id.slice(0, 8)}
                      </td>

                      <td className="py-3 px-4 whitespace-nowrap font-sans font-medium text-white max-w-[200px] truncate">
                        {r.workflow || r.name || 'default'}
                      </td>

                      <td className="py-3 px-4 whitespace-nowrap text-geist-secondary max-w-[160px] truncate">
                        {r.repo_alias || r.repo || '-'}
                      </td>

                      <td className="py-3 px-4 whitespace-nowrap text-geist-faint text-[11px]">
                        {r.driver || 'docker'}
                      </td>

                      <td className="py-3 px-4 whitespace-nowrap text-geist-secondary">
                        {formatDuration(r.duration_sec)}
                      </td>

                      <td className="py-3 px-4 whitespace-nowrap text-geist-faint text-[11px]">
                        {formatRelativeTime(r.started_at)}
                      </td>

                      <td className="py-3 px-4 whitespace-nowrap text-right">
                        <span className="inline-flex items-center gap-1 text-[11px] text-geist-blue group-hover:text-white transition-colors">
                          <span>View</span>
                          <ChevronRight className="w-3 h-3 group-hover:translate-x-0.5 transition-transform" />
                        </span>
                      </td>
                    </tr>
                  );
                })}
              </tbody>
            </table>
          </div>
        )}
      </div>
    </div>
  );
}

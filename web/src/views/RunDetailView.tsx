import { useEffect, useState, useCallback, useRef, useMemo } from 'react';
import {
  ArrowLeft,
  Terminal,
  Activity,
  CheckCircle2,
  XCircle,
  Clock,
  RotateCcw,
  Layers,
  AlertTriangle,
} from 'lucide-react';
import {
  fetchRun,
  fetchLogs,
  fetchRunMetrics,
  type RunRecord,
  type StepMetadata,
  type TelemetrySample,
} from '@/lib/api';
import { subscribeLogs, type SSELogEvent } from '@/lib/sse';
import { LogViewer } from '@/components/LogViewer';
import { VitalsAreaChart, ThroughputChart } from '@/components/charts';
import { Button } from '@/components/ui/button';
import { Badge } from '@/components/ui/badge';
import { Tabs, TabsList, TabsTrigger, TabsContent } from '@/components/ui/tabs';
import { cn, formatBytes, formatDuration, formatRelativeTime } from '@/lib/utils';

export interface RunDetailViewProps {
  runId: string | null;
  onBack: () => void;
}

interface StoredLogLine {
  line: string;
  stepId?: string;
}

export function RunDetailView({ runId, onBack }: RunDetailViewProps) {
  const [run, setRun] = useState<RunRecord | null>(null);
  const [steps, setSteps] = useState<StepMetadata[]>([]);
  const [selectedStepId, setSelectedStepId] = useState<string | null>(null);
  const [logLines, setLogLines] = useState<StoredLogLine[]>([]);
  const [stepLogs, setStepLogs] = useState<string[] | null>(null);
  const [metrics, setMetrics] = useState<TelemetrySample[]>([]);
  const [loading, setLoading] = useState(true);
  const [activeTab, setActiveTab] = useState<'logs' | 'telemetry'>('logs');

  const unsubscribeRef = useRef<(() => void) | null>(null);
  const stepLogsCacheRef = useRef<Record<string, string[]>>({});

  // Load run details, initial steps, and telemetry metrics (isolated from step selection)
  const loadRunData = useCallback(async () => {
    if (!runId) return;
    try {
      setLoading(true);
      const runData = await fetchRun(runId);
      setRun(runData);
      if (runData.steps && runData.steps.length > 0) {
        setSteps(runData.steps);
      }

      // Load metrics
      try {
        const m = await fetchRunMetrics(runId);
        setMetrics(m || []);
      } catch (e) {
        console.warn('Failed to fetch run metrics:', e);
      }

      // If run already completed, fetch historical full logs
      const isFinished =
        runData.status === 'completed' ||
        runData.status === 'success' ||
        runData.status === 'failed';
      if (isFinished) {
        try {
          const res = await fetchLogs(runId);
          const lines = (res.logs || []).map((l) => ({
            line: l,
          }));
          setLogLines(lines);
        } catch (e) {
          console.warn('Failed to fetch historical logs:', e);
        }
      }
    } catch (err) {
      console.error('Failed to load run:', err);
    } finally {
      setLoading(false);
    }
  }, [runId]);

  useEffect(() => {
    loadRunData();
  }, [loadRunData]);

  // Isolate step log loading: does not touch loading = true or reload run/metrics
  useEffect(() => {
    let active = true;

    if (!runId || !selectedStepId) {
      setStepLogs(null);
      return;
    }

    const isFinished =
      run?.status === 'completed' ||
      run?.status === 'success' ||
      run?.status === 'failed';
    if (!isFinished) {
      // During active run, client-side filtering by stepId handles it
      setStepLogs(null);
      return;
    }

    // Check in-memory step cache
    if (stepLogsCacheRef.current[selectedStepId]) {
      setStepLogs(stepLogsCacheRef.current[selectedStepId]);
      return;
    }

    // Clear previous step logs while fetching new step logs
    setStepLogs(null);

    // Fetch step-specific logs in background without setting global loading
    fetchLogs(runId, selectedStepId)
      .then((res) => {
        if (active) {
          const fetched = res.logs || [];
          stepLogsCacheRef.current[selectedStepId] = fetched;
          setStepLogs(fetched);
        }
      })
      .catch((err) => {
        if (active) {
          console.warn('Failed to fetch step logs for', selectedStepId, err);
        }
      });

    return () => {
      active = false;
    };
  }, [runId, selectedStepId, run?.status]);

  // Set up SSE live subscription if run is active
  useEffect(() => {
    if (!runId || !run) return;

    const isRunning =
      run.status === 'running' || run.status === 'in_progress' || run.status === 'pending';

    if (isRunning) {
      // Connect to SSE stream
      const unsub = subscribeLogs(
        runId,
        (evt: SSELogEvent) => {
          const text = evt.log_line || evt.line;
          if (text) {
            setLogLines((prev) => [...prev, { line: text, stepId: evt.step_id }]);
          }

          // Handle step updates
          if (evt.step) {
            setSteps((prev) => {
              const incoming = evt.step as StepMetadata;
              const idx = prev.findIndex((s) => s.id === incoming.id || s.name === incoming.name);
              if (idx >= 0) {
                const updated = [...prev];
                updated[idx] = { ...updated[idx], ...incoming };
                return updated;
              }
              return [...prev, incoming];
            });
          }

          // Handle run status updates
          if (evt.job || evt.type === 'job_complete' || evt.type === 'run_finished') {
            setRun((prev) => (prev ? { ...prev, status: evt.job?.status || 'completed' } : prev));
          }
        },
        {
          onError: () => {
            // Reconnection handled inside sse.ts
          },
        }
      );

      unsubscribeRef.current = unsub;

      // Poll telemetry metrics every 2.5 seconds while active
      const metricsInterval = setInterval(async () => {
        try {
          const m = await fetchRunMetrics(runId);
          if (m && m.length > 0) {
            setMetrics(m);
          }
        } catch {
          // Ignore polling errors
        }
      }, 2500);

      return () => {
        unsub();
        clearInterval(metricsInterval);
      };
    }
  }, [runId, run?.status]);

  // Filter logs by selected step if one is selected
  const displayedLogs = useMemo(() => {
    if (!selectedStepId) {
      return logLines.map((l) => l.line);
    }
    // If step-specific logs were fetched from backend
    if (stepLogs !== null) {
      return stepLogs;
    }
    // Otherwise filter in-memory logLines (tagged by SSE during streaming)
    const filtered = logLines.filter((l) => !l.stepId || l.stepId === selectedStepId);
    return filtered.map((l) => l.line);
  }, [logLines, selectedStepId, stepLogs]);

  if (!runId) {
    return (
      <div className="rounded-geist bg-geist-surface border border-geist-hairline p-12 text-center text-geist-secondary space-y-3">
        <Terminal className="w-10 h-10 mx-auto text-geist-hairline" />
        <h3 className="text-sm font-semibold text-white">No Run Selected</h3>
        <p className="text-xs text-geist-faint">
          Select a workflow run from History or Dashboard to view execution details.
        </p>
        <Button variant="outline" size="sm" onClick={onBack} className="h-8 text-xs">
          ← Back to History
        </Button>
      </div>
    );
  }

  const isCompleted = run?.status === 'completed' || run?.status === 'success';
  const isFailed = run?.status === 'failed';
  const isRunning = run?.status === 'running' || run?.status === 'in_progress';
  const isQueued = run?.status === 'queued';

  return (
    <div className="space-y-5">
      {/* Run Header Card */}
      <div className="rounded-geist bg-geist-surface border border-geist-hairline p-4 space-y-3">
        <div className="flex flex-col sm:flex-row items-start sm:items-center justify-between gap-3">
          <div className="flex items-center gap-3">
            <Button
              variant="outline"
              size="sm"
              onClick={onBack}
              className="h-8 px-2.5 text-xs text-geist-secondary hover:text-white gap-1.5"
            >
              <ArrowLeft className="w-3.5 h-3.5" />
              <span>Back</span>
            </Button>

            <div className="flex items-center gap-2">
              <span className="font-mono text-sm font-bold text-white tracking-tight">
                {run?.workflow || run?.name || runId.slice(0, 8)}
              </span>
              <span className="font-mono text-xs text-geist-faint">({runId.slice(0, 8)})</span>
            </div>
          </div>

          <div className="flex items-center gap-2.5">
            <Badge
              variant={
                isCompleted
                  ? 'success'
                  : isFailed
                  ? 'destructive'
                  : isRunning
                  ? 'warning'
                  : isQueued
                  ? 'outline'
                  : 'outline'
              }
              className={cn(
                'text-xs uppercase font-mono px-2 py-0.5 gap-1.5',
                isQueued && 'text-status-warning-text border-status-warning-border/60 bg-status-warning-bg/40'
              )}
            >
              {isCompleted ? (
                <CheckCircle2 className="w-3.5 h-3.5 text-status-success-solid" />
              ) : isFailed ? (
                <XCircle className="w-3.5 h-3.5 text-status-danger-solid" />
              ) : isRunning ? (
                <Activity className="w-3.5 h-3.5 text-status-warning-solid animate-pulse" />
              ) : (
                <Clock
                  className={cn(
                    'w-3.5 h-3.5',
                    isQueued ? 'text-status-warning-text' : 'text-geist-faint'
                  )}
                />
              )}
              <span>{run?.status || 'loading'}</span>
            </Badge>

            <Button
              variant="ghost"
              size="sm"
              onClick={loadRunData}
              disabled={loading}
              className="h-8 px-2 text-xs text-geist-secondary hover:text-white"
              title="Refresh run"
            >
              <RotateCcw className={cn('w-3.5 h-3.5', loading && 'animate-spin')} />
            </Button>
          </div>
        </div>

        {/* Metadata Details Row */}
        <div className="grid grid-cols-2 sm:grid-cols-4 gap-3 pt-3 border-t border-geist-hairline/60 text-xs font-mono">
          <div>
            <div className="text-[10px] uppercase text-geist-faint">Repository</div>
            <div className="text-white font-medium truncate mt-0.5">
              {run?.repo_alias || run?.repo || '-'}
            </div>
          </div>

          <div>
            <div className="text-[10px] uppercase text-geist-faint">Driver</div>
            <div className="text-white font-medium truncate mt-0.5">
              {run?.driver || 'docker'}
            </div>
          </div>

          <div>
            <div className="text-[10px] uppercase text-geist-faint">Duration</div>
            <div className="text-white font-medium truncate mt-0.5">
              {formatDuration(run?.duration_sec || 0)}
            </div>
          </div>

          <div>
            <div className="text-[10px] uppercase text-geist-faint">Started</div>
            <div className="text-white font-medium truncate mt-0.5">
              {formatRelativeTime(run?.started_at || '')}
            </div>
          </div>
        </div>
      </div>

      {/* Split Layout: Left Pane Steps Tree | Right Pane Tabs (Logs & Telemetry) */}
      <div className="grid grid-cols-1 lg:grid-cols-12 gap-5 min-h-[560px]">
        {/* Left Pane: Step Hierarchy Tree */}
        <div className="lg:col-span-4 rounded-geist bg-geist-surface border border-geist-hairline p-3 flex flex-col justify-between overflow-hidden">
          <div>
            <div className="flex items-center justify-between pb-2.5 mb-2 border-b border-geist-hairline">
              <div className="flex items-center gap-2">
                <Layers className="w-4 h-4 text-geist-blue" />
                <span className="text-xs font-semibold text-white">Execution Steps</span>
              </div>
              <Badge variant="outline" className="font-mono text-[10px]">
                {steps.length} steps
              </Badge>
            </div>

            {/* All Steps Selector */}
            <button
              onClick={() => setSelectedStepId(null)}
              className={cn(
                'w-full flex items-center justify-between px-3 py-2 rounded-geist text-xs font-mono transition-colors duration-150 mb-1 cursor-pointer text-left',
                selectedStepId === null
                  ? 'bg-geist-elevated text-white border border-geist-hairline shadow-sm'
                  : 'text-geist-secondary hover:text-white hover:bg-geist-hover border border-transparent'
              )}
            >
              <div className="flex items-center gap-2">
                <Terminal className="w-3.5 h-3.5 text-geist-faint" />
                <span>All Steps (Full Stream)</span>
              </div>
              <span className="text-[10px] text-geist-faint">{logLines.length} lines</span>
            </button>

            {/* Individual Steps List */}
            <div className="space-y-1 max-h-[460px] overflow-y-auto pr-1">
              {steps.length === 0 ? (
                <div className="p-6 text-center text-geist-faint text-xs space-y-1">
                  <p>No step metadata reported yet.</p>
                  <p className="text-[11px]">Steps will appear as execution advances.</p>
                </div>
              ) : (
                steps.map((step, idx) => {
                  const stepFinished = step.status === 'completed' || step.status === 'success';
                  const stepFailed = step.status === 'failed' || step.exit_code !== 0;
                  const stepActive =
                    step.status === 'running' || step.status === 'in_progress';
                  const isSelected = selectedStepId === step.id;

                  return (
                    <button
                      key={step.id || idx}
                      onClick={() => setSelectedStepId(step.id)}
                      className={cn(
                        'w-full flex items-center justify-between p-2.5 rounded-geist text-xs font-mono transition-all duration-150 cursor-pointer text-left border',
                        isSelected
                          ? 'bg-geist-elevated text-white border-geist-hairline shadow-sm'
                          : 'bg-transparent text-geist-secondary hover:text-white hover:bg-geist-hover border-transparent'
                      )}
                    >
                      <div className="flex items-center gap-2.5 min-w-0 pr-2">
                        {stepFinished ? (
                          <CheckCircle2 className="w-3.5 h-3.5 text-status-success-solid flex-shrink-0" />
                        ) : stepFailed ? (
                          <XCircle className="w-3.5 h-3.5 text-status-danger-solid flex-shrink-0" />
                        ) : stepActive ? (
                          <Activity className="w-3.5 h-3.5 text-geist-blue animate-pulse flex-shrink-0" />
                        ) : (
                          <Clock className="w-3.5 h-3.5 text-geist-faint flex-shrink-0" />
                        )}

                        <div className="min-w-0 truncate">
                          <div className="font-medium text-white truncate text-xs">
                            {step.name || `Step ${step.number || idx + 1}`}
                          </div>
                          {step.oom_killed && (
                            <span className="text-[10px] text-status-danger-text flex items-center gap-1">
                              <AlertTriangle className="w-3 h-3" />
                              OOM Killed
                            </span>
                          )}
                        </div>
                      </div>

                      <div className="text-right flex-shrink-0 text-[11px] text-geist-faint">
                        {step.duration_sec ? `${step.duration_sec.toFixed(1)}s` : '-'}
                      </div>
                    </button>
                  );
                })
              )}
            </div>
          </div>

          {/* Step Footer Quick Info */}
          <div className="pt-2 border-t border-geist-hairline/60 text-[11px] font-mono text-geist-faint flex items-center justify-between">
            <span>Filter: {selectedStepId ? 'Active Step' : 'All Output'}</span>
            <span>{displayedLogs.length} lines</span>
          </div>
        </div>

        {/* Right Pane: Tabs for Logs and Telemetry */}
        <div className="lg:col-span-8 flex flex-col min-w-0">
          <Tabs
            value={activeTab}
            onValueChange={(v) => setActiveTab(v as 'logs' | 'telemetry')}
            className="flex-1 flex flex-col"
          >
            <div className="flex items-center justify-between mb-3 select-none">
              <TabsList className="bg-geist-elevated border border-geist-hairline">
                <TabsTrigger value="logs" className="gap-1.5 font-mono text-xs">
                  <Terminal className="w-3.5 h-3.5" />
                  <span>Console Logs</span>
                </TabsTrigger>
                <TabsTrigger value="telemetry" className="gap-1.5 font-mono text-xs">
                  <Activity className="w-3.5 h-3.5 text-geist-blue" />
                  <span>Telemetry Metrics</span>
                  {metrics.length > 0 && (
                    <span className="ml-1 text-[10px] px-1 py-0.2 rounded bg-geist-hover text-geist-secondary">
                      {metrics.length}
                    </span>
                  )}
                </TabsTrigger>
              </TabsList>
            </div>

            {/* Logs Tab */}
            <TabsContent value="logs" className="flex-1 mt-0">
              <LogViewer
                logs={displayedLogs}
                isLoading={loading}
                isStreaming={isRunning}
                height={520}
                title={
                  selectedStepId
                    ? `Step Output: ${steps.find((s) => s.id === selectedStepId)?.name || selectedStepId}`
                    : 'Workflow Execution Console'
                }
                emptyMessage="No output received for this step."
              />
            </TabsContent>

            {/* Telemetry Tab */}
            <TabsContent value="telemetry" className="flex-1 mt-0 space-y-4">
              {/* Summary KPIs from run metrics_summary */}
              {run?.metrics_summary && (
                <div className="grid grid-cols-2 sm:grid-cols-4 gap-3">
                  <div className="p-3 rounded-geist bg-geist-surface border border-geist-hairline">
                    <div className="text-[10px] font-mono text-geist-faint uppercase">
                      Peak CPU
                    </div>
                    <div className="text-lg font-bold font-mono text-white mt-0.5">
                      {run.metrics_summary.peak_cpu_percent?.toFixed(1) ?? '0.0'}%
                    </div>
                  </div>

                  <div className="p-3 rounded-geist bg-geist-surface border border-geist-hairline">
                    <div className="text-[10px] font-mono text-geist-faint uppercase">
                      Peak RSS
                    </div>
                    <div className="text-lg font-bold font-mono text-white mt-0.5">
                      {formatBytes(run.metrics_summary.peak_rss_bytes || 0)}
                    </div>
                  </div>

                  <div className="p-3 rounded-geist bg-geist-surface border border-geist-hairline">
                    <div className="text-[10px] font-mono text-geist-faint uppercase">
                      Disk Written
                    </div>
                    <div className="text-lg font-bold font-mono text-white mt-0.5">
                      {formatBytes(run.metrics_summary.total_disk_written_bytes || 0)}
                    </div>
                  </div>

                  <div className="p-3 rounded-geist bg-geist-surface border border-geist-hairline">
                    <div className="text-[10px] font-mono text-geist-faint uppercase">
                      Network I/O
                    </div>
                    <div className="text-lg font-bold font-mono text-white mt-0.5">
                      {formatBytes(
                        (run.metrics_summary.total_net_rx_bytes || 0) +
                          (run.metrics_summary.total_net_tx_bytes || 0)
                      )}
                    </div>
                  </div>
                </div>
              )}

              {/* Charts Grid: Side-by-side on wide screens */}
              <div className="grid grid-cols-1 xl:grid-cols-2 gap-4">
                {/* Vitals Area Chart: CPU % and Memory RSS */}
                <VitalsAreaChart
                  data={metrics}
                  height={240}
                  title="Process & Host Vitals (CPU / Memory)"
                  description="Live sampled CPU percentage and memory resident set size (RSS)"
                />

                {/* Throughput Chart: Disk Write & Net RX/TX */}
                <ThroughputChart
                  data={metrics}
                  height={240}
                  title="I/O & Network Throughput"
                  description="Disk write throughput rate and network interface ingress/egress"
                />
              </div>
            </TabsContent>
          </Tabs>
        </div>
      </div>
    </div>
  );
}

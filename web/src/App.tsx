import { useEffect, useState, useCallback } from 'react';
import {
  LayoutDashboard,
  Server,
  History,
  KeyRound,
  Terminal,
  Activity,
  Cpu,
  HardDrive,
  RefreshCw,
  Power,
  ChevronRight,
  Shield,
  Clock,
  AlertCircle,
  CheckCircle2,
  XCircle,
  Boxes,
} from 'lucide-react';
import {
  fetchStatus,
  fetchRunners,
  fetchRuns,
  fetchSecrets,
  startService,
  stopService,
  type StatusResponse,
  type RunnerInfo,
  type RunRecord,
  type SecretMetadata,
} from '@/lib/api';
import { Button } from '@/components/ui/button';
import { Badge } from '@/components/ui/badge';
import { cn, formatBytes, formatDuration, formatRelativeTime } from '@/lib/utils';

type NavTab = 'dashboard' | 'runners' | 'history' | 'secrets' | 'run-detail';

export default function App() {
  const [activeTab, setActiveTab] = useState<NavTab>('dashboard');
  const [activeRunId, setActiveRunId] = useState<string | null>(null);

  // Global status state
  const [status, setStatus] = useState<StatusResponse | null>(null);
  const [statusLoading, setStatusLoading] = useState(true);
  const [statusError, setStatusError] = useState<string | null>(null);
  const [lastRefreshed, setLastRefreshed] = useState<Date>(new Date());

  // Data states
  const [runners, setRunners] = useState<RunnerInfo[]>([]);
  const [runs, setRuns] = useState<RunRecord[]>([]);
  const [secrets, setSecrets] = useState<SecretMetadata[]>([]);
  const [serviceActionLoading, setServiceActionLoading] = useState(false);

  const loadStatus = useCallback(async () => {
    try {
      setStatusLoading(true);
      const s = await fetchStatus();
      setStatus(s);
      setStatusError(null);
      setLastRefreshed(new Date());
    } catch (err: unknown) {
      const msg = err instanceof Error ? err.message : 'Failed to fetch status';
      setStatusError(msg);
    } finally {
      setStatusLoading(false);
    }
  }, []);

  const loadRunners = useCallback(async () => {
    try {
      const data = await fetchRunners();
      setRunners(data);
    } catch (err) {
      console.warn('Failed to fetch runners', err);
    }
  }, []);

  const loadRuns = useCallback(async () => {
    try {
      const res = await fetchRuns({ limit: 10 });
      setRuns(res.runs);
    } catch (err) {
      console.warn('Failed to fetch runs', err);
    }
  }, []);

  const loadSecrets = useCallback(async () => {
    try {
      const data = await fetchSecrets();
      setSecrets(data);
    } catch (err) {
      console.warn('Failed to fetch secrets', err);
    }
  }, []);

  // Poll status every 5 seconds
  useEffect(() => {
    loadStatus();
    loadRunners();
    loadRuns();
    const interval = setInterval(() => {
      loadStatus();
    }, 5000);
    return () => clearInterval(interval);
  }, [loadStatus, loadRunners, loadRuns]);

  // Load tab-specific data on switch
  useEffect(() => {
    if (activeTab === 'runners') loadRunners();
    if (activeTab === 'history') loadRuns();
    if (activeTab === 'secrets') loadSecrets();
  }, [activeTab, loadRunners, loadRuns, loadSecrets]);

  const handleToggleService = async () => {
    if (!status) return;
    setServiceActionLoading(true);
    try {
      if (status.daemon_connected) {
        await stopService();
      } else {
        await startService();
      }
      await loadStatus();
    } catch (err) {
      console.error('Service action failed:', err);
    } finally {
      setServiceActionLoading(false);
    }
  };

  const handleSelectRun = (runId: string) => {
    setActiveRunId(runId);
    setActiveTab('run-detail');
  };

  return (
    <div className="flex h-screen w-screen overflow-hidden bg-geist-canvas text-geist-ink font-sans selection:bg-[#0070f3]/25 selection:text-white">
      {/* Sidebar Navigation */}
      <aside className="w-64 flex-shrink-0 flex flex-col justify-between border-r border-geist-hairline bg-geist-surface select-none">
        {/* Brand & App Identity */}
        <div>
          <div className="h-14 flex items-center justify-between px-4 border-b border-geist-hairline">
            <div className="flex items-center gap-2.5">
              <div className="w-7 h-7 rounded-geist bg-[#ededed] text-[#0a0a0a] flex items-center justify-center font-mono font-bold text-sm tracking-tight shadow-sm">
                a
              </div>
              <div className="flex items-center gap-2">
                <span className="font-semibold text-sm tracking-tight text-white">actr</span>
                <span className="text-[10px] uppercase font-mono px-1.5 py-0.5 rounded bg-geist-hover text-geist-faint border border-geist-hairline">
                  web
                </span>
              </div>
            </div>
            <Badge variant="outline" className="font-mono text-[10px] text-geist-secondary px-1.5 py-0">
              {status?.version || 'dev'}
            </Badge>
          </div>

          {/* Navigation Links */}
          <nav className="p-3 space-y-1">
            <button
              onClick={() => setActiveTab('dashboard')}
              className={cn(
                'w-full flex items-center gap-2.5 px-3 py-2 rounded-geist text-xs font-medium transition-colors duration-150 cursor-pointer',
                activeTab === 'dashboard'
                  ? 'bg-geist-elevated text-white border border-geist-hairline shadow-sm'
                  : 'text-geist-body hover:text-white hover:bg-geist-hover border border-transparent'
              )}
            >
              <LayoutDashboard className="w-4 h-4 text-geist-secondary" />
              <span>Dashboard</span>
            </button>

            <button
              onClick={() => setActiveTab('runners')}
              className={cn(
                'w-full flex items-center justify-between px-3 py-2 rounded-geist text-xs font-medium transition-colors duration-150 cursor-pointer',
                activeTab === 'runners'
                  ? 'bg-geist-elevated text-white border border-geist-hairline shadow-sm'
                  : 'text-geist-body hover:text-white hover:bg-geist-hover border border-transparent'
              )}
            >
              <div className="flex items-center gap-2.5">
                <Server className="w-4 h-4 text-geist-secondary" />
                <span>Runners</span>
              </div>
              {status?.total_runners !== undefined && (
                <span className="font-mono text-[10px] text-geist-faint">
                  {status.total_runners}
                </span>
              )}
            </button>

            <button
              onClick={() => setActiveTab('history')}
              className={cn(
                'w-full flex items-center justify-between px-3 py-2 rounded-geist text-xs font-medium transition-colors duration-150 cursor-pointer',
                activeTab === 'history'
                  ? 'bg-geist-elevated text-white border border-geist-hairline shadow-sm'
                  : 'text-geist-body hover:text-white hover:bg-geist-hover border border-transparent'
              )}
            >
              <div className="flex items-center gap-2.5">
                <History className="w-4 h-4 text-geist-secondary" />
                <span>History</span>
              </div>
              {status?.active_jobs ? (
                <span className="w-2 h-2 rounded-full bg-status-warning-solid animate-pulse" />
              ) : null}
            </button>

            <button
              onClick={() => setActiveTab('secrets')}
              className={cn(
                'w-full flex items-center gap-2.5 px-3 py-2 rounded-geist text-xs font-medium transition-colors duration-150 cursor-pointer',
                activeTab === 'secrets'
                  ? 'bg-geist-elevated text-white border border-geist-hairline shadow-sm'
                  : 'text-geist-body hover:text-white hover:bg-geist-hover border border-transparent'
              )}
            >
              <KeyRound className="w-4 h-4 text-geist-secondary" />
              <span>Secrets Vault</span>
            </button>

            {activeRunId && (
              <button
                onClick={() => setActiveTab('run-detail')}
                className={cn(
                  'w-full flex items-center justify-between px-3 py-2 rounded-geist text-xs font-medium transition-colors duration-150 cursor-pointer',
                  activeTab === 'run-detail'
                    ? 'bg-geist-elevated text-white border border-geist-hairline shadow-sm'
                    : 'text-geist-body hover:text-white hover:bg-geist-hover border border-transparent'
                )}
              >
                <div className="flex items-center gap-2.5 truncate">
                  <Terminal className="w-4 h-4 text-geist-blue flex-shrink-0" />
                  <span className="truncate">Run: {activeRunId.slice(0, 8)}</span>
                </div>
                <ChevronRight className="w-3.5 h-3.5 text-geist-faint flex-shrink-0" />
              </button>
            )}
          </nav>
        </div>

        {/* Sidebar Daemon Control Card */}
        <div className="p-3 border-t border-geist-hairline">
          <div className="p-3 rounded-geist bg-geist-elevated border border-geist-hairline space-y-2.5">
            <div className="flex items-center justify-between">
              <span className="text-[11px] font-medium text-geist-faint uppercase tracking-wider">
                Daemon
              </span>
              <div className="flex items-center gap-1.5">
                <span
                  className={cn(
                    'w-2 h-2 rounded-full',
                    status?.daemon_connected
                      ? 'bg-status-success-solid animate-pulse'
                      : 'bg-status-danger-solid'
                  )}
                />
                <span className="text-[11px] font-medium">
                  {status?.daemon_connected ? 'Active' : 'Offline'}
                </span>
              </div>
            </div>

            <Button
              variant="outline"
              size="sm"
              onClick={handleToggleService}
              disabled={serviceActionLoading || !status}
              className="w-full text-xs h-7 gap-1.5 justify-center"
            >
              <Power className="w-3.5 h-3.5" />
              <span>{status?.daemon_connected ? 'Stop Service' : 'Start Service'}</span>
            </Button>
          </div>
        </div>
      </aside>

      {/* Main View Area */}
      <main className="flex-1 flex flex-col min-w-0 bg-geist-canvas overflow-hidden">
        {/* Top Navbar */}
        <header className="h-14 flex items-center justify-between px-6 border-b border-geist-hairline bg-geist-surface/50 backdrop-blur select-none">
          <div className="flex items-center gap-3">
            <h1 className="text-sm font-semibold capitalize text-white">
              {activeTab === 'run-detail' ? `Run Detail (${activeRunId?.slice(0, 8)})` : activeTab}
            </h1>
            {activeTab === 'dashboard' && (
              <Badge variant="outline" className="text-[11px] gap-1 font-mono text-geist-secondary">
                <Activity className="w-3 h-3 text-geist-blue" />
                Live Vitals
              </Badge>
            )}
          </div>

          <div className="flex items-center gap-2">
            <Button
              variant="ghost"
              size="sm"
              onClick={loadStatus}
              disabled={statusLoading}
              className="h-8 gap-1.5 text-xs text-geist-secondary hover:text-white"
            >
              <RefreshCw className={cn('w-3.5 h-3.5', statusLoading && 'animate-spin')} />
              <span>Refresh</span>
            </Button>
          </div>
        </header>

        {/* Dynamic Content Views */}
        <div className="flex-1 overflow-y-auto p-6 space-y-6">
          {statusError && (
            <div className="p-3 rounded-geist border border-status-danger-border bg-status-danger-bg text-status-danger-text text-xs flex items-center gap-2">
              <AlertCircle className="w-4 h-4 flex-shrink-0" />
              <span>Failed to communicate with actr daemon: {statusError}</span>
            </div>
          )}

          {activeTab === 'dashboard' && (
            <DashboardView
              status={status}
              runs={runs}
              runners={runners}
              onSelectRun={handleSelectRun}
            />
          )}

          {activeTab === 'runners' && (
            <RunnersView runners={runners} onRefresh={loadRunners} />
          )}

          {activeTab === 'history' && (
            <HistoryView runs={runs} onSelectRun={handleSelectRun} onRefresh={loadRuns} />
          )}

          {activeTab === 'secrets' && (
            <SecretsView secrets={secrets} onRefresh={loadSecrets} />
          )}

          {activeTab === 'run-detail' && (
            <RunDetailView
              runId={activeRunId}
              onBack={() => setActiveTab('history')}
            />
          )}
        </div>

        {/* Global Footer Status Bar */}
        <footer className="h-9 px-4 border-t border-geist-hairline bg-geist-surface flex items-center justify-between text-xs font-mono text-geist-secondary select-none">
          <div className="flex items-center gap-4">
            <div className="flex items-center gap-1.5">
              <span
                className={cn(
                  'w-2 h-2 rounded-full',
                  status?.daemon_connected
                    ? 'bg-status-success-solid'
                    : 'bg-status-danger-solid'
                )}
              />
              <span className="text-[11px] text-geist-ink">
                {status?.daemon_connected ? 'Daemon Connected' : 'Daemon Disconnected'}
              </span>
            </div>

            <div className="h-3 w-px bg-geist-hairline" />

            <div className="flex items-center gap-1 text-[11px]">
              <Clock className="w-3 h-3 text-geist-faint" />
              <span>Uptime: {formatDuration(status?.uptime_seconds || 0)}</span>
            </div>

            <div className="h-3 w-px bg-geist-hairline" />

            <div className="flex items-center gap-1 text-[11px]">
              <Cpu className="w-3 h-3 text-geist-faint" />
              <span>CPU: {status?.host_cpu?.toFixed(1) ?? '0.0'}%</span>
            </div>

            <div className="h-3 w-px bg-geist-hairline" />

            <div className="flex items-center gap-1 text-[11px]">
              <HardDrive className="w-3 h-3 text-geist-faint" />
              <span>
                Mem: {formatBytes(status?.host_mem_used || 0)} / {formatBytes(status?.host_mem_total || 0)}
              </span>
            </div>
          </div>

          <div className="flex items-center gap-3 text-[11px] text-geist-faint">
            <span>Jobs: {status?.active_jobs || 0} active</span>
            <span className="text-geist-hairline">|</span>
            <span>Refreshed: {lastRefreshed.toLocaleTimeString()}</span>
          </div>
        </footer>
      </main>
    </div>
  );
}

// --------------------------------------------------------------------------
// Sub-Views Foundation (Task 6 UI shell foundation)
// --------------------------------------------------------------------------

function DashboardView({
  status,
  runs,
  runners,
  onSelectRun,
}: {
  status: StatusResponse | null;
  runs: RunRecord[];
  runners: RunnerInfo[];
  onSelectRun: (id: string) => void;
}) {
  return (
    <div className="space-y-6">
      {/* Vitals KPI Cards */}
      <div className="grid grid-cols-1 sm:grid-cols-2 lg:grid-cols-4 gap-4">
        <div className="p-4 rounded-geist bg-geist-surface border border-geist-hairline space-y-1">
          <div className="flex items-center justify-between text-geist-secondary text-xs">
            <span>Daemon State</span>
            <Power className="w-3.5 h-3.5 text-geist-faint" />
          </div>
          <div className="text-xl font-semibold tracking-tight text-white flex items-center gap-2">
            {status?.daemon_connected ? 'Active' : 'Offline'}
            <span
              className={cn(
                'w-2 h-2 rounded-full',
                status?.daemon_connected ? 'bg-status-success-solid' : 'bg-status-danger-solid'
              )}
            />
          </div>
          <p className="text-[11px] text-geist-faint">
            actr runtime v{status?.version || 'dev'}
          </p>
        </div>

        <div className="p-4 rounded-geist bg-geist-surface border border-geist-hairline space-y-1">
          <div className="flex items-center justify-between text-geist-secondary text-xs">
            <span>Registered Runners</span>
            <Server className="w-3.5 h-3.5 text-geist-faint" />
          </div>
          <div className="text-xl font-semibold tracking-tight text-white">
            {runners.length || status?.total_runners || 0}
          </div>
          <p className="text-[11px] text-geist-faint">Ready to accept workflow jobs</p>
        </div>

        <div className="p-4 rounded-geist bg-geist-surface border border-geist-hairline space-y-1">
          <div className="flex items-center justify-between text-geist-secondary text-xs">
            <span>Host CPU</span>
            <Cpu className="w-3.5 h-3.5 text-geist-faint" />
          </div>
          <div className="text-xl font-semibold tracking-tight text-white font-mono">
            {status?.host_cpu?.toFixed(1) ?? '0.0'}%
          </div>
          <div className="w-full bg-geist-elevated h-1 rounded-full overflow-hidden">
            <div
              className="bg-geist-blue h-full transition-all duration-300"
              style={{ width: `${Math.min(status?.host_cpu || 0, 100)}%` }}
            />
          </div>
        </div>

        <div className="p-4 rounded-geist bg-geist-surface border border-geist-hairline space-y-1">
          <div className="flex items-center justify-between text-geist-secondary text-xs">
            <span>Host Memory</span>
            <HardDrive className="w-3.5 h-3.5 text-geist-faint" />
          </div>
          <div className="text-xl font-semibold tracking-tight text-white font-mono">
            {formatBytes(status?.host_mem_used || 0)}
          </div>
          <p className="text-[11px] text-geist-faint">
            of {formatBytes(status?.host_mem_total || 0)} total
          </p>
        </div>
      </div>

      {/* Recent Runs Preview */}
      <div className="rounded-geist bg-geist-surface border border-geist-hairline overflow-hidden">
        <div className="px-4 py-3 border-b border-geist-hairline flex items-center justify-between">
          <div className="flex items-center gap-2">
            <History className="w-4 h-4 text-geist-secondary" />
            <h2 className="text-xs font-semibold text-white">Recent Workflow Runs</h2>
          </div>
          <span className="text-[11px] text-geist-faint">Showing {runs.length} most recent</span>
        </div>

        {runs.length === 0 ? (
          <div className="p-8 text-center text-geist-secondary text-xs space-y-1">
            <Boxes className="w-8 h-8 mx-auto text-geist-hairline" />
            <p className="text-geist-body font-medium">No recent workflow runs recorded.</p>
            <p className="text-geist-faint">Executions triggered via actr will appear here.</p>
          </div>
        ) : (
          <div className="divide-y divide-geist-hairline">
            {runs.map((r) => (
              <div
                key={r.id}
                onClick={() => onSelectRun(r.id)}
                className="px-4 py-3 flex items-center justify-between hover:bg-geist-hover transition-colors duration-150 cursor-pointer"
              >
                <div className="flex items-center gap-3">
                  {r.status === 'completed' ? (
                    <CheckCircle2 className="w-4 h-4 text-status-success-solid" />
                  ) : r.status === 'failed' ? (
                    <XCircle className="w-4 h-4 text-status-danger-solid" />
                  ) : (
                    <Activity className="w-4 h-4 text-status-warning-solid animate-pulse" />
                  )}
                  <div>
                    <div className="flex items-center gap-2">
                      <span className="text-xs font-semibold text-white font-mono">
                        {r.name || r.workflow || r.id.slice(0, 8)}
                      </span>
                      <span className="text-[10px] text-geist-faint font-mono">
                        {r.repo}
                      </span>
                    </div>
                    <div className="flex items-center gap-2 text-[11px] text-geist-secondary">
                      <span>Started {formatRelativeTime(r.started_at)}</span>
                      <span>•</span>
                      <span>Duration: {formatDuration(r.duration_sec)}</span>
                    </div>
                  </div>
                </div>
                <div className="flex items-center gap-2">
                  <Badge
                    variant={
                      r.status === 'completed'
                        ? 'success'
                        : r.status === 'failed'
                        ? 'destructive'
                        : 'warning'
                    }
                  >
                    {r.status}
                  </Badge>
                  <ChevronRight className="w-4 h-4 text-geist-faint" />
                </div>
              </div>
            ))}
          </div>
        )}
      </div>
    </div>
  );
}

function RunnersView({
  runners,
  onRefresh,
}: {
  runners: RunnerInfo[];
  onRefresh: () => void;
}) {
  return (
    <div className="rounded-geist bg-geist-surface border border-geist-hairline overflow-hidden">
      <div className="px-4 py-3 border-b border-geist-hairline flex items-center justify-between">
        <div className="flex items-center gap-2">
          <Server className="w-4 h-4 text-geist-secondary" />
          <h2 className="text-xs font-semibold text-white">Configured Runners</h2>
        </div>
        <Button variant="outline" size="sm" onClick={onRefresh} className="h-7 text-xs">
          Refresh
        </Button>
      </div>

      {runners.length === 0 ? (
        <div className="p-8 text-center text-geist-secondary text-xs space-y-1">
          <Server className="w-8 h-8 mx-auto text-geist-hairline" />
          <p className="text-geist-body font-medium">No active runners found.</p>
          <p className="text-geist-faint">
            Check your configuration in config.yaml or supervisor.yaml.
          </p>
        </div>
      ) : (
        <div className="divide-y divide-geist-hairline">
          {runners.map((r, idx) => (
            <div key={idx} className="p-4 flex items-center justify-between">
              <div className="space-y-1">
                <div className="flex items-center gap-2">
                  <span className="text-xs font-semibold text-white font-mono">{r.name}</span>
                  <span className="text-[11px] text-geist-faint font-mono">({r.repo})</span>
                </div>
                <div className="flex items-center gap-1.5 flex-wrap">
                  {r.labels?.map((label, lIdx) => (
                    <span
                      key={lIdx}
                      className="text-[10px] px-1.5 py-0.5 rounded bg-geist-elevated text-geist-secondary border border-geist-hairline font-mono"
                    >
                      {label}
                    </span>
                  ))}
                </div>
              </div>

              <div className="flex items-center gap-4 text-xs font-mono">
                <div className="text-right">
                  <div className="text-geist-faint text-[10px]">Concurrency</div>
                  <div className="text-white">
                    {r.active_slots} / {r.max_concurrency} slots
                  </div>
                </div>
                <Badge variant={r.status === 'busy' ? 'warning' : 'success'}>
                  {r.status || 'idle'}
                </Badge>
              </div>
            </div>
          ))}
        </div>
      )}
    </div>
  );
}

function HistoryView({
  runs,
  onSelectRun,
  onRefresh,
}: {
  runs: RunRecord[];
  onSelectRun: (id: string) => void;
  onRefresh: () => void;
}) {
  return (
    <div className="rounded-geist bg-geist-surface border border-geist-hairline overflow-hidden">
      <div className="px-4 py-3 border-b border-geist-hairline flex items-center justify-between">
        <div className="flex items-center gap-2">
          <History className="w-4 h-4 text-geist-secondary" />
          <h2 className="text-xs font-semibold text-white">Run History</h2>
        </div>
        <Button variant="outline" size="sm" onClick={onRefresh} className="h-7 text-xs">
          Refresh
        </Button>
      </div>

      {runs.length === 0 ? (
        <div className="p-8 text-center text-geist-secondary text-xs space-y-1">
          <History className="w-8 h-8 mx-auto text-geist-hairline" />
          <p className="text-geist-body font-medium">No run records found.</p>
        </div>
      ) : (
        <div className="divide-y divide-geist-hairline">
          {runs.map((r) => (
            <div
              key={r.id}
              onClick={() => onSelectRun(r.id)}
              className="p-4 flex items-center justify-between hover:bg-geist-hover transition-colors duration-150 cursor-pointer"
            >
              <div className="space-y-1">
                <div className="flex items-center gap-2">
                  <span className="text-xs font-semibold text-white font-mono">
                    {r.name || r.id.slice(0, 8)}
                  </span>
                  <span className="text-[11px] text-geist-faint font-mono">{r.repo}</span>
                </div>
                <div className="flex items-center gap-3 text-[11px] text-geist-secondary">
                  <span>Driver: {r.driver || 'docker'}</span>
                  <span>•</span>
                  <span>Duration: {formatDuration(r.duration_sec)}</span>
                  <span>•</span>
                  <span>Started: {formatRelativeTime(r.started_at)}</span>
                </div>
              </div>

              <div className="flex items-center gap-3">
                <Badge
                  variant={
                    r.status === 'completed'
                      ? 'success'
                      : r.status === 'failed'
                      ? 'destructive'
                      : 'warning'
                  }
                >
                  {r.status}
                </Badge>
                <ChevronRight className="w-4 h-4 text-geist-faint" />
              </div>
            </div>
          ))}
        </div>
      )}
    </div>
  );
}

function SecretsView({
  secrets,
  onRefresh,
}: {
  secrets: SecretMetadata[];
  onRefresh: () => void;
}) {
  return (
    <div className="rounded-geist bg-geist-surface border border-geist-hairline overflow-hidden">
      <div className="px-4 py-3 border-b border-geist-hairline flex items-center justify-between">
        <div className="flex items-center gap-2">
          <KeyRound className="w-4 h-4 text-geist-secondary" />
          <h2 className="text-xs font-semibold text-white">Vault Secrets</h2>
        </div>
        <Button variant="outline" size="sm" onClick={onRefresh} className="h-7 text-xs">
          Refresh
        </Button>
      </div>

      {secrets.length === 0 ? (
        <div className="p-8 text-center text-geist-secondary text-xs space-y-1">
          <Shield className="w-8 h-8 mx-auto text-geist-hairline" />
          <p className="text-geist-body font-medium">No secrets configured in vault.</p>
          <p className="text-geist-faint">Encrypted secrets will be available to runners.</p>
        </div>
      ) : (
        <div className="divide-y divide-geist-hairline">
          {secrets.map((sec, idx) => (
            <div key={idx} className="p-4 flex items-center justify-between">
              <div className="space-y-0.5">
                <span className="text-xs font-semibold text-white font-mono">{sec.key}</span>
                <div className="text-[11px] text-geist-faint">
                  Provider: <span className="font-mono text-geist-secondary">{sec.provider}</span>
                </div>
              </div>
              <Badge variant="outline" className="font-mono text-[10px]">
                Encrypted
              </Badge>
            </div>
          ))}
        </div>
      )}
    </div>
  );
}

function RunDetailView({
  runId,
  onBack,
}: {
  runId: string | null;
  onBack: () => void;
}) {
  return (
    <div className="space-y-4">
      <div className="flex items-center justify-between">
        <div className="flex items-center gap-3">
          <Button variant="outline" size="sm" onClick={onBack} className="h-7 text-xs">
            ← Back to History
          </Button>
          <h2 className="text-xs font-semibold text-white font-mono">
            Run ID: {runId || 'N/A'}
          </h2>
        </div>
      </div>

      <div className="rounded-geist bg-geist-surface border border-geist-hairline p-6 text-center space-y-2">
        <Terminal className="w-8 h-8 mx-auto text-geist-blue" />
        <h3 className="text-sm font-semibold text-white">Run Detail & Log Stream</h3>
        <p className="text-xs text-geist-secondary max-w-md mx-auto">
          Connected to run <span className="font-mono text-white">{runId}</span>. Live logs, steps, and telemetry stream are wired for visualization in subsequent tasks.
        </p>
      </div>
    </div>
  );
}

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
  Clock,
  AlertCircle,
  Menu,
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
import { cn, formatBytes, formatDuration, formatAppVersion } from '@/lib/utils';
import {
  DashboardView,
  HostVitalsView,
  RunnersView,
  HistoryView,
  SecretsView,
  RunDetailView,
} from '@/views';

type NavTab = 'dashboard' | 'vitals' | 'runners' | 'history' | 'secrets' | 'run-detail';

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
  const [showStopModal, setShowStopModal] = useState(false);
  const [mobileNavOpen, setMobileNavOpen] = useState(false);

  const totalRunners = runners.length || status?.total_runners || 0;
  const activeJobsCount = Math.max(
    status?.active_jobs || 0,
    runs.filter((r) => r.status === 'running' || r.status === 'in_progress').length
  );
  const versionInfo = formatAppVersion(status?.version);

  const loadStatus = useCallback(async (isManual = false) => {
    try {
      if (isManual) setStatusLoading(true);
      const s = await fetchStatus();
      setStatus(s);
      setStatusError(null);
      setLastRefreshed(new Date());
    } catch (err: unknown) {
      const msg = err instanceof Error ? err.message : 'Failed to fetch status';
      setStatusError(msg);
    } finally {
      if (isManual) setStatusLoading(false);
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

  // Poll status every 5 seconds (background poll does not flash loading spinners)
  useEffect(() => {
    loadStatus(true);
    loadRunners();
    loadRuns();
    const interval = setInterval(() => {
      loadStatus(false);
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
      {/* Mobile Backdrop */}
      {mobileNavOpen && (
        <div
          onClick={() => setMobileNavOpen(false)}
          className="fixed inset-0 bg-black/60 z-20 md:hidden backdrop-blur-sm"
        />
      )}

      {/* Sidebar Navigation */}
      <aside
        className={cn(
          'w-64 flex-shrink-0 flex flex-col justify-between border-r border-geist-hairline bg-geist-surface select-none z-30 transition-transform duration-200 ease-in-out',
          'fixed md:relative inset-y-0 left-0',
          mobileNavOpen ? 'translate-x-0' : '-translate-x-full md:translate-x-0'
        )}
      >
        {/* Brand & App Identity */}
        <div>
          <div className="h-14 flex items-center justify-between px-4 border-b border-geist-hairline">
            <div className="flex items-center gap-2.5 min-w-0">
              <div className="w-7 h-7 rounded-[6px] bg-gradient-to-b from-[#242424] to-[#121212] border border-geist-borderHover/80 flex items-center justify-center shadow-sm flex-shrink-0">
                <Terminal className="w-3.5 h-3.5 text-white" />
              </div>
              <div className="flex items-center gap-1.5 min-w-0">
                <span className="font-semibold text-sm tracking-tight text-white">actr</span>
                <span className="text-[10px] uppercase font-mono px-1.5 py-0.5 rounded bg-geist-hover text-geist-secondary border border-geist-hairline flex-shrink-0">
                  web
                </span>
              </div>
            </div>
            <Badge
              variant="outline"
              className="font-mono text-[11px] text-geist-secondary px-2 py-0.5 whitespace-nowrap flex-shrink-0"
              title={`actr version: ${versionInfo.full}`}
            >
              {versionInfo.display}
            </Badge>
          </div>

          {/* Navigation Links */}
          <nav className="p-3 space-y-1">
            <button
              onClick={() => {
                setActiveTab('dashboard');
                setMobileNavOpen(false);
              }}
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
              onClick={() => {
                setActiveTab('vitals');
                setMobileNavOpen(false);
              }}
              className={cn(
                'w-full flex items-center justify-between px-3 py-2 rounded-geist text-xs font-medium transition-colors duration-150 cursor-pointer',
                activeTab === 'vitals'
                  ? 'bg-geist-elevated text-white border border-geist-hairline shadow-sm'
                  : 'text-geist-body hover:text-white hover:bg-geist-hover border border-transparent'
              )}
            >
              <div className="flex items-center gap-2.5">
                <Activity className="w-4 h-4 text-geist-secondary" />
                <span>Host Vitals</span>
              </div>
              {status?.daemon_connected && (
                <span className="w-1.5 h-1.5 rounded-full bg-status-success-solid animate-pulse" />
              )}
            </button>

            <button
              onClick={() => {
                setActiveTab('runners');
                setMobileNavOpen(false);
              }}
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
              {totalRunners > 0 && (
                <span className="font-mono text-[10px] text-geist-faint">
                  {totalRunners}
                </span>
              )}
            </button>

            <button
              onClick={() => {
                setActiveTab('history');
                setMobileNavOpen(false);
              }}
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
              {activeJobsCount > 0 ? (
                <span className="w-2 h-2 rounded-full bg-status-warning-solid animate-pulse" />
              ) : null}
            </button>

            <button
              onClick={() => {
                setActiveTab('secrets');
                setMobileNavOpen(false);
              }}
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
                onClick={() => {
                  setActiveTab('run-detail');
                  setMobileNavOpen(false);
                }}
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
              onClick={() => {
                if (status?.daemon_connected) {
                  setShowStopModal(true);
                } else {
                  handleToggleService();
                }
              }}
              disabled={serviceActionLoading || !status}
              className="w-full text-xs h-8 gap-1.5 justify-center"
              aria-label={status?.daemon_connected ? 'Stop Daemon Service' : 'Start Daemon Service'}
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
            <button
              type="button"
              onClick={() => setMobileNavOpen(true)}
              className="md:hidden text-geist-secondary hover:text-white p-1 rounded hover:bg-geist-hover cursor-pointer"
              aria-label="Open navigation menu"
            >
              <Menu className="w-5 h-5" />
            </button>
            <h1 className="text-sm font-semibold capitalize text-white">
              {activeTab === 'run-detail'
                ? `Run Detail (${activeRunId?.slice(0, 8)})`
                : activeTab === 'vitals'
                ? 'Host Node Vitals'
                : activeTab}
            </h1>
            {(activeTab === 'dashboard' || activeTab === 'vitals') && (
              <Badge variant="outline" className="text-[11px] gap-1.5 font-mono text-status-success-solid border-status-success-border bg-status-success-bg/20">
                <span className="w-1.5 h-1.5 rounded-full bg-status-success-solid animate-pulse" />
                Live Vitals (1s)
              </Badge>
            )}
          </div>

          <div className="flex items-center gap-2">
            <Button
              variant="ghost"
              size="sm"
              onClick={() => loadStatus(true)}
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
              onNavigateTab={(tab) => setActiveTab(tab as NavTab)}
              onToggleService={handleToggleService}
              serviceActionLoading={serviceActionLoading}
              onRefresh={loadStatus}
            />
          )}

          {activeTab === 'vitals' && (
            <HostVitalsView
              status={status}
              runners={runners}
              onRefresh={loadStatus}
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
            <span>Jobs: {activeJobsCount} active</span>
            <span className="text-geist-hairline">|</span>
            <span>Refreshed: {lastRefreshed.toLocaleTimeString()}</span>
          </div>
        </footer>
      </main>

      {/* Stop Daemon Confirmation Modal */}
      {showStopModal && (
        <div className="fixed inset-0 z-50 flex items-center justify-center p-4 bg-black/80 backdrop-blur-sm animate-in fade-in duration-150">
          <div
            role="dialog"
            aria-modal="true"
            aria-labelledby="stop-daemon-title"
            className="w-full max-w-sm rounded-geist bg-geist-surface border border-status-danger-border shadow-2xl p-5 space-y-4 animate-in zoom-in-95 duration-150"
            onClick={(e) => e.stopPropagation()}
          >
            <div className="flex items-center gap-2.5 text-status-danger-text">
              <AlertCircle className="w-5 h-5 flex-shrink-0" />
              <h3 id="stop-daemon-title" className="text-sm font-semibold text-white">
                Stop actr Daemon?
              </h3>
            </div>
            <p className="text-xs text-geist-secondary leading-relaxed">
              This will stop the local actr daemon service. Any active runner jobs and live log streams will be interrupted.
            </p>
            <div className="flex items-center justify-end gap-2 pt-2 border-t border-geist-hairline">
              <Button
                variant="outline"
                size="sm"
                onClick={() => setShowStopModal(false)}
                className="h-8 text-xs px-3"
              >
                Cancel
              </Button>
              <Button
                variant="destructive"
                size="sm"
                onClick={() => {
                  setShowStopModal(false);
                  handleToggleService();
                }}
                disabled={serviceActionLoading}
                className="h-8 text-xs px-3 gap-1.5"
              >
                <Power className="w-3.5 h-3.5" />
                <span>Stop Service</span>
              </Button>
            </div>
          </div>
        </div>
      )}
    </div>
  );
}

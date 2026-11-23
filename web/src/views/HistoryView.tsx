import { useState, useEffect, useCallback } from 'react';
import {
  History,
  Search,
  RotateCcw,
  CheckCircle2,
  XCircle,
  Activity,
  Clock,
  ChevronRight,
  ChevronLeft,
  Boxes,
  GitBranch,
} from 'lucide-react';
import { fetchRuns, type RunRecord, type RunsQueryParams } from '@/lib/api';
import { Badge } from '@/components/ui/badge';
import { Button } from '@/components/ui/button';
import { cn, formatDuration, formatRelativeTime } from '@/lib/utils';

export interface HistoryViewProps {
  runs?: RunRecord[];
  onSelectRun: (id: string) => void;
  onRefresh?: () => void;
}

const PAGE_SIZE = 25;

export function HistoryView({
  runs: initialRuns = [],
  onSelectRun,
  onRefresh,
}: HistoryViewProps) {
  const [runsList, setRunsList] = useState<RunRecord[]>(initialRuns);
  const [totalCount, setTotalCount] = useState<number>(initialRuns.length);
  const [offset, setOffset] = useState<number>(0);
  const [loading, setLoading] = useState<boolean>(false);

  // Filters
  const [searchQuery, setSearchQuery] = useState<string>('');
  const [debouncedQuery, setDebouncedQuery] = useState<string>('');
  const [repoFilter, setRepoFilter] = useState<string>('');
  const [debouncedRepo, setDebouncedRepo] = useState<string>('');
  const [statusFilter, setStatusFilter] = useState<string>('all');

  // Debounce search query changes by 300ms
  useEffect(() => {
    const timer = setTimeout(() => {
      setDebouncedQuery(searchQuery);
      setOffset(0);
    }, 300);
    return () => clearTimeout(timer);
  }, [searchQuery]);

  // Debounce repo filter changes by 300ms
  useEffect(() => {
    const timer = setTimeout(() => {
      setDebouncedRepo(repoFilter);
      setOffset(0);
    }, 300);
    return () => clearTimeout(timer);
  }, [repoFilter]);

  const loadData = useCallback(async () => {
    try {
      setLoading(true);
      const params: RunsQueryParams = {
        limit: PAGE_SIZE,
        offset,
      };
      if (debouncedQuery.trim()) params.q = debouncedQuery.trim();
      if (statusFilter !== 'all') params.status = statusFilter;
      if (debouncedRepo.trim()) params.repo = debouncedRepo.trim();

      const res = await fetchRuns(params);
      setRunsList(res.runs || []);
      setTotalCount(res.total || 0);
    } catch (err) {
      console.warn('Failed to load runs history:', err);
    } finally {
      setLoading(false);
    }
  }, [offset, debouncedQuery, statusFilter, debouncedRepo]);

  useEffect(() => {
    loadData();
  }, [loadData]);

  const handleSearchSubmit = (e: React.FormEvent) => {
    e.preventDefault();
    setDebouncedQuery(searchQuery);
    setDebouncedRepo(repoFilter);
    setOffset(0);
  };

  const handleRefresh = () => {
    onRefresh?.();
    loadData();
  };

  const totalPages = Math.ceil(totalCount / PAGE_SIZE) || 1;
  const currentPage = Math.floor(offset / PAGE_SIZE) + 1;

  return (
    <div className="space-y-5">
      {/* Search & Filter Header Card */}
      <div className="rounded-geist bg-geist-surface border border-geist-hairline p-4 space-y-3">
        <div className="flex flex-col sm:flex-row items-start sm:items-center justify-between gap-3">
          <div className="flex items-center gap-2.5">
            <History className="w-5 h-5 text-geist-blue" />
            <h2 className="text-base font-semibold text-white tracking-tight">
              Execution History
            </h2>
            <Badge variant="outline" className="font-mono text-xs">
              {totalCount} total
            </Badge>
          </div>

          <Button
            variant="outline"
            size="sm"
            onClick={handleRefresh}
            disabled={loading}
            className="h-8 gap-1.5 text-xs text-geist-secondary hover:text-white"
          >
            <RotateCcw className={cn('w-3.5 h-3.5', loading && 'animate-spin')} />
            <span>Refresh</span>
          </Button>
        </div>

        {/* Filter Toolbar */}
        <form
          onSubmit={handleSearchSubmit}
          className="grid grid-cols-1 sm:grid-cols-12 gap-3 pt-2 border-t border-geist-hairline/60"
        >
          {/* Query Search Input */}
          <div className="sm:col-span-5 relative">
            <Search className="w-3.5 h-3.5 text-geist-faint absolute left-3 top-1/2 -translate-y-1/2" />
            <input
              type="text"
              value={searchQuery}
              onChange={(e) => setSearchQuery(e.target.value)}
              placeholder="Search by workflow name, run ID, or commit..."
              className="w-full h-8 pl-8 pr-3 rounded-geist bg-geist-elevated border border-geist-hairline text-xs text-white placeholder-geist-faint focus:outline-none focus:border-geist-blue transition-colors font-mono"
            />
          </div>

          {/* Status Dropdown */}
          <div className="sm:col-span-3">
            <select
              value={statusFilter}
              onChange={(e) => {
                setStatusFilter(e.target.value);
                setOffset(0);
              }}
              className="w-full h-8 px-2.5 rounded-geist bg-geist-elevated border border-geist-hairline text-xs text-white focus:outline-none focus:border-geist-blue transition-colors font-mono cursor-pointer"
            >
              <option value="all">Status: All</option>
              <option value="completed">Status: Completed / Success</option>
              <option value="failed">Status: Failed</option>
              <option value="running">Status: Running / Active</option>
              <option value="interrupted">Status: Interrupted</option>
            </select>
          </div>

          {/* Repo Input */}
          <div className="sm:col-span-3 relative">
            <GitBranch className="w-3.5 h-3.5 text-geist-faint absolute left-3 top-1/2 -translate-y-1/2" />
            <input
              type="text"
              value={repoFilter}
              onChange={(e) => setRepoFilter(e.target.value)}
              placeholder="Filter by repo..."
              className="w-full h-8 pl-8 pr-3 rounded-geist bg-geist-elevated border border-geist-hairline text-xs text-white placeholder-geist-faint focus:outline-none focus:border-geist-blue transition-colors font-mono"
            />
          </div>

          {/* Filter Submit */}
          <div className="sm:col-span-1">
            <Button type="submit" variant="default" size="sm" className="w-full h-8 text-xs px-2">
              Filter
            </Button>
          </div>
        </form>
      </div>

      {/* Runs Table */}
      <div className="rounded-geist bg-geist-surface border border-geist-hairline overflow-hidden">
        {runsList.length === 0 ? (
          <div className="p-12 text-center text-geist-secondary text-xs space-y-2">
            <Boxes className="w-10 h-10 mx-auto text-geist-hairline" />
            <p className="text-sm font-semibold text-white">No Execution Records Found</p>
            <p className="text-geist-faint max-w-sm mx-auto">
              No workflow runs matched the filter criteria. Try clearing search filters or trigger a workflow run.
            </p>
          </div>
        ) : (
          <div className="overflow-x-auto">
            <table className="w-full text-left border-collapse text-xs">
              <thead>
                <tr className="border-b border-geist-hairline bg-geist-elevated/40 text-[11px] text-geist-faint font-mono uppercase tracking-wider">
                  <th className="py-2.5 px-4 font-medium">Status</th>
                  <th className="py-2.5 px-4 font-medium">Run ID</th>
                  <th className="py-2.5 px-4 font-medium">Workflow</th>
                  <th className="py-2.5 px-4 font-medium">Repository</th>
                  <th className="py-2.5 px-4 font-medium">Driver</th>
                  <th className="py-2.5 px-4 font-medium">Duration</th>
                  <th className="py-2.5 px-4 font-medium">Started At</th>
                  <th className="py-2.5 px-4 font-medium text-right">Details</th>
                </tr>
              </thead>
              <tbody className="divide-y divide-geist-hairline font-mono">
                {runsList.map((r) => {
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

        {/* Pagination Bar */}
        <div className="px-4 py-3 border-t border-geist-hairline bg-geist-surface flex items-center justify-between text-xs font-mono text-geist-secondary">
          <div>
            Showing <span className="text-white font-medium">{totalCount > 0 ? offset + 1 : 0}</span> to{' '}
            <span className="text-white font-medium">
              {Math.min(offset + PAGE_SIZE, totalCount)}
            </span>{' '}
            of <span className="text-white font-medium">{totalCount}</span> runs
          </div>

          <div className="flex items-center gap-2">
            <span className="text-geist-faint text-[11px]">
              Page {currentPage} of {totalPages}
            </span>

            <div className="flex items-center gap-1">
              <Button
                variant="outline"
                size="sm"
                onClick={() => setOffset((prev) => Math.max(prev - PAGE_SIZE, 0))}
                disabled={offset === 0 || loading}
                className="h-7 px-2 text-xs"
              >
                <ChevronLeft className="w-3.5 h-3.5" />
                <span className="sr-only">Previous</span>
              </Button>

              <Button
                variant="outline"
                size="sm"
                onClick={() => setOffset((prev) => prev + PAGE_SIZE)}
                disabled={offset + PAGE_SIZE >= totalCount || loading}
                className="h-7 px-2 text-xs"
              >
                <ChevronRight className="w-3.5 h-3.5" />
                <span className="sr-only">Next</span>
              </Button>
            </div>
          </div>
        </div>
      </div>
    </div>
  );
}

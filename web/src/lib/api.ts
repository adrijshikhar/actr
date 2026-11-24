// API Client for actr embedded web dashboard

export class ApiError extends Error {
  constructor(
    public status: number,
    message: string
  ) {
    super(message);
    this.name = 'ApiError';
  }
}

async function request<T>(endpoint: string, options?: RequestInit): Promise<T> {
  const url = endpoint.startsWith('/') ? endpoint : `/${endpoint}`;
  const res = await fetch(url, {
    ...options,
    headers: {
      'Content-Type': 'application/json',
      'X-Actr-Web': '1',
      ...options?.headers,
    },
  });

  if (!res.ok) {
    let errMsg = `Request failed with status ${res.status}`;
    try {
      const errBody = await res.json();
      if (errBody && errBody.error) {
        errMsg = errBody.error;
      }
    } catch {
      // Body not JSON
    }
    throw new ApiError(res.status, errMsg);
  }

  return res.json() as Promise<T>;
}

export interface StatusResponse {
  version: string;
  daemon_connected: boolean;
  active_jobs: number;
  queued_jobs?: number;
  total_runners: number;
  host_cpu: number;
  host_mem_used: number;
  host_mem_total: number;
  uptime_seconds: number;
}

export interface RunnerInfo {
  repo: string;
  name: string;
  labels: string[];
  max_concurrency: number;
  active_slots: number;
  queued_jobs?: number;
  status: string;
}

export interface StepMetadata {
  id: string;
  number: number;
  name: string;
  status: string;
  exit_code: number;
  oom_killed: boolean;
  started_at: string;
  ended_at: string;
  duration_sec: number;
  peak_rss_bytes: number;
  log_bytes: number;
  log_lines: number;
}

export interface RunMetricsSummary {
  peak_cpu_percent: number;
  peak_rss_bytes: number;
  total_net_rx_bytes: number;
  total_net_tx_bytes: number;
  total_disk_written_bytes: number;
}

export interface RunRecord {
  id: string;
  name: string;
  workflow: string;
  repo: string;
  repo_alias?: string;
  repo_url?: string;
  owner: string;
  status: string;
  started_at: string;
  ended_at: string;
  duration_sec: number;
  driver: string;
  metrics_summary?: RunMetricsSummary;
  steps?: StepMetadata[];
}

export interface RunsListResponse {
  runs: RunRecord[];
  total: number;
  limit: number;
  offset: number;
}

export interface RunsQueryParams {
  limit?: number;
  offset?: number;
  repo?: string;
  status?: string;
  q?: string;
}

export interface LogsResponse {
  logs: string[];
}

export interface TelemetrySample {
  timestamp: string;
  pid: number;
  pgid: number;
  process_count: number;
  step_cpu: number;
  step_rss_bytes: number;
  step_working_set_bytes?: number;
  step_elapsed_sec: number;
  active_step_name?: string;
  thread_count?: number;
  cpu_throttled_sec?: number;
  disk_used_bytes: number;
  disk_free_bytes: number;
  disk_total_bytes: number;
  disk_used_ratio: number;
  disk_write_rate: number;
  step_disk_read_rate?: number;
  disk_inodes_free: number;
  disk_written_bytes?: number;
  net_rx_rate: number;
  net_tx_rate: number;
  net_rx_bytes?: number;
  net_tx_bytes?: number;
  host_cpu: number;
  host_mem_used: number;
  host_mem_total: number;
  host_load_1m: number;
  trace_id?: string;
  span_id?: string;
}

export type RunnerMetrics = TelemetrySample;

export interface SecretMetadata {
  key: string;
  provider: string;
  scope?: string;
  updatedAt?: string;
}

export interface SecretMutationResponse {
  status: string;
  key: string;
}

export interface ServiceStatusResponse {
  status: string;
}

export interface ServiceActionResponse {
  status: string;
}

// REST API calls

export async function fetchStatus(): Promise<StatusResponse> {
  return request<StatusResponse>('/api/status');
}

export async function fetchRunners(): Promise<RunnerInfo[]> {
  return request<RunnerInfo[]>('/api/runners');
}

export async function fetchRuns(params?: RunsQueryParams): Promise<RunsListResponse> {
  const query = new URLSearchParams();
  if (params?.limit !== undefined) query.set('limit', String(params.limit));
  if (params?.offset !== undefined) query.set('offset', String(params.offset));
  if (params?.repo) query.set('repo', params.repo);
  if (params?.status) query.set('status', params.status);
  if (params?.q) query.set('q', params.q);

  const qs = query.toString();
  return request<RunsListResponse>(`/api/runs${qs ? `?${qs}` : ''}`);
}

export async function fetchRun(id: string): Promise<RunRecord> {
  return request<RunRecord>(`/api/runs/${encodeURIComponent(id)}`);
}

export async function fetchLogs(id: string, stepId?: string): Promise<LogsResponse> {
  const query = stepId ? `?step_id=${encodeURIComponent(stepId)}` : '';
  return request<LogsResponse>(`/api/runs/${encodeURIComponent(id)}/logs${query}`);
}

export async function fetchRunMetrics(id: string): Promise<TelemetrySample[]> {
  return request<TelemetrySample[]>(`/api/runs/${encodeURIComponent(id)}/metrics`);
}

export async function fetchCurrentMetrics(): Promise<RunnerMetrics> {
  return request<RunnerMetrics>('/api/metrics/current');
}

export async function fetchSecrets(): Promise<SecretMetadata[]> {
  return request<SecretMetadata[]>('/api/secrets');
}

export async function createSecret(
  key: string,
  value: string,
  scope?: string
): Promise<SecretMutationResponse> {
  return request<SecretMutationResponse>('/api/secrets', {
    method: 'POST',
    body: JSON.stringify({ key, value, scope: scope || '' }),
  });
}

export async function deleteSecret(key: string): Promise<SecretMutationResponse> {
  return request<SecretMutationResponse>(`/api/secrets/${encodeURIComponent(key)}`, {
    method: 'DELETE',
  });
}

export async function fetchServiceStatus(): Promise<ServiceStatusResponse> {
  return request<ServiceStatusResponse>('/api/service');
}

export async function startService(): Promise<ServiceActionResponse> {
  return request<ServiceActionResponse>('/api/service/start', {
    method: 'POST',
  });
}

export async function stopService(): Promise<ServiceActionResponse> {
  return request<ServiceActionResponse>('/api/service/stop', {
    method: 'POST',
  });
}

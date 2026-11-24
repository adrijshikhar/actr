// Typed Server-Sent Events (SSE) Manager for actr web dashboard
import type { RunnerMetrics, StepMetadata, RunRecord } from './api';

export interface SSELogEvent {
  type: string;
  step_id?: string;
  line?: string;
  log_line?: string;
  job_id?: string;
  repo_alias?: string;
  step?: StepMetadata | any;
  job?: RunRecord | any;
  [key: string]: any;
}

export interface SSESubscriptionOptions {
  onOpen?: () => void;
  onError?: (err: Event) => void;
  reconnectIntervalMs?: number;
  maxReconnectIntervalMs?: number;
}

/**
 * Subscribes to real-time logs and step events for a specific run.
 * Automatically handles reconnection and cleanup.
 */
export function subscribeLogs(
  runId: string,
  onEvent: (event: SSELogEvent) => void,
  options?: SSESubscriptionOptions
): () => void {
  let isClosed = false;
  let eventSource: EventSource | null = null;
  let retryTimer: ReturnType<typeof setTimeout> | null = null;
  let backoffMs = options?.reconnectIntervalMs || 1000;
  const maxBackoffMs = options?.maxReconnectIntervalMs || 8000;

  const url = `/api/runs/${encodeURIComponent(runId)}/logs/stream`;

  const handleMessage = (e: MessageEvent) => {
    try {
      const data = JSON.parse(e.data) as SSELogEvent;
      data.type = data.type || e.type;
      if (data.line && !data.log_line) {
        data.log_line = data.line;
      }
      if (!data.line && data.log_line) {
        data.line = data.log_line;
      }
      if (e.type.startsWith('step_') && !data.step) {
        data.step = { ...data };
      }
      if ((e.type.startsWith('job_') || e.type.startsWith('run_')) && !data.job) {
        data.job = { ...data };
      }
      onEvent(data);
    } catch (err) {
      console.warn('Failed to parse SSE log data:', e.data, err);
    }
  };

  const connect = () => {
    if (isClosed) return;

    try {
      eventSource = new EventSource(url);

      eventSource.onopen = () => {
        backoffMs = options?.reconnectIntervalMs || 1000;
        options?.onOpen?.();
      };

      // Standard message listener
      eventSource.onmessage = handleMessage;

      // Custom event types sent by actr monitor
      const customEvents = [
        'log',
        'step_start',
        'step_complete',
        'job_start',
        'job_complete',
        'step_started',
        'step_finished',
        'run_started',
        'run_finished',
      ];
      for (const evtName of customEvents) {
        eventSource.addEventListener(evtName, handleMessage as EventListener);
      }

      eventSource.onerror = (err) => {
        options?.onError?.(err);
        if (eventSource) {
          eventSource.close();
          eventSource = null;
        }

        if (!isClosed) {
          retryTimer = setTimeout(() => {
            backoffMs = Math.min(backoffMs * 1.5, maxBackoffMs);
            connect();
          }, backoffMs);
        }
      };
    } catch (e) {
      console.error('Failed to establish EventSource connection to', url, e);
      if (!isClosed) {
        retryTimer = setTimeout(connect, backoffMs);
      }
    }
  };

  connect();

  return () => {
    isClosed = true;
    if (retryTimer) {
      clearTimeout(retryTimer);
      retryTimer = null;
    }
    if (eventSource) {
      eventSource.close();
      eventSource = null;
    }
  };
}

/**
 * Subscribes to live host and runner metrics snapshots streamed every 1s.
 * Automatically handles reconnection and cleanup.
 */
export function subscribeMetrics(
  onMetrics: (metrics: RunnerMetrics) => void,
  options?: SSESubscriptionOptions
): () => void {
  let isClosed = false;
  let eventSource: EventSource | null = null;
  let retryTimer: ReturnType<typeof setTimeout> | null = null;
  let backoffMs = options?.reconnectIntervalMs || 1000;
  const maxBackoffMs = options?.maxReconnectIntervalMs || 8000;

  const url = '/api/metrics/stream';

  const handleData = (dataStr: string) => {
    try {
      const data = JSON.parse(dataStr) as RunnerMetrics;
      onMetrics(data);
    } catch (err) {
      console.warn('Failed to parse SSE metrics data:', dataStr, err);
    }
  };

  const connect = () => {
    if (isClosed) return;

    try {
      eventSource = new EventSource(url);

      eventSource.onopen = () => {
        backoffMs = options?.reconnectIntervalMs || 1000;
        options?.onOpen?.();
      };

      eventSource.onmessage = (e: MessageEvent) => {
        handleData(e.data);
      };

      eventSource.addEventListener('metrics', (e: Event) => {
        const msgEvent = e as MessageEvent;
        handleData(msgEvent.data);
      });

      eventSource.onerror = (err) => {
        options?.onError?.(err);
        if (eventSource) {
          eventSource.close();
          eventSource = null;
        }

        if (!isClosed) {
          retryTimer = setTimeout(() => {
            backoffMs = Math.min(backoffMs * 1.5, maxBackoffMs);
            connect();
          }, backoffMs);
        }
      };
    } catch (e) {
      console.error('Failed to establish EventSource connection to', url, e);
      if (!isClosed) {
        retryTimer = setTimeout(connect, backoffMs);
      }
    }
  };

  connect();

  return () => {
    isClosed = true;
    if (retryTimer) {
      clearTimeout(retryTimer);
      retryTimer = null;
    }
    if (eventSource) {
      eventSource.close();
      eventSource = null;
    }
  };
}

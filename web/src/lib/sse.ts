// Typed Server-Sent Events (SSE) Manager for actr web dashboard
import type { RunnerMetrics } from './api';

export interface SSELogEvent {
  type: string;
  job_id?: string;
  repo_alias?: string;
  step_id?: string;
  log_line?: string;
  step?: {
    id: string;
    number: number;
    name: string;
    status: string;
  };
  job?: {
    id: string;
    name: string;
    workflow: string;
  };
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

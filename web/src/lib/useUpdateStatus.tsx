import { useCallback, useEffect, useState } from 'react';
import { api, apiPost } from '../api';

export interface UpdateStatus {
  state: 'idle' | 'checking' | 'available' | 'downloading' | 'ready' | 'failed';
  version: string;
  current: string;
  error?: string;
}

const POLL_MS = 30_000;

/**
 * Mirrors the desktop tray's app-update state into the UI. Server-sent events
 * provide immediate updates, with GET polling as a recovery fallback. `install`
 * triggers the same download-and-install flow as the tray item.
 */
export function useUpdateStatus() {
  const [status, setStatus] = useState<UpdateStatus | null>(null);

  useEffect(() => {
    let cancelled = false;
    let eventVersion = 0;
    const poll = () => {
      const requestEventVersion = eventVersion;
      return api('/update/status')
        .then((s: UpdateStatus) => {
          if (!cancelled && eventVersion === requestEventVersion) setStatus(s);
        })
        .catch(() => {});
    };

    const source = new EventSource('/admin/update/events');
    source.onmessage = (event) => {
      try {
        const next = JSON.parse(event.data) as UpdateStatus;
        eventVersion++;
        if (!cancelled) setStatus(next);
      } catch {
        // Ignore malformed events and keep the stream open.
      }
    };

    poll();
    const interval = setInterval(poll, POLL_MS);
    return () => {
      cancelled = true;
      source.close();
      clearInterval(interval);
    };
  }, []);

  const install = useCallback(async () => {
    await apiPost('/update/status');
  }, []);

  return { status, install };
}

export const DownloadIcon = ({ className = 'w-4 h-4' }: { className?: string }) => (
  <svg className={className} viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round">
    <path d="M21 15v4a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2v-4" />
    <polyline points="7 10 12 15 17 10" />
    <line x1="12" y1="15" x2="12" y2="3" />
  </svg>
);

import { useEffect, useState, useRef, useCallback } from 'react';
import { api, apiPost, apiPut } from '../api';
import { toast } from './ui/toast';
import { Button } from '@/components/ui/button';
import { Switch } from '@/components/ui/switch';

export default function ProxyPanel() {
  const [running, setRunning] = useState<boolean | null>(null);
  const [autoStart, setAutoStart] = useState(false);
  const [debugLogs, setDebugLogs] = useState(false);
  const [analyticsOptIn, setAnalyticsOptIn] = useState(false);
  const [autoStartLabel, setAutoStartLabel] = useState('Auto-start at Login');
  const [logs, setLogs] = useState('Loading...');
  const [actionInProgress, setActionInProgress] = useState(false);
  const logRef = useRef<HTMLDivElement>(null);

  const updateStatus = useCallback(async () => {
    try {
      const s = await api('/status');
      setRunning(s.running);
    } catch {
      // ignore
    }
  }, []);

  const refreshLogs = useCallback(async () => {
    try {
      const data = await api('/logs');
      setLogs(data.logs || '(empty)');
    } catch {
      setLogs('Failed to load logs');
    }
  }, []);

  const loadAutoStart = useCallback(async () => {
    try {
      const data = await api('/autostart');
      setAutoStart(data.enabled);
      const isMac = /Mac|iPhone|iPad/.test(navigator.userAgent);
      setAutoStartLabel(isMac ? 'Auto-start at Login' : 'Auto-start with Windows');
    } catch {
      // ignore
    }
  }, []);

  const loadDebugLogs = useCallback(async () => {
    try {
      const data = await api('/debug-logs');
      setDebugLogs(data.enabled);
    } catch {
      // ignore
    }
  }, []);

  const loadAnalytics = useCallback(async () => {
    try {
      const data = await api('/analytics/settings');
      setAnalyticsOptIn(data.opt_in);
    } catch {
      // ignore
    }
  }, []);

  useEffect(() => {
    updateStatus();
    loadAutoStart();
    loadDebugLogs();
    loadAnalytics();
  }, [updateStatus, loadAutoStart, loadDebugLogs, loadAnalytics]);

  useEffect(() => {
    refreshLogs();
  }, [refreshLogs]);

  useEffect(() => {
    if (logRef.current) {
      logRef.current.scrollTop = logRef.current.scrollHeight;
    }
  }, [logs]);

  const handleStart = async () => {
    setActionInProgress(true);
    try {
      await apiPost('/proxy/start');
      toast.add({ title: 'Proxy starting...' });
      setTimeout(() => { updateStatus(); setActionInProgress(false); }, 1500);
    } catch (e) {
      toast.add({ title: 'Failed to start proxy: ' + (e as Error).message, type: 'error' });
      setActionInProgress(false);
    }
  };

  const handleStop = async () => {
    setActionInProgress(true);
    try {
      await apiPost('/proxy/stop');
      toast.add({ title: 'Proxy stopping...' });
      setTimeout(() => { updateStatus(); setActionInProgress(false); }, 1000);
    } catch (e) {
      toast.add({ title: 'Failed to stop proxy: ' + (e as Error).message, type: 'error' });
      setActionInProgress(false);
    }
  };

  const handleRestart = async () => {
    setActionInProgress(true);
    try {
      await apiPost('/proxy/restart');
      toast.add({ title: 'Proxy restarting...' });
      setTimeout(() => { updateStatus(); setActionInProgress(false); }, 2000);
    } catch (e) {
      toast.add({ title: 'Failed to restart proxy: ' + (e as Error).message, type: 'error' });
      setActionInProgress(false);
    }
  };

  const handleToggleAutoStart = async (enabled: boolean) => {
    try {
      await apiPut('/autostart', { enabled });
      setAutoStart(enabled);
      toast.add({ title: enabled ? 'Auto-start enabled' : 'Auto-start disabled', type: 'success' });
    } catch (e) {
      setAutoStart(!enabled);
      toast.add({ title: 'Failed to update auto-start: ' + (e as Error).message, type: 'error' });
    }
  };

  const handleToggleDebugLogs = async (enabled: boolean) => {
    try {
      await apiPut('/debug-logs', { enabled });
      setDebugLogs(enabled);
      toast.add({ title: enabled ? 'Debug logs enabled' : 'Debug logs disabled', type: 'success' });
    } catch (e) {
      setDebugLogs(!enabled);
      toast.add({ title: 'Failed to update debug logs: ' + (e as Error).message, type: 'error' });
    }
  };

  const handleToggleAnalytics = async (enabled: boolean) => {
    try {
      await apiPut('/analytics/settings', { opt_in: enabled, prompted: true });
      setAnalyticsOptIn(enabled);
      toast.add({ title: enabled ? 'Anonymous analytics enabled' : 'Anonymous analytics disabled', type: 'success' });
    } catch (e) {
      setAnalyticsOptIn(!enabled);
      toast.add({ title: 'Failed to update analytics: ' + (e as Error).message, type: 'error' });
    }
  };

  return (
    <>
      <div className="rounded-xl border border-border bg-card p-6 mb-4">
        <h3 className="text-sm font-semibold tracking-tight mb-4">Start at Login</h3>
        <div className="flex items-center justify-between gap-3">
          <div>
            <div className="text-sm font-medium text-foreground">{autoStartLabel}</div>
            <div className="text-xs text-muted-foreground mt-0.5">Launch Prism automatically when you log in</div>
          </div>
          <Switch
            checked={autoStart}
            onCheckedChange={(checked) => handleToggleAutoStart(checked)}
          />
        </div>
      </div>

      <div className="rounded-xl border border-border bg-card p-6 mb-4">
        <h3 className="text-sm font-semibold tracking-tight mb-4">Debug Logs</h3>
        <div className="flex items-center justify-between gap-3">
          <div>
            <div className="text-sm font-medium text-foreground">Log request &amp; response bodies</div>
            <div className="text-xs text-muted-foreground mt-0.5">Dump each translated request and response to the debug directory</div>
          </div>
          <Switch
            checked={debugLogs}
            onCheckedChange={(checked) => handleToggleDebugLogs(checked)}
          />
        </div>
      </div>

      <div className="rounded-xl border border-border bg-card p-6 mb-4">
        <h3 className="text-sm font-semibold tracking-tight mb-4">Anonymous Analytics</h3>
        <div className="flex items-center justify-between gap-3">
          <div>
            <div className="text-sm font-medium text-foreground">Send anonymous usage data</div>
            <div className="text-xs text-muted-foreground mt-0.5">
              Sends an anonymous daily ping (app version + OS) to count active installs. No prompts,
              models, or requests are ever sent.{' '}
              <a
                href="https://github.com/KavinMK05/Prism/blob/main/TELEMETRY.md"
                target="_blank"
                rel="noopener noreferrer"
                className="underline underline-offset-2 hover:text-foreground"
              >
                Learn what&apos;s sent
              </a>
            </div>
          </div>
          <Switch
            checked={analyticsOptIn}
            onCheckedChange={(checked) => handleToggleAnalytics(checked)}
          />
        </div>
      </div>

      <div className="rounded-xl border border-border bg-card p-6 mb-4">
        <h3 className="text-sm font-semibold tracking-tight mb-4">Proxy Control</h3>
        <div className="flex gap-2.5 flex-wrap">
          <Button disabled={running === true || actionInProgress} onClick={handleStart}>Start</Button>
          <Button variant="destructive" disabled={running === false || actionInProgress} onClick={handleStop}>Stop</Button>
          <Button variant="outline" disabled={running === false || actionInProgress} onClick={handleRestart}>Restart</Button>
        </div>
      </div>

      <div className="rounded-xl border border-border bg-card p-6 mb-4">
        <h3 className="text-sm font-semibold tracking-tight mb-4">Logs</h3>
        <div className="bg-muted border border-border rounded-md p-3.5 max-h-[320px] overflow-y-auto font-mono text-xs leading-relaxed text-muted-foreground whitespace-pre-wrap break-words" ref={logRef}>{logs}</div>
        <div className="flex gap-2.5 mt-5 flex-wrap">
          <Button variant="outline" onClick={refreshLogs}>Refresh</Button>
        </div>
      </div>
    </>
  );
}

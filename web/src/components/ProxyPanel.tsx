import { useEffect, useState, useRef, useCallback } from 'react';
import { api, apiPost, apiPut } from '../api';
import { useToast } from '../ToastContext';
import { Button } from '@/components/ui/button';
import { Switch } from '@/components/ui/switch';

export default function ProxyPanel() {
  const { toast } = useToast();
  const [running, setRunning] = useState<boolean | null>(null);
  const [autoStart, setAutoStart] = useState(false);
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

  useEffect(() => {
    updateStatus();
    loadAutoStart();
  }, [updateStatus, loadAutoStart]);

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
      toast('Proxy starting...');
      setTimeout(() => { updateStatus(); setActionInProgress(false); }, 1500);
    } catch (e) {
      toast('Failed to start proxy: ' + (e as Error).message, 'error');
      setActionInProgress(false);
    }
  };

  const handleStop = async () => {
    setActionInProgress(true);
    try {
      await apiPost('/proxy/stop');
      toast('Proxy stopping...');
      setTimeout(() => { updateStatus(); setActionInProgress(false); }, 1000);
    } catch (e) {
      toast('Failed to stop proxy: ' + (e as Error).message, 'error');
      setActionInProgress(false);
    }
  };

  const handleRestart = async () => {
    setActionInProgress(true);
    try {
      await apiPost('/proxy/restart');
      toast('Proxy restarting...');
      setTimeout(() => { updateStatus(); setActionInProgress(false); }, 2000);
    } catch (e) {
      toast('Failed to restart proxy: ' + (e as Error).message, 'error');
      setActionInProgress(false);
    }
  };

  const handleToggleAutoStart = async (enabled: boolean) => {
    try {
      await apiPut('/autostart', { enabled });
      setAutoStart(enabled);
      toast(enabled ? 'Auto-start enabled' : 'Auto-start disabled');
    } catch (e) {
      setAutoStart(!enabled);
      toast('Failed to update auto-start: ' + (e as Error).message, 'error');
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

import { useEffect, useState, useRef, useCallback } from 'react';
import { api, apiPost, apiPut } from '../api';
import { useToast } from '../ToastContext';

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

  // Auto-scroll logs to bottom
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
      <div className="card">
        <h3>Start at Login</h3>
        <div className="toggle-row">
          <div>
            <div className="toggle-label">{autoStartLabel}</div>
            <div className="toggle-desc">Launch Prism automatically when you log in</div>
          </div>
          <label className="toggle-switch">
            <input
              type="checkbox"
              checked={autoStart}
              onChange={(e) => handleToggleAutoStart(e.target.checked)}
            />
            <span className="toggle-slider" />
          </label>
        </div>
      </div>

      <div className="card">
        <h3>Proxy Control</h3>
        <div className="btn-row" style={{ marginTop: 0 }}>
          <button
            className="btn btn-primary"
            disabled={running === true || actionInProgress}
            onClick={handleStart}
          >
            Start
          </button>
          <button
            className="btn btn-danger"
            disabled={running === false || actionInProgress}
            onClick={handleStop}
          >
            Stop
          </button>
          <button
            className="btn btn-ghost"
            disabled={running === false || actionInProgress}
            onClick={handleRestart}
          >
            Restart
          </button>
        </div>
      </div>

      <div className="card">
        <h3>Logs</h3>
        <div className="log-view" ref={logRef}>{logs}</div>
        <div className="btn-row">
          <button className="btn btn-ghost" onClick={refreshLogs}>Refresh</button>
        </div>
      </div>
    </>
  );
}

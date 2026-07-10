import { useEffect, useState, useCallback, useRef } from 'react';
import { api, apiPost, apiPut } from '../api';
import { useToast } from '../ToastContext';

const PHASE_LABELS: Record<string, string> = {
  'downloading-python': 'Downloading Python',
  'extracting-python': 'Extracting Python',
  'creating-venv': 'Creating virtual environment',
  'downloading-searxng': 'Downloading SearXNG source',
  'extracting-searxng': 'Extracting SearXNG source',
  'installing-searxng': 'Installing SearXNG dependencies',
};

export default function SearXNGPanel() {
  const { toast } = useToast();
  const [status, setStatus] = useState<any>(null);
  const [settings, setSettings] = useState<any>(null);
  const [settingsError, setSettingsError] = useState('');
  const intervalRef = useRef<ReturnType<typeof setInterval> | null>(null);

  const refreshStatus = useCallback(async () => {
    try { setStatus(await api('/searxng/status')); } catch { /* ignore */ }
  }, []);

  const loadSettings = useCallback(async () => {
    try { setSettings(await api('/searxng/settings')); setSettingsError(''); }
    catch { setSettingsError('Install SearXNG first to edit settings.'); }
  }, []);

  useEffect(() => {
    refreshStatus();
    loadSettings();
    intervalRef.current = setInterval(refreshStatus, 2000);
    return () => { if (intervalRef.current) clearInterval(intervalRef.current); };
  }, [refreshStatus, loadSettings]);

  const handleStart = async () => {
    try { await apiPost('/searxng/start'); toast('SearXNG installing/starting\u2026'); }
    catch (e) { toast('Failed: ' + (e as Error).message, 'error'); }
  };
  const handleStop = async () => {
    try { await apiPost('/searxng/stop'); toast('SearXNG stopping\u2026'); setTimeout(refreshStatus, 1000); }
    catch (e) { toast('Failed: ' + (e as Error).message, 'error'); }
  };
  const handleRestart = async () => {
    try { await apiPost('/searxng/restart'); toast('SearXNG restarting\u2026'); setTimeout(refreshStatus, 2000); }
    catch (e) { toast('Failed: ' + (e as Error).message, 'error'); }
  };

  const handleAutostart = async (enabled: boolean) => {
    try { await apiPut('/searxng/autostart', { enabled }); toast(enabled ? 'SearXNG will auto-start on Prism launch.' : 'SearXNG auto-start disabled.'); setTimeout(refreshStatus, 300); }
    catch (e) { toast('Failed: ' + (e as Error).message, 'error'); refreshStatus(); }
  };

  const saveSettings = async () => {
    if (!settings) return;
    const formats: string[] = [];
    if (settings._fmtHtml) formats.push('html');
    if (settings._fmtJson) formats.push('json');
    if (settings._fmtCsv) formats.push('csv');
    if (settings._fmtRss) formats.push('rss');
    const body = {
      port: parseInt(settings.port) || 0, bind_address: settings.bind_address, base_url: settings.base_url,
      secret_key: settings.secret_key, method: settings.method, limiter: !!settings.limiter,
      public_instance: !!settings.public_instance, image_proxy: !!settings.image_proxy,
      safe_search: parseInt(settings.safe_search) || 0, autocomplete: settings.autocomplete,
      default_lang: settings.default_lang, formats, default_locale: settings.default_locale,
      default_theme: settings.default_theme, simple_style: settings.simple_style, hotkeys: settings.hotkeys,
      query_in_title: !!settings.query_in_title, center_alignment: !!settings.center_alignment,
      results_on_new_tab: !!settings.results_on_new_tab, search_on_category_select: !!settings.search_on_category_select,
    };
    try { await apiPut('/searxng/settings', body); toast('Settings saved. Restart SearXNG to apply.'); }
    catch (e) { toast('Save failed: ' + (e as Error).message, 'error'); }
  };

  const regenSecret = () => {
    const chars = 'abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789';
    const buf = new Uint32Array(64);
    crypto.getRandomValues(buf);
    setSettings((prev: any) => ({ ...prev, secret_key: Array.from(buf, n => chars[n % chars.length]).join('') }));
  };

  const update = (key: string, val: any) => setSettings((prev: any) => ({ ...prev, [key]: val }));
  const running = status?.running;
  const installPhase = status?.install?.phase;
  const installMsg = installPhase && PHASE_LABELS[installPhase] ? PHASE_LABELS[installPhase] + '\u2026' + (status.install.progress > 0 ? ' ' + status.install.progress + '%' : '') :
    installPhase === 'error' ? 'Error: ' + (status.install.error || '') : '';
  const searxUrl = 'http://127.0.0.1:' + (status?.port || 8888) + '/';

  const autocompleteOptions = ['', 'google', 'duckduckgo', 'bing', 'brave', 'wikipedia', 'startpage', 'qwant', 'yandex', 'dbpedia', 'mwmbl', 'seznam', 'swisscows', 'privacywall', 'naver', '360search', 'baidu', 'quark', 'sogou'];

  return (
    <>
      <div className="card">
        <h3>SearXNG</h3>
        <p className="card-description">Managed local metasearch instance. Start creates an isolated Python venv and installs SearXNG (~80MB, a minute or two). If no system Python is found, Prism downloads a standalone interpreter first.</p>
        <div className="searx-status">
          <span className={`status-dot ${running ? 'running' : 'stopped'}`} />
          <span className="badge">{running ? 'Running' : 'Stopped'}</span>
          <a className="url" href={searxUrl} target="_blank" style={{ marginLeft: 'auto', fontSize: '13px' }}>{searxUrl}</a>
        </div>
        <div className="btn-row">
          <button className="btn btn-primary" disabled={running} onClick={handleStart}>Start</button>
          <button className="btn btn-danger" disabled={!running} onClick={handleStop}>Stop</button>
          <button className="btn btn-ghost" disabled={!running} onClick={handleRestart}>Restart</button>
        </div>
        <p className={`searx-install-msg ${installPhase === 'error' ? 'error' : ''}`} style={{ fontSize: '13px', color: 'var(--text-secondary)', margin: '10px 0', minHeight: '18px' }}>{installMsg}</p>
        <div className="toggle-row" style={{ marginTop: '16px' }}>
          <div>
            <div className="toggle-label">Auto-start on Prism launch</div>
            <div className="toggle-desc" id="searxAutostartNote">{status?.autostart ? (status.installed ? 'Auto-starts on Prism launch.' : 'SearXNG will auto-start once you install it with Start.') : 'Start SearXNG automatically when Prism launches'}</div>
          </div>
          <label className="toggle-switch">
            <input type="checkbox" checked={!!status?.autostart} onChange={e => handleAutostart(e.target.checked)} />
            <span className="toggle-slider" />
          </label>
        </div>
      </div>

      {settingsError ? (
        <div className="card"><p className="card-description" style={{ color: 'var(--danger)' }}>{settingsError}</p></div>
      ) : settings && (
        <div className="card">
          <h3>Settings</h3>
          <p className="card-description">Structured editor for the SearXNG instance. Changes require a Restart to take effect. Advanced keys (engines, outgoing, redis) are not exposed here \u2014 edit <code>settings.yml</code> directly if needed.</p>

          <div className="searx-form-section" style={{ marginBottom: '20px' }}>
            <h4 style={{ margin: '0 0 12px', paddingBottom: '8px', fontSize: '14px', fontWeight: 600, borderBottom: '1px solid var(--border)' }}>Server</h4>
            <div style={{ display: 'grid', gridTemplateColumns: '1fr 1fr', gap: '14px 18px' }}>
              <div className="field" style={{ marginBottom: 0 }}><label>Port</label><input type="number" value={settings.port || ''} onChange={e => update('port', e.target.value)} /></div>
              <div className="field" style={{ marginBottom: 0 }}><label>Bind address</label><input type="text" value={settings.bind_address || ''} onChange={e => update('bind_address', e.target.value)} /></div>
              <div className="field" style={{ marginBottom: 0 }}><label>Base URL</label><input type="text" placeholder="(blank for local)" value={settings.base_url || ''} onChange={e => update('base_url', e.target.value)} /></div>
              <div className="field" style={{ marginBottom: 0 }}><label>Method</label><select value={settings.method || 'POST'} onChange={e => update('method', e.target.value)}><option value="POST">POST</option><option value="GET">GET</option></select></div>
              <div className="field" style={{ marginBottom: 0, gridColumn: '1 / -1' }}><label>Secret key</label>
                <div style={{ display: 'flex', gap: '8px', alignItems: 'stretch' }}>
                  <input type="text" style={{ flex: 1, fontFamily: 'monospace' }} value={settings.secret_key || ''} onChange={e => update('secret_key', e.target.value)} />
                  <button className="btn btn-ghost" onClick={regenSecret}>Regenerate</button>
                </div>
              </div>
            </div>
            <div style={{ display: 'flex', flexDirection: 'column', gap: '4px', marginTop: '14px' }}>
              {[['limiter', 'Rate limiter', 'Requires Valkey/Redis'], ['public_instance', 'Public instance', 'Expose to the network (not just localhost)'], ['image_proxy', 'Image proxy', 'Proxy image requests through SearXNG']].map(([key, label, desc]) => (
                <div key={key} style={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between', padding: '8px 0' }}>
                  <div><div className="toggle-label">{label}</div><div className="toggle-desc">{desc}</div></div>
                  <label className="toggle-switch"><input type="checkbox" checked={!!settings[key]} onChange={e => update(key, e.target.checked)} /><span className="toggle-slider" /></label>
                </div>
              ))}
            </div>
          </div>

          <div className="searx-form-section" style={{ marginBottom: '20px' }}>
            <h4 style={{ margin: '0 0 12px', paddingBottom: '8px', fontSize: '14px', fontWeight: 600, borderBottom: '1px solid var(--border)' }}>Search</h4>
            <div style={{ display: 'grid', gridTemplateColumns: '1fr 1fr', gap: '14px 18px' }}>
              <div className="field" style={{ marginBottom: 0 }}><label>Safe search</label><select value={String(settings.safe_search || 0)} onChange={e => update('safe_search', e.target.value)}><option value="0">None</option><option value="1">Moderate</option><option value="2">Strict</option></select></div>
              <div className="field" style={{ marginBottom: 0 }}><label>Autocomplete</label><select value={settings.autocomplete || ''} onChange={e => update('autocomplete', e.target.value)}><option value="">(off)</option>{autocompleteOptions.filter(o => o).map(o => <option key={o} value={o}>{o}</option>)}</select></div>
              <div className="field" style={{ marginBottom: 0 }}><label>Default language</label><input type="text" placeholder="(blank = browser)" value={settings.default_lang || ''} onChange={e => update('default_lang', e.target.value)} /></div>
              <div className="field" style={{ marginBottom: 0, gridColumn: '1 / -1' }}><label>Output formats (html required)</label>
                <div style={{ display: 'flex', gap: '18px', flexWrap: 'wrap', padding: '4px 0' }}>
                  {[['html', '_fmtHtml'], ['json', '_fmtJson'], ['csv', '_fmtCsv'], ['rss', '_fmtRss']].map(([label, key]) => (
                    <label key={label} style={{ display: 'flex', alignItems: 'center', gap: '6px', fontSize: '14px', cursor: 'pointer' }}>
                      <input type="checkbox" checked={!!(settings.formats || []).includes(label) || !!settings[key]} onChange={e => update(key, e.target.checked)} /> {label}
                    </label>
                  ))}
                </div>
              </div>
              <div className="field" style={{ marginBottom: 0 }}><label>Default locale</label><input type="text" value={settings.default_locale || ''} onChange={e => update('default_locale', e.target.value)} /></div>
              <div className="field" style={{ marginBottom: 0 }}><label>Default theme</label><input type="text" value={settings.default_theme || ''} onChange={e => update('default_theme', e.target.value)} /></div>
              <div className="field" style={{ marginBottom: 0 }}><label>Simple style</label><input type="text" value={settings.simple_style || ''} onChange={e => update('simple_style', e.target.value)} /></div>
              <div className="field" style={{ marginBottom: 0 }}><label>Hotkeys</label><input type="text" value={settings.hotkeys || ''} onChange={e => update('hotkeys', e.target.value)} /></div>
            </div>
            <div style={{ display: 'flex', flexDirection: 'column', gap: '4px', marginTop: '14px' }}>
              {[['query_in_title', 'Query in title'], ['center_alignment', 'Center alignment'], ['results_on_new_tab', 'Results on new tab'], ['search_on_category_select', 'Search on category select']].map(([key, label]) => (
                <div key={key} style={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between', padding: '8px 0' }}>
                  <div className="toggle-label">{label}</div>
                  <label className="toggle-switch"><input type="checkbox" checked={!!settings[key]} onChange={e => update(key, e.target.checked)} /><span className="toggle-slider" /></label>
                </div>
              ))}
            </div>
          </div>

          <div className="btn-row">
            <button className="btn btn-primary" onClick={saveSettings}>Save Settings</button>
          </div>
        </div>
      )}
    </>
  );
}

import { useEffect, useState, useCallback, useRef } from 'react';
import { api, apiPost, apiPut } from '../api';
import { useToast } from '../ToastContext';
import { Button } from '@/components/ui/button';
import { Input } from '@/components/ui/input';
import { Label } from '@/components/ui/label';
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select';
import { Checkbox } from '@/components/ui/checkbox';
import { Switch } from '@/components/ui/switch';

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
      <div className="rounded-xl border border-border bg-card p-6 mb-4">
        <h3 className="text-sm font-semibold tracking-tight mb-1">SearXNG</h3>
        <p className="text-[13px] text-muted-foreground mb-4">Managed local metasearch instance. Start creates an isolated Python venv and installs SearXNG (~80MB, a minute or two). If no system Python is found, Prism downloads a standalone interpreter first.</p>
        <div className="flex items-center gap-2.5 px-3.5 py-3 my-3 border border-border rounded-md bg-card">
          <span className={`w-2 h-2 rounded-full inline-block ${running ? 'bg-green-500 shadow-[0_0_0_3px_rgba(34,197,94,0.18)]' : 'bg-destructive'}`} />
          <span className="text-sm font-semibold">{running ? 'Running' : 'Stopped'}</span>
          <a className="ml-auto text-[13px] text-muted-foreground hover:text-foreground hover:underline" href={searxUrl} target="_blank">{searxUrl}</a>
        </div>
        <div className="flex gap-2.5 flex-wrap">
          <Button disabled={running} onClick={handleStart}>Start</Button>
          <Button variant="destructive" disabled={!running} onClick={handleStop}>Stop</Button>
          <Button variant="outline" disabled={!running} onClick={handleRestart}>Restart</Button>
        </div>
        <p className={`text-[13px] text-muted-foreground my-2.5 min-h-[18px] ${installPhase === 'error' ? 'text-destructive' : ''}`}>{installMsg}</p>
        <div className="flex items-center justify-between gap-3 mt-4">
          <div>
            <div className="text-sm font-medium text-foreground">Auto-start on Prism launch</div>
            <div className="text-xs text-muted-foreground mt-0.5" id="searxAutostartNote">{status?.autostart ? (status.installed ? 'Auto-starts on Prism launch.' : 'SearXNG will auto-start once you install it with Start.') : 'Start SearXNG automatically when Prism launches'}</div>
          </div>
          <Switch checked={!!status?.autostart} onCheckedChange={(v) => handleAutostart(!!v)} />
        </div>
      </div>

      {settingsError ? (
        <div className="rounded-xl border border-border bg-card p-6 mb-4"><p className="text-[13px] text-destructive">{settingsError}</p></div>
      ) : settings && (
        <div className="rounded-xl border border-border bg-card p-6 mb-4">
          <h3 className="text-sm font-semibold tracking-tight mb-1">Settings</h3>
          <p className="text-[13px] text-muted-foreground mb-5">Structured editor for the SearXNG instance. Changes require a Restart to take effect. Advanced keys (engines, outgoing, redis) are not exposed here — edit <code>settings.yml</code> directly if needed.</p>

          <div className="mb-5">
            <h4 className="text-sm font-semibold m-0 pb-2 border-b border-border mb-3">Server</h4>
            <div className="grid grid-cols-2 gap-3.5">
              <div><Label>Port</Label><Input type="number" value={settings.port || ''} onChange={e => update('port', e.target.value)} className="mt-1.5" /></div>
              <div><Label>Bind address</Label><Input type="text" value={settings.bind_address || ''} onChange={e => update('bind_address', e.target.value)} className="mt-1.5" /></div>
              <div><Label>Base URL</Label><Input type="text" placeholder="(blank for local)" value={settings.base_url || ''} onChange={e => update('base_url', e.target.value)} className="mt-1.5" /></div>
              <div><Label>Method</Label><Select value={settings.method || 'POST'} onValueChange={(v) => update('method', v)}><SelectTrigger className="w-full mt-1.5"><SelectValue /></SelectTrigger><SelectContent><SelectItem value="POST">POST</SelectItem><SelectItem value="GET">GET</SelectItem></SelectContent></Select></div>
              <div className="col-span-2"><Label>Secret key</Label>
                <div className="flex gap-2 items-center mt-1.5">
                  <Input type="text" className="flex-1 font-mono" value={settings.secret_key || ''} onChange={e => update('secret_key', e.target.value)} />
                  <Button variant="outline" onClick={regenSecret}>Regenerate</Button>
                </div>
              </div>
            </div>
            <div className="flex flex-col gap-1 mt-3.5">
              {[['limiter', 'Rate limiter', 'Requires Valkey/Redis'], ['public_instance', 'Public instance', 'Expose to the network (not just localhost)'], ['image_proxy', 'Image proxy', 'Proxy image requests through SearXNG']].map(([key, label, desc]) => (
                <div key={key} className="flex items-center justify-between py-2">
                  <div><div className="text-sm font-medium text-foreground">{label}</div><div className="text-xs text-muted-foreground mt-0.5">{desc}</div></div>
                  <Switch checked={!!settings[key]} onCheckedChange={(v) => update(key, !!v)} />
                </div>
              ))}
            </div>
          </div>

          <div className="mb-5">
            <h4 className="text-sm font-semibold m-0 pb-2 border-b border-border mb-3">Search</h4>
            <div className="grid grid-cols-2 gap-3.5">
              <div><Label>Safe search</Label><Select value={String(settings.safe_search || 0)} onValueChange={(v) => update('safe_search', v)}><SelectTrigger className="w-full mt-1.5"><SelectValue /></SelectTrigger><SelectContent><SelectItem value="0">None</SelectItem><SelectItem value="1">Moderate</SelectItem><SelectItem value="2">Strict</SelectItem></SelectContent></Select></div>
              <div><Label>Autocomplete</Label><Select value={settings.autocomplete || ''} onValueChange={(v) => update('autocomplete', v)}><SelectTrigger className="w-full mt-1.5"><SelectValue /></SelectTrigger><SelectContent><SelectItem value="">(off)</SelectItem>{autocompleteOptions.filter(o => o).map(o => <SelectItem key={o} value={o}>{o}</SelectItem>)}</SelectContent></Select></div>
              <div><Label>Default language</Label><Input type="text" placeholder="(blank = browser)" value={settings.default_lang || ''} onChange={e => update('default_lang', e.target.value)} className="mt-1.5" /></div>
              <div className="col-span-2"><Label>Output formats (html required)</Label>
                <div className="flex gap-4.5 flex-wrap py-1">
                  {[['html', '_fmtHtml'], ['json', '_fmtJson'], ['csv', '_fmtCsv'], ['rss', '_fmtRss']].map(([label, key]) => (
                    <Label key={label} className="flex items-center gap-2 cursor-pointer text-sm">
                      <Checkbox checked={!!(settings.formats || []).includes(label) || !!settings[key]} onCheckedChange={(v) => update(key, !!v)} /> {label}
                    </Label>
                  ))}
                </div>
              </div>
              <div><Label>Default locale</Label><Input type="text" value={settings.default_locale || ''} onChange={e => update('default_locale', e.target.value)} className="mt-1.5" /></div>
              <div><Label>Default theme</Label><Input type="text" value={settings.default_theme || ''} onChange={e => update('default_theme', e.target.value)} className="mt-1.5" /></div>
              <div><Label>Simple style</Label><Input type="text" value={settings.simple_style || ''} onChange={e => update('simple_style', e.target.value)} className="mt-1.5" /></div>
              <div><Label>Hotkeys</Label><Input type="text" value={settings.hotkeys || ''} onChange={e => update('hotkeys', e.target.value)} className="mt-1.5" /></div>
            </div>
            <div className="flex flex-col gap-1 mt-3.5">
              {[['query_in_title', 'Query in title'], ['center_alignment', 'Center alignment'], ['results_on_new_tab', 'Results on new tab'], ['search_on_category_select', 'Search on category select']].map(([key, label]) => (
                <div key={key} className="flex items-center justify-between py-2">
                  <div className="text-sm font-medium text-foreground">{label}</div>
                  <Switch checked={!!settings[key]} onCheckedChange={(v) => update(key, !!v)} />
                </div>
              ))}
            </div>
          </div>

          <div className="flex gap-2.5 flex-wrap">
            <Button onClick={saveSettings}>Save Settings</Button>
          </div>
        </div>
      )}
    </>
  );
}

import { useEffect, useState, useCallback } from 'react';
import { api, apiPost, apiPut } from '../api';
import { toast } from './ui/toast';
import { Button } from '@/components/ui/button';
import { Input } from '@/components/ui/input';
import { Label } from '@/components/ui/label';
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select';
import { Switch } from '@/components/ui/switch';
import { Textarea } from '@/components/ui/textarea';
import { Drawer, DrawerClose, DrawerContent, DrawerDescription, DrawerFooter, DrawerHeader, DrawerTitle, DrawerTrigger } from '@/components/ui/drawer';

// Built-in provider catalog is returned by /admin/search/providers.
// Each entry: { id, name, needsKey, envVar, signupUrl, isManaged }
type ProviderMeta = {
  id: string;
  name: string;
  needsKey: boolean;
  envVar?: string;
  signupUrl?: string;
  isManaged?: boolean; // searxng (Prism runs the instance)
  custom?: boolean;    // user-defined declarative provider
};

type ProviderState = {
  enabled: boolean;
  apiKey?: string;      // write-only: backend returns "" or "••••" sentinel
  baseURL?: string;     // searxng / ollama only
  hasKey?: boolean;     // backend-reported: a key is configured (env or stored)
  keyFromEnv?: boolean;
};

type CustomProvider = {
  id: string;
  name: string;
  endpoint: string;
  method?: string;
  authHeader?: string;
  keyEnv?: string;
  queryParam?: string;
  body?: Record<string, any>;
  params?: Record<string, string>;
  resultsJSONPath: string;
  fieldMap: Record<string, string>;
  enabled: boolean;
  hasKey?: boolean;
  keyFromEnv?: boolean;
};

type SearchConfig = {
  active: string;
  fallback: string[];
  maxPerTurn: number;
  timeoutMs: number;
  defaultNumResults: number;
  providers: Record<string, ProviderState>;
  customProviders?: CustomProvider[];
};

type TestResult = { ok: boolean; resultCount?: number; error?: string; sample?: { title: string; url: string }[]; provider?: string };

const STATUS_BADGE: Record<string, { label: string; cls: string }> = {
  managed: { label: 'Managed', cls: 'bg-green-500/15 text-green-600 dark:text-green-400' },
  running: { label: 'Running', cls: 'bg-green-500/15 text-green-600 dark:text-green-400' },
  configured: { label: 'Configured', cls: 'bg-blue-500/15 text-blue-600 dark:text-blue-400' },
  missing: { label: 'Missing key', cls: 'bg-amber-500/15 text-amber-600 dark:text-amber-400' },
  disabled: { label: 'Not enabled', cls: 'bg-muted text-muted-foreground' },
};

function badgeFor(meta: ProviderMeta, s: ProviderState | undefined): { label: string; cls: string } {
  if (!s || !s.enabled) return STATUS_BADGE.disabled;
  if (meta.isManaged) return STATUS_BADGE.managed;
  if (!meta.needsKey) return STATUS_BADGE.running;
  if (s.hasKey) return STATUS_BADGE.configured;
  return STATUS_BADGE.missing;
}

function customBadge(cp: CustomProvider): { label: string; cls: string } {
  if (!cp.enabled) return STATUS_BADGE.disabled;
  if (!cp.authHeader) return STATUS_BADGE.running;
  if (cp.hasKey) return STATUS_BADGE.configured;
  return STATUS_BADGE.missing;
}

const EMPTY_FORM = {
  id: '', name: '', endpoint: '', method: 'POST', authHeader: '', apiKey: '', keyEnv: '',
  queryParam: '', resultsJSONPath: 'results', body: '', params: '', fieldMap: '{"title":"title","url":"url","snippet":"snippet"}', enabled: true,
};
type CustomForm = typeof EMPTY_FORM;

export default function SearchProvidersPanel() {
  const [catalog, setCatalog] = useState<ProviderMeta[]>([]);
  const [cfg, setCfg] = useState<SearchConfig | null>(null);
  const [testing, setTesting] = useState<string | null>(null);
  const [testResult, setTestResult] = useState<Record<string, TestResult>>({});
  const [dirty, setDirty] = useState(false);
  const [drawerOpen, setDrawerOpen] = useState(false);
  const [editingId, setEditingId] = useState<string | null>(null);
  const [form, setForm] = useState<CustomForm>(EMPTY_FORM);

  const load = useCallback(async () => {
    try {
      const [cats, c] = await Promise.all([api('/search/providers'), api('/search/config')]);
      setCatalog(cats || []);
      setCfg(c);
      setDirty(false);
    } catch (e) {
      toast.add({ title: 'Failed to load search config: ' + (e as Error).message, type: 'error' });
    }
  }, []);

  useEffect(() => { load(); }, [load]);

  const update = (mut: (c: SearchConfig) => SearchConfig) => {
    setCfg((prev) => (prev ? mut(prev) : prev));
    setDirty(true);
  };

  const setActive = (id: string) => update((c) => ({ ...c, active: id }));
  const setEnabled = (id: string, enabled: boolean) =>
    update((c) => ({ ...c, providers: { ...c.providers, [id]: { ...c.providers[id], enabled } } }));
  const setKey = (id: string, apiKey: string) =>
    update((c) => ({ ...c, providers: { ...c.providers, [id]: { ...c.providers[id], apiKey } } }));
  const setBaseURL = (id: string, baseURL: string) =>
    update((c) => ({ ...c, providers: { ...c.providers, [id]: { ...c.providers[id], baseURL } } }));

  const save = async () => {
    if (!cfg) return;
    try {
      await apiPut('/search/config', cfg);
      toast.add({ title: 'Search settings saved.', type: 'success' });
      setDirty(false);
      load(); // refresh hasKey flags from backend
    } catch (e) {
      toast.add({ title: 'Save failed: ' + (e as Error).message, type: 'error' });
    }
  };

  const persistCustom = async (next: SearchConfig) => {
    try {
      await apiPut('/search/config', next);
      toast.add({ title: 'Search settings saved.', type: 'success' });
      setDirty(false);
      load();
    } catch (e) {
      toast.add({ title: 'Save failed: ' + (e as Error).message, type: 'error' });
    }
  };

  const setCustomEnabled = (id: string, enabled: boolean) =>
    update((c) => ({ ...c, customProviders: (c.customProviders ?? []).map((cp) => (cp.id === id ? { ...cp, enabled } : cp)) }));

  const test = async (id: string) => {
    setTesting(id);
    try {
      const r: TestResult = await apiPost('/search/test', { provider: id });
      setTestResult((prev) => ({ ...prev, [id]: r }));
      toast.add({ title: r.ok ? `${id}: ${r.resultCount ?? 0} results` : `${id}: ${r.error ?? 'failed'}`, type: r.ok ? 'success' : 'error' });
    } catch (e) {
      setTestResult((prev) => ({ ...prev, [id]: { ok: false, error: (e as Error).message } }));
      toast.add({ title: `${id}: ${(e as Error).message}`, type: 'error' });
    } finally {
      setTesting(null);
    }
  };

  const moveFallback = (id: string, dir: -1 | 1) => {
    update((c) => {
      const arr = [...c.fallback];
      const i = arr.indexOf(id);
      if (i < 0) return c;
      const j = i + dir;
      if (j < 0 || j >= arr.length) return c;
      [arr[i], arr[j]] = [arr[j], arr[i]];
      return { ...c, fallback: arr };
    });
  };

  const openAdd = () => {
    setEditingId(null);
    setForm(EMPTY_FORM);
    setDrawerOpen(true);
  };

  const openEdit = (cp: CustomProvider) => {
    setEditingId(cp.id);
    setForm({
      id: cp.id, name: cp.name || '', endpoint: cp.endpoint || '', method: cp.method || 'POST',
      authHeader: cp.authHeader || '', apiKey: '', keyEnv: cp.keyEnv || '',       queryParam: cp.queryParam || '',
      resultsJSONPath: cp.resultsJSONPath || 'results',
      body: cp.body ? JSON.stringify(cp.body, null, 2) : '',
      params: cp.params ? JSON.stringify(cp.params, null, 2) : '',
      fieldMap: cp.fieldMap ? JSON.stringify(cp.fieldMap, null, 2) : '{"title":"title","url":"url"}',
      enabled: cp.enabled,
    });
    setDrawerOpen(true);
  };

  const parseObjectField = (text: string, label: string): Record<string, any> | null => {
    const t = text.trim();
    if (!t) return {};
    try {
      const v = JSON.parse(t);
      if (v && typeof v === 'object' && !Array.isArray(v)) return v;
      toast.add({ title: `${label} must be a JSON object.`, type: 'error' });
      return null;
    } catch (e) {
      toast.add({ title: `${label} has invalid JSON: ${(e as Error).message}`, type: 'error' });
      return null;
    }
  };

  const saveCustom = async () => {
    if (!cfg) return;
    const id = form.id.trim();
    if (!id) { toast.add({ title: 'Provider ID is required.', type: 'error' }); return; }
    if (!form.endpoint.trim()) { toast.add({ title: 'Endpoint URL is required.', type: 'error' }); return; }
    if (catalog.find((m) => m.id === id)) { toast.add({ title: `ID "${id}" collides with a built-in provider.`, type: 'error' }); return; }
    const body = parseObjectField(form.body, 'Body template');
    if (body === null) return;
    const params = parseObjectField(form.params, 'Params');
    if (params === null) return;
    const fieldMap = parseObjectField(form.fieldMap, 'Field map');
    if (fieldMap === null) return;

    const cp: CustomProvider = {
      id, name: form.name.trim(), endpoint: form.endpoint.trim(), method: form.method || 'POST',
      authHeader: form.authHeader.trim(), keyEnv: form.keyEnv.trim(), queryParam: form.queryParam.trim(),
      body: Object.keys(body).length ? body : undefined,
      params: Object.keys(params).length ? (params as Record<string, string>) : undefined,
      resultsJSONPath: form.resultsJSONPath.trim() || 'results',
      fieldMap: fieldMap as Record<string, string>,
      enabled: form.enabled,
    };
    const list = cfg.customProviders ?? [];
    const idx = list.findIndex((x) => x.id === editingId);
    const next = idx >= 0 ? list.map((x, i) => (i === idx ? { ...x, ...cp, id: x.id } : x)) : [...list, cp];
    const nextCfg = { ...cfg, customProviders: next };
    setCfg(nextCfg);
    setDrawerOpen(false);
    // Persist atomically with the rest of the search config.
    await persistCustom(nextCfg);
  };

  const deleteCustom = async (id: string) => {
    if (!cfg) return;
    const nextCfg = { ...cfg, customProviders: (cfg.customProviders ?? []).filter((x) => x.id !== id) };
    setCfg(nextCfg);
    await persistCustom(nextCfg);
  };

  if (!cfg) return <p className="text-sm text-muted-foreground">Loading…</p>;

  const customProviders = cfg.customProviders ?? [];
  const customById = new Map(customProviders.map((cp) => [cp.id, cp]));
  const allProviders: ProviderMeta[] = [
    ...catalog,
    ...customProviders.map((cp) => ({ id: cp.id, name: cp.name || cp.id, needsKey: !!cp.authHeader, isManaged: false, custom: true })),
  ];
  const isEnabled = (id: string) => {
    const s = cfg.providers[id];
    if (s) return !!s.enabled;
    const cp = customById.get(id);
    return cp ? !!cp.enabled : false;
  };
  const fallbackOrder = cfg.fallback.filter(isEnabled);

  return (
    <>
      {/* Active + limits */}
      <div className="rounded-xl border border-border bg-card p-6 mb-4">
        <h3 className="text-sm font-semibold tracking-tight mb-1">Search providers</h3>
        <p className="text-[13px] text-muted-foreground mb-4">
          Prism intercepts the built-in web-search tools of Claude Code, ZCode, Codex, and Grok Build and runs them through the active provider here — so search works on <em>any</em> upstream model, not just Anthropic/OpenAI. Pick a backend, add API keys, and set a fallback order. Keys are stored locally (0600) and never written into agent configs.
        </p>

        <div className="grid grid-cols-2 gap-3.5 mb-4">
          <div>
            <Label>Active provider</Label>
            <Select value={cfg.active} onValueChange={setActive}>
              <SelectTrigger className="w-full mt-1.5"><SelectValue /></SelectTrigger>
              <SelectContent>
                {allProviders.map((m) => (
                  <SelectItem key={m.id} value={m.id}>{m.name}</SelectItem>
                ))}
              </SelectContent>
            </Select>
          </div>
          <div>
            <Label>Max searches per turn</Label>
            <Input type="number" min={1} max={20} value={cfg.maxPerTurn} onChange={(e) => update((c) => ({ ...c, maxPerTurn: parseInt(e.target.value) || 5 }))} className="mt-1.5" />
          </div>
          <div>
            <Label>Timeout (ms)</Label>
            <Input type="number" min={1000} step={500} value={cfg.timeoutMs} onChange={(e) => update((c) => ({ ...c, timeoutMs: parseInt(e.target.value) || 8000 }))} className="mt-1.5" />
          </div>
          <div>
            <Label>Default results per search</Label>
            <Input type="number" min={1} max={10} value={cfg.defaultNumResults} onChange={(e) => update((c) => ({ ...c, defaultNumResults: parseInt(e.target.value) || 5 }))} className="mt-1.5" />
          </div>
        </div>

        {/* Fallback order */}
        <h4 className="text-sm font-semibold m-0 pb-2 border-b border-border mb-3">Fallback order</h4>
        <p className="text-xs text-muted-foreground mb-2.5">If the active provider errors or returns no results, Prism tries these in order. Unconfigured providers are skipped automatically.</p>
        {fallbackOrder.length === 0 ? (
          <p className="text-xs text-muted-foreground italic">No fallback providers enabled.</p>
        ) : (
          <div className="flex flex-col gap-1.5">
            {fallbackOrder.map((id, i) => {
              const m = allProviders.find((c) => c.id === id);
              return (
                <div key={id} className="flex items-center gap-2.5 px-3 py-2 border border-border rounded-md bg-card">
                  <span className="text-xs font-mono text-muted-foreground w-4">{i + 1}</span>
                  <span className="text-sm font-medium">{m?.name ?? id}</span>
                  <div className="ml-auto flex gap-1">
                    <Button variant="outline" size="sm" disabled={i === 0} onClick={() => moveFallback(id, -1)}>&uarr;</Button>
                    <Button variant="outline" size="sm" disabled={i === fallbackOrder.length - 1} onClick={() => moveFallback(id, 1)}>&darr;</Button>
                  </div>
                </div>
              );
            })}
          </div>
        )}
      </div>

      {/* Provider list */}
      <div className="rounded-xl border border-border bg-card p-6 mb-4">
        <div className="flex items-center justify-between mb-1">
          <h3 className="text-sm font-semibold tracking-tight">Providers</h3>
          <Drawer open={drawerOpen} onOpenChange={setDrawerOpen} direction="right">
            <DrawerTrigger asChild>
              <Button size="sm" onClick={openAdd}>+ Add custom provider</Button>
            </DrawerTrigger>
            <DrawerContent>
              <DrawerHeader>
                <DrawerTitle>{editingId ? 'Edit custom provider' : 'Add custom provider'}</DrawerTitle>
                <DrawerDescription>Define any REST search API without rebuilding Prism. Fields support <code className="text-xs">&#123;&#123;query&#125;&#125;</code>, <code className="text-xs">&#123;&#123;numResults&#125;&#125;</code>, <code className="text-xs">&#123;&#123;allowedDomains&#125;&#125;</code> templates.</DrawerDescription>
              </DrawerHeader>
              <div className="px-4 pb-4 flex-1 overflow-y-auto">
                <div className="grid grid-cols-2 gap-2">
                  <div>
                    <Label className="text-xs text-muted-foreground mb-0.5">ID</Label>
                    <Input type="text" placeholder="e.g. linkup" value={form.id} disabled={!!editingId} onChange={(e) => setForm((f) => ({ ...f, id: e.target.value }))} />
                  </div>
                  <div>
                    <Label className="text-xs text-muted-foreground mb-0.5">Name</Label>
                    <Input type="text" placeholder="e.g. Linkup" value={form.name} onChange={(e) => setForm((f) => ({ ...f, name: e.target.value }))} />
                  </div>
                  <div className="col-span-2">
                    <Label className="text-xs text-muted-foreground mb-0.5">Endpoint URL</Label>
                    <Input type="text" placeholder="https://api.example.com/v1/search" value={form.endpoint} onChange={(e) => setForm((f) => ({ ...f, endpoint: e.target.value }))} className="font-mono" />
                  </div>
                  <div>
                    <Label className="text-xs text-muted-foreground mb-0.5">Method</Label>
                    <Select value={form.method} onValueChange={(v) => setForm((f) => ({ ...f, method: v }))}>
                      <SelectTrigger className="w-full"><SelectValue /></SelectTrigger>
                      <SelectContent>
                        <SelectItem value="POST">POST</SelectItem>
                        <SelectItem value="GET">GET</SelectItem>
                      </SelectContent>
                    </Select>
                  </div>
                  <div>
                    <Label className="text-xs text-muted-foreground mb-0.5">Query param (GET)</Label>
                    <Input type="text" placeholder="q" value={form.queryParam} onChange={(e) => setForm((f) => ({ ...f, queryParam: e.target.value }))} className="font-mono" />
                  </div>
                  <div>
                    <Label className="text-xs text-muted-foreground mb-0.5">Auth header</Label>
                    <Input type="text" placeholder="X-API-KEY or Authorization Bearer" value={form.authHeader} onChange={(e) => setForm((f) => ({ ...f, authHeader: e.target.value }))} className="font-mono" />
                  </div>
                  <div>
                    <Label className="text-xs text-muted-foreground mb-0.5">API key{form.authHeader && (form.keyEnv || form.apiKey) ? ' (leave blank to keep)' : ''}</Label>
                    <Input type="password" placeholder={editingId ? '\u2022\u2022\u2022\u2022\u2022' : 'optional'} value={form.apiKey} onChange={(e) => setForm((f) => ({ ...f, apiKey: e.target.value }))} className="font-mono" />
                  </div>
                  <div>
                    <Label className="text-xs text-muted-foreground mb-0.5">Key env var</Label>
                    <Input type="text" placeholder="LINKUP_API_KEY" value={form.keyEnv} onChange={(e) => setForm((f) => ({ ...f, keyEnv: e.target.value }))} className="font-mono" />
                  </div>
                  <div>
                    <Label className="text-xs text-muted-foreground mb-0.5">Results JSON path</Label>
                    <Input type="text" placeholder="results or web.results" value={form.resultsJSONPath} onChange={(e) => setForm((f) => ({ ...f, resultsJSONPath: e.target.value }))} className="font-mono" />
                  </div>
                  <div>
                    <Label className="text-xs text-muted-foreground mb-0.5">Enabled</Label>
                    <div className="mt-1.5"><Switch checked={form.enabled} onCheckedChange={(v) => setForm((f) => ({ ...f, enabled: !!v }))} /></div>
                  </div>
                  <div className="col-span-2">
                    <Label className="text-xs text-muted-foreground mb-0.5">Body template (JSON, POST only)</Label>
                    <Textarea value={form.body} onChange={(e) => setForm((f) => ({ ...f, body: e.target.value }))} placeholder={'{\n  "query": "{{query}}",\n  "num": "{{numResults}}"\n}'} className="font-mono text-xs" />
                  </div>
                  <div className="col-span-2">
                    <Label className="text-xs text-muted-foreground mb-0.5">Params (JSON, GET only)</Label>
                    <Textarea value={form.params} onChange={(e) => setForm((f) => ({ ...f, params: e.target.value }))} placeholder={'{"lang":"en","country":"us"}'} className="font-mono text-xs" />
                  </div>
                  <div className="col-span-2">
                    <Label className="text-xs text-muted-foreground mb-0.5">Field map (JSON)</Label>
                    <Textarea value={form.fieldMap} onChange={(e) => setForm((f) => ({ ...f, fieldMap: e.target.value }))} placeholder={'{"title":"title","url":"url","snippet":"content","pageAge":"publishedDate","score":"score","highlights":"highlights"}'} className="font-mono text-xs" />
                  </div>
                </div>
              </div>
              <DrawerFooter>
                <Button onClick={saveCustom}>{editingId ? 'Save Changes' : 'Add Provider'}</Button>
                <DrawerClose asChild>
                  <Button variant="outline">Cancel</Button>
                </DrawerClose>
              </DrawerFooter>
            </DrawerContent>
          </Drawer>
        </div>
        <p className="text-[13px] text-muted-foreground mb-4">Enable a provider and add its API key. Env vars (e.g. <code>EXA_API_KEY</code>) are detected automatically and take precedence. Custom providers are declared entirely in the UI.</p>

        <div className="flex flex-col gap-1">
          {allProviders.map((m) => {
            if (m.custom) {
              const cp = customById.get(m.id)!;
              const badge = customBadge(cp);
              const tr = testResult[m.id];
              return (
                <div key={m.id} className="border border-border rounded-md p-3.5">
                  <div className="flex items-center gap-3">
                    <Switch checked={cp.enabled} onCheckedChange={(v) => setCustomEnabled(m.id, !!v)} />
                    <div className="flex-1 min-w-0">
                      <div className="flex items-center gap-2">
                        <span className="text-sm font-semibold text-foreground">{cp.name || cp.id}</span>
                        <span className={`text-[11px] font-medium px-1.5 py-0.5 rounded ${badge.cls}`}>{badge.label}</span>
                        <span className="text-[11px] font-medium px-1.5 py-0.5 rounded bg-accent text-accent-foreground">Custom</span>
                        {cfg.active === m.id && <span className="text-[11px] font-medium px-1.5 py-0.5 rounded bg-accent text-accent-foreground">Active</span>}
                      </div>
                      <div className="text-xs text-muted-foreground mt-0.5">
                        <span className="font-mono">{cp.endpoint}</span>
                        {cp.authHeader && <> &middot; auth: <span className="font-mono">{cp.authHeader}</span></>}
                        {cp.keyEnv && <> &middot; <span className="font-mono">{cp.keyEnv}</span></>}
                        {cp.resultsJSONPath && <> &middot; path: <span className="font-mono">{cp.resultsJSONPath}</span></>}
                      </div>
                    </div>
                    <div className="flex gap-1.5">
                      <Button variant="outline" size="sm" disabled={testing === m.id} onClick={() => test(m.id)}>
                        {testing === m.id ? 'Testing\u2026' : 'Test'}
                      </Button>
                      <Button variant="outline" size="sm" onClick={() => openEdit(cp)}>Edit</Button>
                      <Button variant="outline" size="sm" className="text-destructive hover:text-destructive" onClick={() => deleteCustom(m.id)}>Delete</Button>
                    </div>
                  </div>
                  {tr && (
                    <div className={`mt-2.5 text-xs rounded px-2.5 py-1.5 ${tr.ok ? 'bg-green-500/10 text-green-700 dark:text-green-400' : 'bg-destructive/10 text-destructive'}`}>
                      {tr.ok ? `${tr.resultCount ?? 0} results` : tr.error}
                      {tr.ok && tr.sample && tr.sample.length > 0 && (
                        <ul className="mt-1 list-disc list-inside text-muted-foreground">
                          {tr.sample.slice(0, 3).map((r, i) => <li key={i} className="truncate"><a className="hover:underline" href={r.url} target="_blank">{r.title}</a></li>)}
                        </ul>
                      )}
                    </div>
                  )}
                </div>
              );
            }

            const s = cfg.providers[m.id] ?? { enabled: false };
            const badge = badgeFor(m, s);
            const tr = testResult[m.id];
            return (
              <div key={m.id} className="border border-border rounded-md p-3.5">
                <div className="flex items-center gap-3">
                  <Switch checked={!!s.enabled} onCheckedChange={(v) => setEnabled(m.id, !!v)} />
                  <div className="flex-1 min-w-0">
                    <div className="flex items-center gap-2">
                      <span className="text-sm font-semibold text-foreground">{m.name}</span>
                      <span className={`text-[11px] font-medium px-1.5 py-0.5 rounded ${badge.cls}`}>{badge.label}</span>
                      {cfg.active === m.id && <span className="text-[11px] font-medium px-1.5 py-0.5 rounded bg-accent text-accent-foreground">Active</span>}
                    </div>
                    <div className="text-xs text-muted-foreground mt-0.5">
                      {m.envVar && <span className="font-mono">{m.envVar}</span>}
                      {m.signupUrl && <> &middot; <a className="hover:underline" href={m.signupUrl} target="_blank">get key</a></>}
                    </div>
                  </div>
                  <Button variant="outline" size="sm" disabled={testing === m.id} onClick={() => test(m.id)}>
                    {testing === m.id ? 'Testing\u2026' : 'Test'}
                  </Button>
                </div>

                {/* Per-provider config */}
                {!m.isManaged && m.needsKey && (
                  <div className="mt-3">
                    <Label>API key{s.hasKey && !s.apiKey ? ' (configured' + (s.keyFromEnv ? ' via env' : '') + ' \u2014 leave blank to keep)' : ''}</Label>
                    <Input type="password" placeholder={s.hasKey ? '\u2022\u2022\u2022\u2022\u2022' : 'enter key'} value={s.apiKey ?? ''} onChange={(e) => setKey(m.id, e.target.value)} className="mt-1.5 font-mono" />
                  </div>
                )}
                {(m.id === 'searxng' || m.id === 'ollama') && (
                  <div className="mt-3">
                    <Label>Base URL</Label>
                    <Input type="text" value={s.baseURL ?? ''} placeholder={m.id === 'searxng' ? 'http://127.0.0.1:8888' : 'http://localhost:11434'} onChange={(e) => setBaseURL(m.id, e.target.value)} className="mt-1.5 font-mono" />
                  </div>
                )}

                {/* Test result */}
                {tr && (
                  <div className={`mt-2.5 text-xs rounded px-2.5 py-1.5 ${tr.ok ? 'bg-green-500/10 text-green-700 dark:text-green-400' : 'bg-destructive/10 text-destructive'}`}>
                    {tr.ok ? `${tr.resultCount ?? 0} results` : tr.error}
                    {tr.ok && tr.sample && tr.sample.length > 0 && (
                      <ul className="mt-1 list-disc list-inside text-muted-foreground">
                        {tr.sample.slice(0, 3).map((r, i) => <li key={i} className="truncate"><a className="hover:underline" href={r.url} target="_blank">{r.title}</a></li>)}
                      </ul>
                    )}
                  </div>
                )}
              </div>
            );
          })}
        </div>
      </div>

      <div className="flex gap-2.5 flex-wrap sticky bottom-0 bg-background/80 backdrop-blur py-3">
        <Button onClick={save} disabled={!dirty}>Save</Button>
        <Button variant="outline" disabled={!dirty} onClick={load}>Discard</Button>
      </div>
    </>
  );
}

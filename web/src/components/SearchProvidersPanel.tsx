import { useEffect, useState, useCallback } from 'react';
import { api, apiPost, apiPut } from '../api';
import { useToast } from '../ToastContext';
import { Button } from '@/components/ui/button';
import { Input } from '@/components/ui/input';
import { Label } from '@/components/ui/label';
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select';
import { Switch } from '@/components/ui/switch';
import { Separator } from '@/components/ui/separator';

// Built-in provider catalog is returned by /admin/search/providers.
// Each entry: { id, name, needsKey, envVar, signupUrl, isManaged }
type ProviderMeta = {
  id: string;
  name: string;
  needsKey: boolean;
  envVar?: string;
  signupUrl?: string;
  isManaged?: boolean; // searxng (Prism runs the instance)
};

type ProviderState = {
  enabled: boolean;
  apiKey?: string;      // write-only: backend returns "" or "••••" sentinel
  baseURL?: string;     // searxng / ollama only
  hasKey?: boolean;     // backend-reported: a key is configured (env or stored)
  keyFromEnv?: boolean;
};

type SearchConfig = {
  active: string;
  fallback: string[];
  maxPerTurn: number;
  timeoutMs: number;
  defaultNumResults: number;
  providers: Record<string, ProviderState>;
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

export default function SearchProvidersPanel() {
  const { toast } = useToast();
  const [catalog, setCatalog] = useState<ProviderMeta[]>([]);
  const [cfg, setCfg] = useState<SearchConfig | null>(null);
  const [testing, setTesting] = useState<string | null>(null);
  const [testResult, setTestResult] = useState<Record<string, TestResult>>({});
  const [dirty, setDirty] = useState(false);

  const load = useCallback(async () => {
    try {
      const [cats, c] = await Promise.all([api('/search/providers'), api('/search/config')]);
      setCatalog(cats || []);
      setCfg(c);
      setDirty(false);
    } catch (e) {
      toast('Failed to load search config: ' + (e as Error).message, 'error');
    }
  }, [toast]);

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
      toast('Search settings saved.');
      setDirty(false);
      load(); // refresh hasKey flags from backend
    } catch (e) {
      toast('Save failed: ' + (e as Error).message, 'error');
    }
  };

  const test = async (id: string) => {
    setTesting(id);
    try {
      const r: TestResult = await apiPost('/search/test', { provider: id });
      setTestResult((prev) => ({ ...prev, [id]: r }));
      toast(r.ok ? `${id}: ${r.resultCount ?? 0} results` : `${id}: ${r.error ?? 'failed'}`, r.ok ? 'success' : 'error');
    } catch (e) {
      setTestResult((prev) => ({ ...prev, [id]: { ok: false, error: (e as Error).message } }));
      toast(`${id}: ${(e as Error).message}`, 'error');
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

  if (!cfg) return <p className="text-sm text-muted-foreground">Loading\u2026</p>;

  const fallbackOrder = cfg.fallback.filter((id) => cfg.providers[id]?.enabled);

  return (
    <>
      {/* Active + limits */}
      <div className="rounded-xl border border-border bg-card p-6 mb-4">
        <h3 className="text-sm font-semibold tracking-tight mb-1">Search providers</h3>
        <p className="text-[13px] text-muted-foreground mb-4">
          Prism intercepts the built-in web-search tools of Claude Code, ZCode, Codex, and Grok Build and runs them through the active provider here \u2014 so search works on <em>any</em> upstream model, not just Anthropic/OpenAI. Pick a backend, add API keys, and set a fallback order. Keys are stored locally (0600) and never written into agent configs.
        </p>

        <div className="grid grid-cols-2 gap-3.5 mb-4">
          <div>
            <Label>Active provider</Label>
            <Select value={cfg.active} onValueChange={setActive}>
              <SelectTrigger className="w-full mt-1.5"><SelectValue /></SelectTrigger>
              <SelectContent>
                {catalog.map((m) => (
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
              const m = catalog.find((c) => c.id === id);
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
        <h3 className="text-sm font-semibold tracking-tight mb-1">Providers</h3>
        <p className="text-[13px] text-muted-foreground mb-4">Enable a provider and add its API key. Env vars (e.g. <code>EXA_API_KEY</code>) are detected automatically and take precedence.</p>

        <div className="flex flex-col gap-1">
          {catalog.map((m) => {
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

        <Separator className="my-5" />

        {/* Custom provider (declarative \u2014 future-proof) */}
        <h4 className="text-sm font-semibold m-0 pb-2 border-b border-border mb-3">Custom provider (advanced)</h4>
        <p className="text-xs text-muted-foreground mb-2.5">Add any REST search API without a rebuild. Define endpoint, auth header, body template (<code>{'{{query}}'}</code>), results JSON path, and field mapping.</p>
        <Button variant="outline" onClick={() => toast('Custom provider editor coming in slice 5 of the search-providers plan.', 'error')}>+ Add custom provider</Button>
      </div>

      <div className="flex gap-2.5 flex-wrap sticky bottom-0 bg-background/80 backdrop-blur py-3">
        <Button onClick={save} disabled={!dirty}>Save</Button>
        <Button variant="outline" disabled={!dirty} onClick={load}>Discard</Button>
      </div>
    </>
  );
}
import { useEffect, useState, useCallback } from 'react';
import { api, apiPost } from '../api';
import { toast } from './ui/toast';
import { Button } from '@/components/ui/button';
import { Label } from '@/components/ui/label';
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select';
import { AgentIcon } from './agentIcons';
import { SearchIcon, RefreshCwIcon, SlidersHorizontalIcon } from 'lucide-react';
import { Input } from '@/components/ui/input';
import { Drawer, DrawerContent, DrawerHeader, DrawerTitle, DrawerDescription, DrawerFooter, DrawerClose } from '@/components/ui/drawer';

const AGENTS = [
  { id: 'codex', name: 'Codex', desc: 'Makes your Prism models appear in Codex\u2019s native model picker. Requires Codex to be installed.', codex: true },
  { id: 'claude-code', name: 'Claude Code', desc: 'Routes Claude Code through Prism by setting ANTHROPIC_BASE_URL and per-tier model mappings in ~/.claude/settings.local.json. Requires Claude Code to be installed.', hasTiers: true },
  { id: 'factory-droid', name: 'Factory Droid', desc: 'Adds your Prism models as [Prism] custom models in ~/.factory/settings.local.json so they appear in Droid\u2019s /model picker. Requires Factory Droid to be installed.' },
  { id: 'opencode', name: 'OpenCode', desc: 'Registers a prism provider with your Prism base URL in ~/.config/opencode/opencode.json so OpenCode can use your local models. Requires OpenCode to be installed.' },
  { id: 'zcode', name: 'ZCode', desc: 'Registers a prism provider in ~/.zcode/v2/config.json so ZCode can use your local models via Prism. Requires ZCode to be installed.' },
  { id: 'zed', name: 'Zed', desc: 'Registers a prism provider under language_models.openai_compatible in Zed\u2019s settings.json so the Zed Agent can use your local models via Prism. Codex models automatically use the Responses API. Requires Zed to be installed.' },
  { id: 'omp', name: 'Oh My Pi', desc: 'Registers prism and prism-codex providers in ~/.omp/agent/models.yml so Oh My Pi can use your local models via Prism. Requires Oh My Pi (omp) to be installed.' },
  { id: 'grok-build', name: 'Grok Build', desc: 'Registers [model.prism-*] entries in ~/.grok/config.toml so Grok Build can use your local models via Prism. Requires Grok Build (grok) to be installed.' },
  { id: 'pi', name: 'Pi', desc: 'Registers a prism provider in ~/.pi/agent/models.json so Pi can use your local models via Prism. Also sets defaultProvider and defaultModel in ~/.pi/agent/settings.json. Requires Pi to be installed.' },
  { id: 'kimi-code', name: 'Kimi Code', desc: 'Registers a prism provider and model aliases in ~/.kimi-code/config.toml so Kimi Code CLI can use your local models via Prism. Requires Kimi Code (kimi) to be installed.' },
];

const TIER_LABELS: Record<string, string> = { opus: 'Opus tier model', sonnet: 'Sonnet tier model', haiku: 'Haiku tier model', subagent: 'Subagent model' };

type AgentStatus = { installed: boolean; active?: boolean; displayName?: string; tiers?: Record<string, string>; model_options?: string[] };

export default function AgentsPanel() {
  const [statuses, setStatuses] = useState<Record<string, AgentStatus>>({});
  const [claudeCodeTiers, setClaudeCodeTiers] = useState<Record<string, string>>({});
  const [tierOptions, setTierOptions] = useState<string[]>([]);
  const [search, setSearch] = useState('');
  const [tierDrawerOpen, setTierDrawerOpen] = useState(false);

  const checkStatus = useCallback(async (id: string) => {
    try {
      if (id === 'codex') {
        const res = await api('/codex-desktop/status');
        setStatuses(prev => ({ ...prev, codex: res }));
      } else {
        const res = await api('/agent/status?id=' + encodeURIComponent(id));
        setStatuses(prev => ({ ...prev, [id]: res }));
        if (id === 'claude-code' && res.tiers) {
          setClaudeCodeTiers(res.tiers);
          setTierOptions(res.model_options || []);
        }
      }
    } catch { setStatuses(prev => ({ ...prev, [id]: { installed: false } })); }
  }, []);

  useEffect(() => {
    AGENTS.forEach(a => checkStatus(a.id));
  }, [checkStatus]);

  const setup = async (id: string) => {
    try {
      if (id === 'codex') {
        await apiPost('/codex-desktop/setup');
        toast.add({ title: 'Codex configured successfully', type: 'success' });
      } else {
        const opts: RequestInit = { method: 'POST' };
        if (id === 'claude-code') {
          opts.headers = { 'Content-Type': 'application/json' };
          opts.body = JSON.stringify({ tiers: claudeCodeTiers });
        }
        const res = await fetch('/admin/agent/setup?id=' + encodeURIComponent(id), opts);
        const data = await res.json().catch(() => ({}));
        if (!res.ok) { toast.add({ title: data.error || 'Setup failed', type: 'error' }); return; }
        toast.add({ title: (data.displayName || id) + ' configured successfully', type: 'success' });
      }
      checkStatus(id);
    } catch (e) { toast.add({ title: 'Setup failed: ' + (e as Error).message, type: 'error' }); }
  };

  const disable = async (id: string, name: string) => {
    if (!confirm('Remove Prism configuration from ' + name + '?')) return;
    try {
      if (id === 'codex') {
        await apiPost('/codex-desktop/restore');
        toast.add({ title: 'Codex configuration restored', type: 'success' });
      } else {
        const res = await fetch('/admin/agent/restore?id=' + encodeURIComponent(id), { method: 'POST' });
        const data = await res.json().catch(() => ({}));
        if (!res.ok) { toast.add({ title: data.error || 'Restore failed', type: 'error' }); return; }
        toast.add({ title: id + ' configuration restored', type: 'success' });
      }
      checkStatus(id);
    } catch (e) { toast.add({ title: 'Restore failed: ' + (e as Error).message, type: 'error' }); }
  };

  const saveTiers = async () => {
    await setup('claude-code');
    setTierDrawerOpen(false);
  };

  const badge = (s: AgentStatus | undefined) => {
    if (!s) return <span className="text-[11px] text-muted-foreground">Checking…</span>;
    if (!s.installed) return <span className="text-[11px] text-muted-foreground">Not installed</span>;
    if (s.active) return <span className="inline-flex items-center gap-1 text-[11px] text-green-600 dark:text-green-500"><span className="size-1.5 rounded-full bg-green-500"/>Active</span>;
    return <span className="text-[11px] text-amber-600 dark:text-amber-500">Not configured</span>;
  };

  const filtered = AGENTS.filter(a => (a.name + ' ' + a.desc).toLowerCase().includes(search.toLowerCase()));

  return (
    <>
      <div className="relative mb-4 w-full">
        <SearchIcon className="absolute left-3 top-1/2 -translate-y-1/2 size-4 text-muted-foreground" />
        <Input className="pl-9" placeholder="Search agents..." value={search} onChange={(e) => setSearch(e.target.value)} />
      </div>

      {filtered.length === 0 ? (
        <p className="text-sm text-muted-foreground">No agents found.</p>
      ) : (
        <div className="grid grid-cols-1 sm:grid-cols-2 gap-x-10 gap-y-3">
          {filtered.map(agent => {
            const s = statuses[agent.id];
            const installed = s?.installed;
            const active = s?.active;
            return (
              <div className="flex items-start gap-3 py-2" key={agent.id}>
                <AgentIcon id={agent.id} />
                <div className="min-w-0 flex-1">
                  <div className="flex items-center gap-2">
                    <h3 className="text-sm font-semibold tracking-tight truncate">{agent.name}</h3>
                    {badge(s)}
                  </div>
                  <p className="text-[13px] text-muted-foreground line-clamp-2 mt-1">{agent.desc}</p>
                </div>
                <div className="shrink-0 self-stretch flex flex-col items-end gap-1.5">
                  <div className="flex items-center gap-2">
                  {!installed ? (
                    <Button size="sm" variant="secondary" className="bg-[#f5f5f5] text-foreground hover:bg-[#ebebeb] dark:bg-secondary dark:text-secondary-foreground dark:hover:bg-secondary/80" disabled>Setup</Button>
                  ) : active ? (
                    <>
                      {agent.hasTiers && (
                        <Button size="icon-sm" variant="ghost" title="Edit model variants" onClick={() => setTierDrawerOpen(true)}>
                          <SlidersHorizontalIcon />
                        </Button>
                      )}
                      <Button size="sm" variant="secondary" className="bg-[#f5f5f5] text-foreground hover:bg-[#ebebeb] dark:bg-secondary dark:text-secondary-foreground dark:hover:bg-secondary/80" onClick={() => disable(agent.id, agent.name)}>Disable</Button>
                    </>
                  ) : (
                    <Button size="sm" variant="secondary" className="bg-[#f5f5f5] text-foreground hover:bg-[#ebebeb] dark:bg-secondary dark:text-secondary-foreground dark:hover:bg-secondary/80" onClick={() => setup(agent.id)}>Setup</Button>
                  )}
                  </div>
                  <Button
                    size="icon-xs"
                    variant="ghost"
                    className="mt-auto opacity-40 hover:opacity-100 dark:opacity-50 dark:hover:opacity-100 disabled:opacity-20"
                    title="Retry setup"
                    aria-label={`Retry setup for ${agent.name}`}
                    onClick={() => setup(agent.id)}
                    disabled={!installed}
                  >
                    <RefreshCwIcon />
                  </Button>
                </div>
              </div>
            );
          })}
        </div>
      )}

      <Drawer open={tierDrawerOpen} onOpenChange={setTierDrawerOpen} direction="right">
        <DrawerContent>
          <DrawerHeader>
            <DrawerTitle>Claude Code model variants</DrawerTitle>
            <DrawerDescription>Map each Claude Code tier to a Prism model. Saving re-applies the Claude Code config.</DrawerDescription>
          </DrawerHeader>
          <div className="px-4 pb-4 flex-1 overflow-y-auto">
            {Object.entries(TIER_LABELS).map(([k, label]) => (
              <div className="mb-4 last:mb-0" key={k}>
                <Label>{label}</Label>
                <Select value={claudeCodeTiers[k] || ''} onValueChange={(v) => setClaudeCodeTiers(prev => ({ ...prev, [k]: v }))}>
                  <SelectTrigger className="w-full mt-1.5"><SelectValue placeholder="Select a model..." /></SelectTrigger>
                  <SelectContent>
                    <SelectItem value="">Select a model...</SelectItem>
                    {tierOptions.map(m => <SelectItem key={m} value={m}>{m}</SelectItem>)}
                  </SelectContent>
                </Select>
              </div>
            ))}
          </div>
          <DrawerFooter>
            <Button onClick={saveTiers}>Save</Button>
            <DrawerClose asChild><Button variant="outline">Cancel</Button></DrawerClose>
          </DrawerFooter>
        </DrawerContent>
      </Drawer>
    </>
  );
}

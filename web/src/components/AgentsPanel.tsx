import { useEffect, useState, useCallback } from 'react';
import { api, apiPost } from '../api';
import { useToast } from '../ToastContext';
import { Button } from '@/components/ui/button';
import { Label } from '@/components/ui/label';
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select';

const AGENTS = [
  { id: 'claude-code', name: 'Claude Code', desc: 'Routes Claude Code through Prism by setting ANTHROPIC_BASE_URL and per-tier model mappings in ~/.claude/settings.local.json. Requires Claude Code to be installed.', hasTiers: true },
  { id: 'factory-droid', name: 'Factory Droid', desc: 'Adds your Prism models as [Prism] custom models in ~/.factory/settings.local.json so they appear in Droid\u2019s /model picker. Requires Factory Droid to be installed.' },
  { id: 'opencode', name: 'OpenCode', desc: 'Registers a prism provider with your Prism base URL in ~/.config/opencode/opencode.json so OpenCode can use your local models. Requires OpenCode to be installed.' },
  { id: 'zcode', name: 'ZCode', desc: 'Registers a prism provider in ~/.zcode/v2/config.json so ZCode can use your local models via Prism. Requires ZCode to be installed.' },
  { id: 'omp', name: 'Oh My Pi', desc: 'Registers prism and prism-codex providers in ~/.omp/agent/models.yml so Oh My Pi can use your local models via Prism. Requires Oh My Pi (omp) to be installed.' },
  { id: 'grok-build', name: 'Grok Build', desc: 'Registers [model.prism-*] entries in ~/.grok/config.toml so Grok Build can use your local models via Prism. Requires Grok Build (grok) to be installed.' },
];

const TIER_LABELS: Record<string, string> = { opus: 'Opus tier model', sonnet: 'Sonnet tier model', haiku: 'Haiku tier model', subagent: 'Subagent model' };

export default function AgentsPanel() {
  const { toast } = useToast();
  const [codexStatus, setCodexStatus] = useState<any>(null);
  const [agentStatuses, setAgentStatuses] = useState<Record<string, any>>({});
  const [claudeCodeTiers, setClaudeCodeTiers] = useState<Record<string, string>>({});
  const [tierOptions, setTierOptions] = useState<string[]>([]);

  const checkCodex = useCallback(async () => {
    try { setCodexStatus(await api('/codex-desktop/status')); } catch { setCodexStatus({ installed: false }); }
  }, []);

  const checkAgent = useCallback(async (id: string) => {
    try {
      const res = await api('/agent/status?id=' + encodeURIComponent(id));
      setAgentStatuses(prev => ({ ...prev, [id]: res }));
      if (id === 'claude-code' && res.tiers) {
        setClaudeCodeTiers(res.tiers);
        setTierOptions(res.model_options || []);
      }
    } catch { setAgentStatuses(prev => ({ ...prev, [id]: { installed: false } })); }
  }, []);

  useEffect(() => {
    checkCodex();
    AGENTS.forEach(a => checkAgent(a.id));
  }, [checkCodex, checkAgent]);

  const setupCodex = async () => {
    try { await apiPost('/codex-desktop/setup'); toast('Codex Desktop configured successfully'); checkCodex(); }
    catch (e) { toast('Setup failed: ' + (e as Error).message, 'error'); }
  };

  const restoreCodex = async () => {
    if (!confirm('Remove Prism configuration from Codex Desktop?')) return;
    try { await apiPost('/codex-desktop/restore'); toast('Codex Desktop configuration restored'); checkCodex(); }
    catch (e) { toast('Restore failed: ' + (e as Error).message, 'error'); }
  };

  const setupAgent = async (id: string) => {
    try {
      const opts: RequestInit = { method: 'POST' };
      if (id === 'claude-code') {
        opts.headers = { 'Content-Type': 'application/json' };
        opts.body = JSON.stringify({ tiers: claudeCodeTiers });
      }
      const res = await fetch('/admin/agent/setup?id=' + encodeURIComponent(id), opts);
      const data = await res.json().catch(() => ({}));
      if (!res.ok) { toast(data.error || 'Setup failed', 'error'); return; }
      toast((data.displayName || id) + ' configured successfully');
      checkAgent(id);
    } catch (e) { toast('Setup failed: ' + (e as Error).message, 'error'); }
  };

  const restoreAgent = async (id: string) => {
    if (!confirm('Remove Prism configuration from ' + id + '?')) return;
    try {
      const res = await fetch('/admin/agent/restore?id=' + encodeURIComponent(id), { method: 'POST' });
      const data = await res.json().catch(() => ({}));
      if (!res.ok) { toast(data.error || 'Restore failed', 'error'); return; }
      toast(id + ' configuration restored');
      checkAgent(id);
    } catch (e) { toast('Restore failed: ' + (e as Error).message, 'error'); }
  };

  const statusHTML = (s: any, displayName?: string) => {
    if (!s) return <span className="text-muted-foreground">Checking...</span>;
    if (!s.installed) return <span className="text-muted-foreground">{displayName || s.displayName || 'Not detected'}</span>;
    if (s.active) return <span className="text-green-500">Active — routed through Prism</span>;
    return <span className="text-amber-500">Installed but not configured</span>;
  };

  return (
    <>
      <h3 className="text-base font-semibold mb-4">Agent Integrations</h3>

      <div className="rounded-xl border border-border bg-card p-6 mb-4">
        <h3 className="text-sm font-semibold tracking-tight mb-1">Codex Desktop Integration</h3>
        <p className="text-[13px] text-muted-foreground mb-3">Makes your Prism models appear in Codex Desktop's native model picker. Requires Codex Desktop to be installed.</p>
        <div className="my-3 text-[13px] text-muted-foreground">
          {codexStatus ? (
            !codexStatus.installed ? <span>Codex Desktop not detected</span> :
            codexStatus.active ? <span className="text-green-500">Active — models synced to Codex Desktop</span> :
            <span className="text-amber-500">Installed but not configured</span>
          ) : 'Checking...'}
        </div>
        <div className="flex gap-2.5 flex-wrap">
          <Button onClick={setupCodex}>Setup</Button>
          <Button variant="outline" onClick={restoreCodex}>Restore</Button>
        </div>
      </div>

      {AGENTS.map(agent => (
        <div className="rounded-xl border border-border bg-card p-6 mb-4" key={agent.id}>
          <h3 className="text-sm font-semibold tracking-tight mb-1">{agent.name}</h3>
          <p className="text-[13px] text-muted-foreground mb-3">{agent.desc}</p>
          <div id={`agent-status-${agent.id}`} className="my-3 text-[13px] text-muted-foreground">
            {statusHTML(agentStatuses[agent.id], agent.name)}
          </div>
          {agent.hasTiers && (
            <>
              {Object.entries(TIER_LABELS).map(([k, label]) => (
                <div className="mb-5 last:mb-0" key={k}>
                  <Label>{label}</Label>
                  <Select value={claudeCodeTiers[k] || ''} onValueChange={(v) => setClaudeCodeTiers(prev => ({ ...prev, [k]: v }))}>
                    <SelectTrigger className="w-full mt-1.5">
                      <SelectValue placeholder="Select a model..." />
                    </SelectTrigger>
                    <SelectContent>
                      <SelectItem value="">Select a model...</SelectItem>
                      {tierOptions.map(m => <SelectItem key={m} value={m}>{m}</SelectItem>)}
                    </SelectContent>
                  </Select>
                </div>
              ))}
            </>
          )}
          <div className="flex gap-2.5 flex-wrap">
            <Button onClick={() => setupAgent(agent.id)}>Setup</Button>
            <Button variant="outline" onClick={() => restoreAgent(agent.id)}>Restore</Button>
          </div>
        </div>
      ))}
    </>
  );
}

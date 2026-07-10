import { useEffect, useState, useCallback } from 'react';
import { api, apiPost } from '../api';
import { useToast } from '../ToastContext';

const AGENTS = [
  { id: 'claude-code', name: 'Claude Code', desc: 'Routes Claude Code through Prism by setting ANTHROPIC_BASE_URL and per-tier model mappings in ~/.claude/settings.json. Requires Claude Code to be installed.', hasTiers: true },
  { id: 'factory-droid', name: 'Factory Droid', desc: 'Adds your Prism models as [Prism] custom models in ~/.factory/settings.json so they appear in Droid\u2019s /model picker. Requires Factory Droid to be installed.' },
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
    if (!s) return <span style={{ color: 'var(--text-secondary)' }}>Checking...</span>;
    if (!s.installed) return <span style={{ color: 'var(--text-secondary)' }}>{displayName || s.displayName || 'Not detected'}</span>;
    if (s.active) return <span style={{ color: 'var(--success)' }}>Active \u2014 routed through Prism</span>;
    return <span style={{ color: '#f59e0b' }}>Installed but not configured</span>;
  };

  return (
    <>
      <h3 style={{ fontSize: '16px', fontWeight: 600, marginBottom: '16px' }}>Agent Integrations</h3>

      <div className="card">
        <h3>Codex Desktop Integration</h3>
        <p className="card-description">Makes your Prism models appear in Codex Desktop\u2019s native model picker. Requires Codex Desktop to be installed.</p>
        <div style={{ margin: '12px 0', fontSize: '13px', color: 'var(--text-secondary)' }}>
          {codexStatus ? (
            !codexStatus.installed ? <span>Codex Desktop not detected</span> :
            codexStatus.active ? <span style={{ color: 'var(--success)' }}>Active \u2014 models synced to Codex Desktop</span> :
            <span style={{ color: '#f59e0b' }}>Installed but not configured</span>
          ) : 'Checking...'}
        </div>
        <div className="btn-row">
          <button className="btn btn-primary" onClick={setupCodex}>Setup</button>
          <button className="btn btn-ghost" onClick={restoreCodex}>Restore</button>
        </div>
      </div>

      {AGENTS.map(agent => (
        <div className="card" key={agent.id}>
          <h3>{agent.name}</h3>
          <p className="card-description">{agent.desc}</p>
          <div id={`agent-status-${agent.id}`} style={{ margin: '12px 0', fontSize: '13px', color: 'var(--text-secondary)' }}>
            {statusHTML(agentStatuses[agent.id], agent.name)}
          </div>
          {agent.hasTiers && (
            <>
              {Object.entries(TIER_LABELS).map(([k, label]) => (
                <div className="field" key={k}>
                  <label>{label}</label>
                  <select value={claudeCodeTiers[k] || ''} onChange={e => setClaudeCodeTiers(prev => ({ ...prev, [k]: e.target.value }))}>
                    <option value="">Select a model...</option>
                    {tierOptions.map(m => <option key={m} value={m}>{m}</option>)}
                  </select>
                </div>
              ))}
            </>
          )}
          <div className="btn-row">
            <button className="btn btn-primary" onClick={() => setupAgent(agent.id)}>Setup</button>
            <button className="btn btn-ghost" onClick={() => restoreAgent(agent.id)}>Restore</button>
          </div>
        </div>
      ))}
    </>
  );
}

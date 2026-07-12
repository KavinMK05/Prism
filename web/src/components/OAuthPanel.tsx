import { useEffect, useState, useCallback } from 'react';
import { useToast } from '../ToastContext';
import { Button } from '@/components/ui/button';

function fmtReset(ts: number): string {
  if (!ts) return '';
  const d = new Date(ts * 1000);
  const diff = d.getTime() - Date.now();
  if (diff <= 0) return 'resetting soon';
  const hrs = Math.floor(diff / 3600000);
  const mins = Math.floor((diff % 3600000) / 60000);
  if (hrs > 24) return 'resets in ' + Math.floor(hrs / 24) + 'd ' + (hrs % 24) + 'h';
  if (hrs > 0) return 'resets in ' + hrs + 'h ' + mins + 'm';
  return 'resets in ' + mins + 'm';
}

function planLabel(rawPlan: string): string {
  if (rawPlan === 'prolite') return 'Pro 5x';
  if (rawPlan === 'pro') return 'Pro 20x';
  if (rawPlan === 'plus') return 'Plus';
  if (rawPlan === 'team') return 'Team';
  if (rawPlan === 'free') return 'Free';
  if (rawPlan && rawPlan !== 'Unknown' && rawPlan !== 'codex') return rawPlan.charAt(0).toUpperCase() + rawPlan.slice(1);
  return 'ChatGPT';
}

export default function OAuthPanel() {
  const { toast } = useToast();
  const [accounts, setAccounts] = useState<any[]>([]);
  const [, setForceRender] = useState(0);

  const loadAccounts = useCallback(async () => {
    try {
      const accs = await fetch('/admin/oauth/accounts').then(r => r.json());
      setAccounts(accs || []);
    } catch { /* ignore */ }
  }, []);

  useEffect(() => { loadAccounts(); }, [loadAccounts]);

  const addCodexAccount = async () => {
    try {
      const res = await fetch('/admin/oauth/login', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ provider: 'codex' }) });
      if (!res.ok) { const text = await res.text(); try { toast(JSON.parse(text).error || text, 'error'); } catch { toast(text, 'error'); } return; }
      alert('Your browser has been opened for Codex login. Please complete the sign-in and return here.');
      let attempts = 0;
      const poll = setInterval(() => {
        attempts++;
        if (attempts > 60) { clearInterval(poll); return; }
        fetch('/admin/oauth/accounts').then(r => r.json()).then(accs => { if (accs.length > 0) { clearInterval(poll); loadAccounts(); } });
      }, 2000);
    } catch (e) { alert('Failed to start OAuth: ' + (e as Error).message); }
  };

  const removeAccount = async (id: string) => {
    if (!confirm('Remove this OAuth account?')) return;
    try {
      const res = await fetch('/admin/oauth/accounts/remove', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ id }) });
      if (!res.ok) { const text = await res.text(); try { toast(JSON.parse(text).error || text, 'error'); } catch { toast(text, 'error'); } return; }
      loadAccounts();
    } catch (e) { alert('Failed to remove account: ' + (e as Error).message); }
  };

  const activateAccount = async (id: string) => {
    try {
      const res = await fetch('/admin/oauth/accounts/activate', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ id }) });
      if (!res.ok) { const text = await res.text(); try { toast(JSON.parse(text).error || text, 'error'); } catch { toast(text, 'error'); } return; }
      loadAccounts();
    } catch (e) { alert('Failed to activate account: ' + (e as Error).message); }
  };

  const refreshUsage = async () => {
    const active = accounts.find(a => a.active);
    if (!active) return;
    try {
      await fetch('/admin/oauth/usage/refresh', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ account_id: active.id }) });
      loadAccounts();
      setForceRender(n => n + 1);
    } catch { /* ignore */ }
  };

  const activeAccount = accounts.find(a => a.active);

  const renderUsageBar = (label: string, pct: number, resetTs: number, period: string) => {
    const barColor = pct > 80 ? 'bg-destructive' : pct > 50 ? 'bg-amber-500' : 'bg-green-500';
    const resetTxt = fmtReset(resetTs);
    return (
      <div key={label} className="mb-2.5">
        <div className="flex justify-between items-center mb-1">
          <span className="text-[13px] font-medium">{label}</span>
          <span className="text-[11px] text-muted-foreground">{resetTxt || period || ''}</span>
        </div>
        <div className="w-full h-5 bg-muted rounded-sm overflow-hidden relative cursor-pointer" onClick={refreshUsage}>
          <div className={`h-full rounded-sm transition-[width] duration-400 ${barColor}`} style={{ width: `${Math.min(pct, 100)}%` }} />
          <div className="absolute top-1/2 left-1/2 -translate-x-1/2 -translate-y-1/2 text-[11px] font-semibold text-foreground" style={{ textShadow: '0 0 4px var(--surface)' }}>{pct.toFixed(0)}% used</div>
        </div>
      </div>
    );
  };

  let usageBars: JSX.Element[] = [];
  if (activeAccount && !activeAccount.usage_unavailable) {
    if (activeAccount.session_percent != null && activeAccount.session_percent > 0) usageBars.push(renderUsageBar('Session (5h)', activeAccount.session_percent, activeAccount.session_reset_at, '5h window'));
    if (activeAccount.weekly_percent != null && activeAccount.weekly_percent > 0) usageBars.push(renderUsageBar('Weekly (7d)', activeAccount.weekly_percent, activeAccount.weekly_reset_at, '7d window'));
    if (activeAccount.review_percent != null && activeAccount.review_percent > 0) usageBars.push(renderUsageBar('Code Reviews', activeAccount.review_percent, 0, '7d window'));
  }

  let detailParts: string[] = [];
  if (activeAccount) {
    if (activeAccount.credits_balance != null && activeAccount.credits_balance > 0) detailParts.push(`Credits: $${(activeAccount.credits_balance * 0.04).toFixed(2)} \u00b7 ${Math.floor(activeAccount.credits_balance)} credits`);
    if (activeAccount.rate_limit_resets != null && activeAccount.rate_limit_resets > 0) detailParts.push(`${activeAccount.rate_limit_resets} rate limit reset${activeAccount.rate_limit_resets > 1 ? 's' : ''} available`);
  }

  return (
    <>
      <div className="rounded-xl border border-border bg-card p-6 mb-4">
        <h3 className="text-sm font-semibold tracking-tight mb-1">Connected Accounts</h3>
        <p className="text-[13px] text-muted-foreground mb-4">Manage your OAuth-connected accounts for subscription-based access.</p>
        {accounts.length === 0 ? (
          <div className="text-muted-foreground text-sm py-4 text-center">No OAuth accounts connected yet. Click &quot;Add Codex Account&quot; to get started.</div>
        ) : accounts.map(a => (
          <div key={a.id} className={`flex items-center justify-between px-4 py-3.5 border border-border rounded-md mb-2.5 transition-colors hover:border-border-strong hover:bg-accent ${a.active ? '!border-green-500/50 bg-green-500/5' : ''}`}>
            <div className="flex-1 min-w-0">
              <div className="text-sm font-medium">{a.email || a.label || a.id}</div>
              <div className="text-xs text-muted-foreground mt-0.5">{planLabel(a.plan_tier || '')} &middot; Token: {a.token_valid ? <span className="text-green-500">Valid</span> : <span className="text-destructive">Expired</span>}{a.token_expiry ? ' \u00b7 Expires: ' + a.token_expiry : ''}</div>
            </div>
            <div className="flex gap-2 items-center">
              <Button variant="secondary" size="sm" disabled={a.active} onClick={() => activateAccount(a.id)}>{a.active ? 'Active' : 'Activate'}</Button>
              <Button variant="destructive" size="sm" onClick={() => removeAccount(a.id)}>Remove</Button>
            </div>
          </div>
        ))}
        <div className="flex gap-2.5 mt-4 flex-wrap">
          <Button onClick={addCodexAccount}>+ Add Codex Account</Button>
        </div>
      </div>

      {activeAccount && (
        <div className="rounded-xl border border-border bg-card p-6 mb-4">
          <h3 className="text-sm font-semibold tracking-tight mb-1">Usage</h3>
          <p className="text-[13px] text-muted-foreground mb-3">Usage for {activeAccount.email || activeAccount.label || activeAccount.id}</p>
          {activeAccount.usage_unavailable ? (
            <div className="py-3 text-muted-foreground text-[13px] leading-relaxed">
              Usage data is temporarily unavailable. The ChatGPT usage endpoint may be rate-limited or the token may need refreshing.
            </div>
          ) : usageBars.length === 0 ? (
            <div className="py-2 text-muted-foreground text-[13px]">No usage data yet. Usage will appear after your first API call.</div>
          ) : (
            <div className="mt-3">{usageBars}</div>
          )}
          {detailParts.length > 0 && <div className="text-xs text-muted-foreground mt-2">{detailParts.join(' \u00b7 ')}</div>}
          <div className="flex gap-2.5 mt-3 flex-wrap">
            <Button variant="secondary" onClick={refreshUsage}>&#8635; Refresh Usage</Button>
          </div>
        </div>
      )}
    </>
  );
}

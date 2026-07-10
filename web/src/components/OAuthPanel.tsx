import { useEffect, useState, useCallback } from 'react';
import { useToast } from '../ToastContext';

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
    const cl = pct > 80 ? 'high' : pct > 50 ? 'medium' : 'low';
    const resetTxt = fmtReset(resetTs);
    return (
      <div key={label} style={{ marginBottom: '10px' }}>
        <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', marginBottom: '4px' }}>
          <span style={{ fontSize: '13px', fontWeight: 500 }}>{label}</span>
          <span style={{ fontSize: '11px', color: 'var(--text-secondary)' }}>{resetTxt || period || ''}</span>
        </div>
        <div className="usage-bar-bg" onClick={refreshUsage} style={{ cursor: 'pointer' }}>
          <div className={`usage-bar-fill ${cl}`} style={{ width: `${Math.min(pct, 100)}%` }} />
          <div className="usage-bar-label">{pct.toFixed(0)}% used</div>
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
      <div className="card">
        <h3>Connected Accounts</h3>
        <p className="card-description">Manage your OAuth-connected accounts for subscription-based access.</p>
        {accounts.length === 0 ? (
          <div style={{ color: 'var(--text-secondary)', fontSize: '14px', padding: '16px 0', textAlign: 'center' }}>No OAuth accounts connected yet. Click &quot;Add Codex Account&quot; to get started.</div>
        ) : accounts.map(a => (
          <div key={a.id} className={`oauth-account${a.active ? ' active' : ''}`}>
            <div className="oauth-account-info">
              <div className="oauth-account-email">{a.email || a.label || a.id}</div>
              <div className="oauth-account-meta">{planLabel(a.plan_tier || '')} &middot; Token: {a.token_valid ? <span style={{ color: 'var(--success)' }}>Valid</span> : <span style={{ color: 'var(--danger)' }}>Expired</span>}{a.token_expiry ? ' &middot; Expires: ' + a.token_expiry : ''}</div>
            </div>
            <div className="oauth-account-actions">
              <button className="btn btn-secondary" disabled={a.active} onClick={() => activateAccount(a.id)}>{a.active ? 'Active' : 'Activate'}</button>
              <button className="btn btn-danger" onClick={() => removeAccount(a.id)}>Remove</button>
            </div>
          </div>
        ))}
        <div className="btn-row" style={{ marginTop: '16px' }}>
          <button className="btn btn-primary" onClick={addCodexAccount}>+ Add Codex Account</button>
        </div>
      </div>

      {activeAccount && (
        <div className="card">
          <h3>Usage</h3>
          <p className="card-description">Usage for {activeAccount.email || activeAccount.label || activeAccount.id}</p>
          {activeAccount.usage_unavailable ? (
            <div style={{ padding: '12px 0', color: 'var(--text-secondary)', fontSize: '13px', lineHeight: 1.5 }}>
              Usage data is temporarily unavailable. The ChatGPT usage endpoint may be rate-limited or the token may need refreshing.
            </div>
          ) : usageBars.length === 0 ? (
            <div style={{ padding: '8px 0', color: 'var(--text-secondary)', fontSize: '13px' }}>No usage data yet. Usage will appear after your first API call.</div>
          ) : (
            <div style={{ marginTop: '12px' }}>{usageBars}</div>
          )}
          {detailParts.length > 0 && <div className="usage-detail" style={{ marginTop: '8px' }} dangerouslySetInnerHTML={{ __html: detailParts.join(' &middot; ') }} />}
          <div className="btn-row" style={{ marginTop: '12px' }}>
            <button className="btn btn-secondary" onClick={refreshUsage}>&#8635; Refresh Usage</button>
          </div>
        </div>
      )}
    </>
  );
}

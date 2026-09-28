import { useEffect, useState } from 'react';
import { api, apiPut } from '../api';
import { Button } from '@/components/ui/button';

const TELEMETRY_URL = 'https://github.com/KavinMK05/Prism/blob/main/TELEMETRY.md';

interface AnalyticsState {
  opt_out: boolean;
  notice_seen: boolean;
  forced_off: boolean;
}

/**
 * One-time first-run notice for anonymous analytics. Telemetry is on by
 * default, so this banner tells the user exactly what is sent and lets them
 * turn it off in one click. Shown until the notice is acknowledged, then never
 * again. Hidden entirely when the PRISM_ANALYTICS_DISABLED kill switch is set.
 */
export default function AnalyticsBanner() {
  const [state, setState] = useState<AnalyticsState | null>(null);

  useEffect(() => {
    api('/analytics/settings')
      .then((s) => setState(s))
      .catch(() => setState({ opt_out: false, notice_seen: true, forced_off: true }));
  }, []);

  if (!state || state.notice_seen || state.forced_off) return null;

  const decide = async (optOut: boolean) => {
    // Optimistically hide; the choice is persisted server-side.
    setState({ opt_out: optOut, notice_seen: true, forced_off: false });
    try {
      await apiPut('/analytics/settings', { opt_out: optOut, notice_seen: true });
    } catch {
      // If the save failed, show the banner again next load.
      setState({ opt_out: false, notice_seen: false, forced_off: false });
    }
  };

  return (
    <div className="rounded-xl border border-border bg-card p-4 mb-4 flex items-start justify-between gap-4">
      <div className="min-w-0">
        <div className="text-sm font-semibold tracking-tight">Anonymous analytics is on</div>
        <p className="text-xs text-muted-foreground mt-1">
          Prism sends one anonymous event per day &mdash; app version, OS, whether Prism proxied
          anything in the last 24 hours, and a rough request-count bucket &mdash; so we can count
          active installs. No prompts, models, or requests are ever sent.{' '}
          <a
            href={TELEMETRY_URL}
            target="_blank"
            rel="noopener noreferrer"
            className="underline underline-offset-2 hover:text-foreground"
          >
            Learn what&apos;s sent
          </a>
        </p>
      </div>
      <div className="flex items-center gap-2 shrink-0">
        <Button size="sm" onClick={() => decide(false)}>Got it</Button>
        <Button size="sm" variant="outline" onClick={() => decide(true)}>Turn off analytics</Button>
        <button
          className="w-7 h-7 rounded-md text-muted-foreground hover:text-foreground hover:bg-accent flex items-center justify-center transition-colors"
          onClick={() => decide(false)}
          title="Dismiss"
          aria-label="Dismiss"
        >
          <svg className="w-4 h-4" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round">
            <line x1="18" y1="6" x2="6" y2="18" />
            <line x1="6" y1="6" x2="18" y2="18" />
          </svg>
        </button>
      </div>
    </div>
  );
}

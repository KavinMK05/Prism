import { useEffect, useState } from 'react';
import { api, apiPut } from '../api';
import { Button } from '@/components/ui/button';

const TELEMETRY_URL = 'https://github.com/KavinMK05/Prism/blob/main/TELEMETRY.md';

interface AnalyticsState {
  opt_in: boolean;
  prompted: boolean;
}

/**
 * One-time first-run consent banner for anonymous analytics. Shown at the top
 * of the admin shell on every tab until the user has been asked (prompted).
 * [Enable] opts in; [Not now] / [x] permanently decline. Never re-prompted.
 */
export default function AnalyticsBanner() {
  const [state, setState] = useState<AnalyticsState | null>(null);

  useEffect(() => {
    api('/analytics/settings')
      .then((s) => setState(s))
      .catch(() => setState({ opt_in: false, prompted: true }));
  }, []);

  if (!state || state.prompted) return null;

  const decide = async (optIn: boolean) => {
    // Optimistically hide; the choice is persisted server-side.
    setState({ opt_in: optIn, prompted: true });
    try {
      await apiPut('/analytics/settings', { opt_in: optIn, prompted: true });
    } catch {
      // If the save failed, show the banner again next load.
      setState({ opt_in: false, prompted: false });
    }
  };

  return (
    <div className="rounded-xl border border-border bg-card p-4 mb-4 flex items-start justify-between gap-4">
      <div className="min-w-0">
        <div className="text-sm font-semibold tracking-tight">Help count Prism installs?</div>
        <p className="text-xs text-muted-foreground mt-1">
          Prism is open source. Send an anonymous daily ping (app version + OS) so we can count active
          installs. No prompts, models, or requests are ever sent.{' '}
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
        <Button size="sm" onClick={() => decide(true)}>Enable</Button>
        <Button size="sm" variant="outline" onClick={() => decide(false)}>Not now</Button>
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

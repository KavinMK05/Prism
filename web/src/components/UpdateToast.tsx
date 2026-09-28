import { useRef, useState } from 'react';
import { Button } from '@/components/ui/button';
import { DownloadIcon, type UpdateStatus } from '../lib/useUpdateStatus.tsx';

interface UpdateToastProps {
  status: UpdateStatus | null;
  install: () => Promise<void>;
}

export default function UpdateToast({ status, install }: UpdateToastProps) {
  const [dismissed, setDismissed] = useState(false);
  const [starting, setStarting] = useState(false);
  const seenVersion = useRef<string | null>(null);

  if (!status || dismissed) return null;

  const { state, version, current } = status;

  // Auto-dismiss once the user has seen this particular version's toast.
  if (version && seenVersion.current === null) seenVersion.current = version;
  if (version && seenVersion.current !== version) {
    seenVersion.current = version;
    setDismissed(false);
    setStarting(false);
  }

  const startInstall = async () => {
    setStarting(true);
    try {
      await install();
      // App relaunches during install; keep the toast up in case it doesn't.
    } catch {
      setStarting(false);
    }
  };

  if (state === 'available' && version !== current) {
    return (
      <div className="fixed bottom-5 right-5 z-50 rounded-xl border border-border bg-card shadow-lg p-4 flex items-center gap-3 animate-in fade-in slide-in-from-bottom-4 duration-300">
        <span className="relative flex h-2.5 w-2.5 shrink-0">
          <span className="animate-ping absolute inline-flex h-full w-full rounded-full bg-green-500 opacity-60" />
          <span className="relative inline-flex rounded-full h-2.5 w-2.5 bg-green-500" />
        </span>
        <div className="min-w-0 text-sm">
          <span className="font-semibold tracking-tight">Update available</span>
          <span className="text-muted-foreground"> &mdash; v{version.replace(/^v/, '')} is ready to install</span>
        </div>
        <Button size="sm" disabled={starting} onClick={startInstall} title={`Download and install v${version.replace(/^v/, '')}`}>
          <DownloadIcon />
          Update
        </Button>
        <button
          className="w-7 h-7 rounded-md text-muted-foreground hover:text-foreground hover:bg-accent flex items-center justify-center transition-colors"
          onClick={() => setDismissed(true)}
          title="Dismiss"
          aria-label="Dismiss"
        >
          <svg className="w-4 h-4" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round">
            <line x1="18" y1="6" x2="6" y2="18" />
            <line x1="6" y1="6" x2="18" y2="18" />
          </svg>
        </button>
      </div>
    );
  }

  return null;
}

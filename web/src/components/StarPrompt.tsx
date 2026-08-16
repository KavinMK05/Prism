import { useEffect } from 'react';
import { StarIcon } from 'lucide-react';
import { toast } from '@/components/ui/toast';

const REPO_URL = 'https://github.com/KavinMK05/Prism';
const STORAGE_PREFIX = 'prism.starPromptDismissed_';

interface StarPromptProps {
  version: string;
}

/**
 * Shows a one-time-per-version toast asking the user to star the repo.
 * The prompt appears once per release (keyed by the build version), stays
 * on screen until the user interacts, and never repeats within a version.
 */
export default function StarPrompt({ version }: StarPromptProps) {
  useEffect(() => {
    // Wait until the admin status poll has populated a real version string.
    if (!version) return;

    const key = STORAGE_PREFIX + version;
    if (localStorage.getItem(key) === '1') return;

    // Give the UI a beat to settle before interrupting the user.
    const timer = setTimeout(() => {
      // Mark as shown immediately so it never reappears this version, even on
      // a reload while the toast is still on screen.
      localStorage.setItem(key, '1');

      toast.add({
        title: 'Enjoying Prism?',
        description: 'Star the repo on GitHub to help others discover it.',
        // No type -> no leading icon.
        // No auto-dismiss: let the user decide (Star or Close) instead of a timer.
        timeout: 0,
        data: { closeAction: true },
        actionProps: {
          children: (
            <span className="inline-flex items-center gap-2">
              Star
              <StarIcon aria-hidden="true" className="size-4" />
            </span>
          ),
          onClick: () => {
            window.open(REPO_URL, '_blank', 'noopener');
          },
        },
      });
    }, 1500);

    return () => clearTimeout(timer);
  }, [version]);

  return null;
}

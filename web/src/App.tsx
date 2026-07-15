import { useState, useEffect, useCallback, type ReactNode } from 'react';
import { useTheme } from './ThemeContext';
import ProxyPanel from './components/ProxyPanel';
import AgentsPanel from './components/AgentsPanel';
import SearXNGPanel from './components/SearXNGPanel';
import OAuthPanel from './components/OAuthPanel';
import ProviderPanel from './components/ProviderPanel';
import ModelsPanel from './components/ModelsPanel';
import StatsPanel from './components/StatsPanel';
import { api } from './api';

type TabId = 'provider' | 'oauth' | 'models' | 'stats' | 'agents' | 'proxy' | 'searxng';

interface Tab {
  id: TabId;
  label: string;
  icon: ReactNode;
}

const TABS: Tab[] = [
  {
    id: 'provider',
    label: 'Provider',
    icon: (
      <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round">
        <rect x="2" y="3" width="20" height="14" rx="2" ry="2" />
        <line x1="8" y1="21" x2="16" y2="21" />
        <line x1="12" y1="17" x2="12" y2="21" />
      </svg>
    ),
  },
  {
    id: 'models',
    label: 'Models',
    icon: (
      <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round">
        <polygon points="12 2 2 7 12 12 22 7 12 2" />
        <polyline points="2 17 12 22 22 17" />
        <polyline points="2 12 12 17 22 12" />
      </svg>
    ),
  },
  {
    id: 'agents',
    label: 'Agents',
    icon: (
      <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round">
        <rect x="3" y="11" width="18" height="10" rx="2" />
        <circle cx="12" cy="5" r="2" />
        <path d="M12 7v4" />
        <line x1="8" y1="16" x2="8.01" y2="16" />
        <line x1="16" y1="16" x2="16.01" y2="16" />
      </svg>
    ),
  },
  {
    id: 'oauth',
    label: 'OAuth',
    icon: (
      <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round">
        <path d="M12 22s8-4 8-10V5l-8-3-8 3v7c0 6 8 10 8 10z" />
      </svg>
    ),
  },
  {
    id: 'proxy',
    label: 'Proxy',
    icon: (
      <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round">
        <circle cx="12" cy="12" r="10" />
        <line x1="2" y1="12" x2="22" y2="12" />
        <path d="M12 2a15.3 15.3 0 0 1 4 10 15.3 15.3 0 0 1-4 10 15.3 15.3 0 0 1-4-10 15.3 15.3 0 0 1 4-10z" />
      </svg>
    ),
  },
  {
    id: 'searxng',
    label: 'SearXNG',
    icon: (
      <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round">
        <circle cx="11" cy="11" r="8" />
        <line x1="21" y1="21" x2="16.65" y2="16.65" />
      </svg>
    ),
  },
  {
    id: 'stats',
    label: 'Stats',
    icon: (
      <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round">
        <line x1="18" y1="20" x2="18" y2="10" />
        <line x1="12" y1="20" x2="12" y2="4" />
        <line x1="6" y1="20" x2="6" y2="14" />
      </svg>
    ),
  },
];

const SECTIONS: { label: string; tabs: TabId[] }[] = [
  { label: 'Configuration', tabs: ['provider', 'models', 'agents'] },
  { label: 'Integrations', tabs: ['oauth', 'proxy', 'searxng'] },
  { label: 'Analytics', tabs: ['stats'] },
];

export default function App() {
  const [activeTab, setActiveTab] = useState<TabId>('provider');
  const [running, setRunning] = useState<boolean | null>(null);
  const { theme, toggleTheme } = useTheme();

  const updateStatus = useCallback(async () => {
    try {
      const s = await api('/status');
      setRunning(s.running);
    } catch {
      // ignore
    }
  }, []);

  useEffect(() => {
    updateStatus();
    const interval = setInterval(updateStatus, 5000);
    return () => clearInterval(interval);
  }, [updateStatus]);

  const renderPanel = () => {
    switch (activeTab) {
      case 'provider': return <ProviderPanel />;
      case 'oauth': return <OAuthPanel />;
      case 'models': return <ModelsPanel />;
      case 'stats': return <StatsPanel />;
      case 'agents': return <AgentsPanel />;
      case 'proxy': return <ProxyPanel />;
      case 'searxng': return <SearXNGPanel />;
      default: return null;
    }
  };

  return (
    <div className="flex h-screen overflow-hidden">
      {/* Sidebar */}
      <aside className="w-[232px] shrink-0 flex flex-col px-4 py-5 border-r border-border bg-card h-screen">
        <div className="flex items-center gap-3 mb-7 px-3">
          <svg className="w-[25px] h-[25px] rounded-md object-contain" viewBox="0 0 798 736" fill="none" xmlns="http://www.w3.org/2000/svg">
            <path d="M565.195 550.137C568.091 549.659 586.876 562.744 590.434 565.046L633.369 592.759C676.193 620.313 720.34 648.418 762.749 676.486C717.407 681.048 670.566 686.871 625.256 692.112L432.227 714.192L299.113 729.279C282.408 731.226 260.565 732.885 244.251 735.537L226.67 728.348L82.6293 670.779L44.4276 655.539C38.3646 653.133 27.2451 649.067 21.9272 646.067C41.832 643.562 64.6882 639.129 84.6775 635.712L200.844 615.751C322.585 595.533 444.044 573.662 565.195 550.137Z" fill="currentColor"/>
            <path d="M375.368 2.33536C377.052 6.89519 375.411 67.773 375.313 78.0682L374.211 218.378L151.99 599.752C151.99 599.752 152.29 599.237 86.9896 612.105C2.28882e-05 629.247 86.9896 612.105 0.278017 629.461L2.45096e-05 629.247C-0.106517 626.356 347.165 48.3347 375.368 2.33536Z" fill="currentColor"/>
            <path d="M398.329 0C401.77 3.03826 425.044 41.3783 428.65 47.2779C458.347 95.9769 487.775 144.841 516.933 193.869L673.536 456.662L759.803 600.199C772.123 620.748 785.232 641.487 797.123 662.213C795.188 661.325 785.146 654.21 782.642 652.514L750.968 631.157L629.18 550.29C624 546.861 584.311 520.905 583.423 519.197C571.391 496.132 547.529 457.556 534.304 435.636C490.536 363.959 447.209 292.017 404.317 219.814C404.452 195.363 403.736 169.525 403.35 145.016C402.603 97.7881 402.144 46.9209 398.329 0Z" fill="currentColor"/>
          </svg>
          <div>
            <h1 className="text-base font-bold tracking-tight leading-tight">Prism Settings</h1>
            <span className="inline-flex items-center gap-1.5 text-xs font-medium text-muted-foreground mt-1">
              <span className={`w-2 h-2 rounded-full inline-block ${running ? 'bg-green-500 shadow-[0_0_0_3px_rgba(34,197,94,0.18)]' : 'bg-destructive'}`} />
              <span>{running === null ? '\u2014' : running ? 'Running' : 'Stopped'}</span>
            </span>
          </div>
        </div>

        <nav className="flex-1 overflow-y-auto" aria-label="Settings">
          {SECTIONS.map((section) => (
            <div className="mt-6 first:mt-0" key={section.label}>
              <div className="text-[11px] font-semibold uppercase tracking-wider text-muted-foreground px-3 mb-2">{section.label}</div>
              <div className="flex flex-col gap-0.5">
                {section.tabs.map((id) => {
                  const tab = TABS.find((t) => t.id === id)!;
                  const isActive = activeTab === tab.id;
                  return (
                    <button
                      key={tab.id}
                      className={`flex items-center gap-2.5 px-3 py-2 rounded-md text-sm font-medium transition-colors ${isActive ? 'bg-accent text-accent-foreground' : 'text-muted-foreground hover:bg-accent hover:text-accent-foreground'}`}
                      onClick={() => setActiveTab(tab.id)}
                      aria-pressed={isActive}
                    >
                      <span className="flex items-center justify-center shrink-0 [&>svg]:w-[18px] [&>svg]:h-[18px]">{tab.icon}</span>
                      <span>{tab.label}</span>
                    </button>
                  );
                })}
              </div>
            </div>
          ))}
        </nav>

        <div className="mt-auto pt-4 border-t border-border">
          <button className="w-9 h-9 rounded-md border border-border bg-card text-muted-foreground hover:text-foreground hover:bg-accent hover:border-border-strong flex items-center justify-center transition-colors" onClick={toggleTheme} title="Toggle theme">
            {theme === 'dark' ? (
              <svg className="w-[18px] h-[18px]" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round">
                <path d="M21 12.79A9 9 0 1 1 11.21 3 7 7 0 0 0 21 12.79z" />
              </svg>
            ) : (
              <svg className="w-[18px] h-[18px]" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round">
                <circle cx="12" cy="12" r="5" />
                <path d="M12 1v2M12 21v2M4.22 4.22l1.42 1.42M18.36 18.36l1.42 1.42M1 12h2M21 12h2M4.22 19.78l1.42-1.42M18.36 5.64l1.42-1.42" />
              </svg>
            )}
          </button>
        </div>
      </aside>

      {/* Main content */}
      <main className="flex-1 min-w-0 overflow-y-auto p-8">
        {activeTab !== 'provider' && (
          <h2 className="text-2xl font-semibold tracking-tight text-foreground mb-5 font-[system-ui]">
            {TABS.find((t) => t.id === activeTab)?.label}
          </h2>
        )}
        {renderPanel()}
      </main>
    </div>
  );
}

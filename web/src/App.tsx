import { useState, useEffect, useCallback } from 'react';
import Header from './components/Header';
import ProxyPanel from './components/ProxyPanel';
import AgentsPanel from './components/AgentsPanel';
import SearXNGPanel from './components/SearXNGPanel';
import OAuthPanel from './components/OAuthPanel';
import ProviderPanel from './components/ProviderPanel';
import ModelsPanel from './components/ModelsPanel';
import StatsPanel from './components/StatsPanel';
import { api } from './api';

type TabId = 'provider' | 'oauth' | 'models' | 'stats' | 'agents' | 'proxy' | 'searxng';

const TABS: { id: TabId; label: string }[] = [
  { id: 'provider', label: 'Provider' },
  { id: 'oauth', label: 'OAuth' },
  { id: 'models', label: 'Models' },
  { id: 'stats', label: 'Stats' },
  { id: 'agents', label: 'Agents' },
  { id: 'proxy', label: 'Proxy' },
  { id: 'searxng', label: 'SearXNG' },
];

export default function App() {
  const [activeTab, setActiveTab] = useState<TabId>('provider');
  const [running, setRunning] = useState<boolean | null>(null);

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
    <div className="container">
      <Header running={running} />
      <div className="tabs">
        {TABS.map((tab) => (
          <button
            key={tab.id}
            className={`tab ${activeTab === tab.id ? 'active' : ''}`}
            onClick={() => setActiveTab(tab.id)}
          >
            {tab.label}
          </button>
        ))}
      </div>
      {renderPanel()}
    </div>
  );
}

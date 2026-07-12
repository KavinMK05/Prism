import { useEffect, useState, useCallback } from 'react';
import { api, apiPut } from '../api';
import { useToast } from '../ToastContext';
import { Input } from '@/components/ui/input';
import { Label } from '@/components/ui/label';
import { Button } from '@/components/ui/button';

function maskKey(k: string): string {
  if (!k) return '(not set)';
  if (k.length <= 8) return '****';
  return k.slice(0, 4) + '\u2026' + k.slice(-4);
}

function normalizeURL(url: string): string {
  url = url.trim();
  if (!url) return url;
  if (!/^https?:\/\//i.test(url)) url = 'https://' + url;
  return url;
}

export default function ProviderPanel() {
  const { toast } = useToast();
  const [config, setConfig] = useState<any>(null);
  const [showEdit, setShowEdit] = useState(false);
  const [editingId, setEditingId] = useState<string | null>(null);
  const [editName, setEditName] = useState('');
  const [editBaseURL, setEditBaseURL] = useState('');
  const [editAPIKey, setEditAPIKey] = useState('');
  const [addName, setAddName] = useState('');
  const [addBaseURL, setAddBaseURL] = useState('');
  const [addAPIKey, setAddAPIKey] = useState('');
  const [newAPIKey, setNewAPIKey] = useState('');
  const [showDeleteModal, setShowDeleteModal] = useState(false);
  const [pendingDeleteId, setPendingDeleteId] = useState<string | null>(null);
  const [pendingDeleteName, setPendingDeleteName] = useState('');

  const loadConfig = useCallback(async () => {
    const cfg = await api('/config');
    setConfig(cfg);
  }, []);

  useEffect(() => { loadConfig(); }, [loadConfig]);

  if (!config) return null;

  const providers = [
    { id: 'ollama_cloud', name: 'Ollama Cloud', url: config.ollama_cloud?.base_url, isCustom: false },
    { id: 'opencode_go', name: 'OpenCode Go', url: config.opencode_go?.base_url, isCustom: false },
    ...(config.custom_providers || []).map((p: any) => ({ id: p.id, name: p.name, url: p.base_url || 'Not configured', isCustom: true })),
  ];

  const getProviderById = (id: string) => {
    if (id === 'ollama_cloud') return config.ollama_cloud;
    if (id === 'opencode_go') return config.opencode_go;
    return (config.custom_providers || []).find((p: any) => p.id === id) || null;
  };

  const activeProvider = config.default_provider;
  const oauthAcct = (config.oauth_accounts || []).find((a: any) => a.id === activeProvider);
  const activeProvObj = getProviderById(activeProvider);
  const providerName = activeProvObj?.name || activeProvider;

  const openEdit = (id: string) => {
    const provider = getProviderById(id);
    if (!provider) return;
    setEditingId(id);
    setEditName(provider.name);
    setEditBaseURL(provider.base_url || '');
    setEditAPIKey('');
    setShowEdit(true);
  };

  const saveEdit = async () => {
    if (!editingId) return;
    const provider = getProviderById(editingId);
    if (!provider) { toast('No provider selected', 'error'); return; }
    const isBuiltIn = editingId === 'ollama_cloud' || editingId === 'opencode_go';
    if (!isBuiltIn && !editName.trim()) { toast('Provider name is required', 'error'); return; }
    const newName = isBuiltIn ? provider.name : editName.trim();
    const newBaseURL = isBuiltIn ? provider.base_url : normalizeURL(editBaseURL);
    provider.name = newName;
    provider.base_url = newBaseURL;
    if (editAPIKey.trim()) provider.api_key = editAPIKey.trim();
    try {
      await apiPut('/config', config);
      toast('Provider updated');
      setShowEdit(false);
      setEditingId(null);
      await loadConfig();
    } catch (e) {
      toast('Failed to update provider: ' + (e as Error).message, 'error');
      await loadConfig();
    }
  };

  const addProvider = async () => {
    if (!addName.trim()) { toast('Provider name is required', 'error'); return; }
    if (!addBaseURL.trim()) { toast('Base URL is required', 'error'); return; }
    const id = 'custom_' + addName.toLowerCase().replace(/[^a-z0-9]+/g, '_').replace(/^_|_$/g, '') + '_' + Math.random().toString(36).substr(2, 6);
    const cfg = { ...config };
    if (!cfg.custom_providers) cfg.custom_providers = [];
    cfg.custom_providers.push({ id, name: addName.trim(), base_url: normalizeURL(addBaseURL), api_key: addAPIKey.trim() });
    cfg.default_provider = id;
    try {
      await apiPut('/config', cfg);
      toast('Provider "' + addName + '" added');
      setAddName(''); setAddBaseURL(''); setAddAPIKey('');
      await loadConfig();
    } catch (e) {
      toast('Failed to add provider: ' + (e as Error).message, 'error');
    }
  };

  const confirmDelete = async () => {
    const id = pendingDeleteId;
    setShowDeleteModal(false);
    if (!id) return;
    const cfg = { ...config };
    cfg.custom_providers = (cfg.custom_providers || []).filter((p: any) => p.id !== id);
    if (cfg.default_provider === id) cfg.default_provider = 'ollama_cloud';
    try {
      await apiPut('/config', cfg);
      toast('Provider deleted');
      setShowEdit(false);
      setEditingId(null);
      await loadConfig();
    } catch (e) {
      toast('Failed to delete provider: ' + (e as Error).message, 'error');
      await loadConfig();
    }
    setPendingDeleteId(null);
  };

  const saveAPIKey = async () => {
    if (!newAPIKey.trim()) { toast('Please enter a key', 'error'); return; }
    const cfg = { ...config };
    const p = cfg.default_provider;
    if (p === 'ollama_cloud') cfg.ollama_cloud.api_key = newAPIKey.trim();
    else if (p === 'opencode_go') cfg.opencode_go.api_key = newAPIKey.trim();
    else {
      const custom = (cfg.custom_providers || []).find((pr: any) => pr.id === p);
      if (custom) custom.api_key = newAPIKey.trim();
      else { toast('Unknown provider', 'error'); return; }
    }
    try {
      await apiPut('/config', cfg);
      setNewAPIKey('');
      toast('API key updated');
      await loadConfig();
    } catch (e) {
      toast('Failed to update key: ' + (e as Error).message, 'error');
      await loadConfig();
    }
  };

  const isBuiltInEditing = editingId === 'ollama_cloud' || editingId === 'opencode_go';
  const editingProvider = editingId ? getProviderById(editingId) : null;

  return (
    <>
      <div className="rounded-xl border border-border bg-card p-6 mb-4">
        <h3 className="text-sm font-semibold tracking-tight mb-4">Default Provider</h3>
        <div className="flex flex-col gap-2">
          {providers.map(p => (
            <div key={p.id} className="flex items-center gap-3.5 px-4 py-3.5 bg-card border border-border rounded-md hover:border-border-strong hover:bg-accent transition-colors cursor-default">
              <div className="flex-1 min-w-0">
                <div className="text-sm font-medium text-foreground">
                  {p.name}
                  {p.isCustom && <span className="ml-2 text-[10px] font-medium text-muted-foreground bg-muted border border-border rounded-full px-1.5 py-0.5">Custom</span>}
                </div>
                <div className="text-xs text-muted-foreground mt-0.5">{p.url}</div>
              </div>
              <button className="w-6 h-6 rounded-sm border-none bg-transparent text-muted-foreground hover:text-foreground hover:bg-accent flex items-center justify-center shrink-0 transition-colors" title="Edit provider" onClick={() => openEdit(p.id)}>
                <svg className="w-4 h-4" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2.5" strokeLinecap="round" strokeLinejoin="round"><path d="M17 3a2.828 2.828 0 1 1 4 4L7.5 20.5 2 22l1.5-5.5L17 3z"/></svg>
              </button>
              {p.isCustom && (
                <button className="w-6 h-6 rounded-sm border-none bg-transparent text-muted-foreground hover:text-destructive hover:bg-destructive/10 flex items-center justify-center shrink-0 transition-colors" title="Delete provider" onClick={() => { setPendingDeleteId(p.id); setPendingDeleteName(p.name); setShowDeleteModal(true); }}>
                  <svg className="w-4 h-4" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round"><line x1="18" y1="6" x2="6" y2="18"/><line x1="6" y1="6" x2="18" y2="18"/></svg>
                </button>
              )}
            </div>
          ))}
        </div>
      </div>

      {showEdit && editingProvider && (
        <div className="rounded-xl border border-border bg-card p-6 mb-4">
          <h3 className="text-sm font-semibold tracking-tight mb-4">Edit: {editingProvider.name}</h3>
          {!isBuiltInEditing && (
            <div className="mb-5 last:mb-0">
              <Label>Name</Label>
              <Input type="text" placeholder="My Provider" value={editName} onChange={e => setEditName(e.target.value)} className="mt-1.5" />
            </div>
          )}
          {!isBuiltInEditing && (
            <div className="mb-5 last:mb-0">
              <Label>Base URL</Label>
              <Input type="text" placeholder="https://api.example.com" value={editBaseURL} onChange={e => setEditBaseURL(e.target.value)} className="mt-1.5" />
            </div>
          )}
          <div className="mb-5 last:mb-0">
            <Label>API Key</Label>
            <div className="flex gap-2 items-center mt-1.5">
              <Input type="password" placeholder={editingProvider.api_key ? 'Leave blank to keep current key' : 'Enter API key'} value={editAPIKey} onChange={e => setEditAPIKey(e.target.value)} />
            </div>
          </div>
          <div className="flex gap-2.5 mt-5 flex-wrap">
            <Button onClick={saveEdit}>Save Changes</Button>
            {!isBuiltInEditing && <Button variant="destructive" onClick={() => { setPendingDeleteId(editingId); setPendingDeleteName(editingProvider.name); setShowDeleteModal(true); }}>Delete Provider</Button>}
          </div>
        </div>
      )}

      <div className="rounded-xl border border-border bg-card p-6 mb-4">
        <h3 className="text-sm font-semibold tracking-tight mb-4">Add Custom Provider</h3>
        <div className="mb-5 last:mb-0">
          <Label>Name</Label>
          <Input type="text" placeholder="e.g. My OpenAI Endpoint" value={addName} onChange={e => setAddName(e.target.value)} className="mt-1.5" />
        </div>
        <div className="mb-5 last:mb-0">
          <Label>Base URL</Label>
          <Input type="text" placeholder="https://api.example.com" value={addBaseURL} onChange={e => setAddBaseURL(e.target.value)} className="mt-1.5" />
        </div>
        <div className="mb-5 last:mb-0">
          <Label>API Key</Label>
          <div className="flex gap-2 items-center mt-1.5">
            <Input type="password" placeholder="sk-..." value={addAPIKey} onChange={e => setAddAPIKey(e.target.value)} />
          </div>
        </div>
        <div className="flex gap-2.5 mt-5 flex-wrap">
          <Button onClick={addProvider}>Add Provider</Button>
        </div>
      </div>

      {!oauthAcct && (
        <div className="rounded-xl border border-border bg-card p-6 mb-4">
          <h3 className="text-sm font-semibold tracking-tight mb-4">API Key — {providerName}</h3>
          <div className="mb-5 last:mb-0">
            <Label>Current Key</Label>
            <div className="font-mono text-[13px] bg-muted px-3 py-2.5 rounded-md border border-border text-muted-foreground mt-1.5 break-all">{maskKey(activeProvObj?.api_key || '')}</div>
          </div>
          <div className="mb-5 last:mb-0">
            <Label>Set New Key</Label>
            <div className="flex gap-2 items-center mt-1.5">
              <Input type="password" placeholder="Enter new API key" value={newAPIKey} onChange={e => setNewAPIKey(e.target.value)} />
            </div>
          </div>
          <div className="flex gap-2.5 mt-5 flex-wrap">
            <Button onClick={saveAPIKey}>Update Key</Button>
          </div>
        </div>
      )}

      {showDeleteModal && (
        <div className="fixed inset-0 bg-black/35 z-[10000] flex items-center justify-center opacity-100 pointer-events-auto" onClick={() => setShowDeleteModal(false)}>
          <div className="bg-card border border-border rounded-xl p-6 max-w-[420px] w-[calc(100%-48px)] shadow-[0_8px_30px_rgba(0,0,0,0.12)] translate-y-0 scale-100" onClick={e => e.stopPropagation()}>
            <h3 className="text-sm font-semibold tracking-tight mb-2">Delete Provider</h3>
            <p className="text-[13px] text-muted-foreground leading-relaxed mb-5">Are you sure you want to delete &quot;{pendingDeleteName}&quot;? This action cannot be undone.</p>
            <div className="flex gap-2.5 justify-end">
              <Button variant="outline" onClick={() => setShowDeleteModal(false)}>Cancel</Button>
              <Button variant="destructive" onClick={confirmDelete}>Delete</Button>
            </div>
          </div>
        </div>
      )}
    </>
  );
}

import { useEffect, useState, useCallback } from 'react';
import { api, apiPut } from '../api';
import { useToast } from '../ToastContext';

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
      await loadConfig();
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
      <div className="card">
        <h3>Default Provider</h3>
        <div className="provider-cards">
          {providers.map(p => (
            <div key={p.id} className={`provider-card ${p.isCustom ? 'custom-provider' : ''}`}>
              <div className="info">
                <div className="name">{p.name}</div>
                <div className="url">{p.url}</div>
              </div>
              <button className="edit-icon" title="Edit provider" onClick={() => openEdit(p.id)}>
                <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2.5" strokeLinecap="round" strokeLinejoin="round"><path d="M17 3a2.828 2.828 0 1 1 4 4L7.5 20.5 2 22l1.5-5.5L17 3z"/></svg>
              </button>
              {p.isCustom && (
                <button className="delete-icon" title="Delete provider" onClick={() => { setPendingDeleteId(p.id); setPendingDeleteName(p.name); setShowDeleteModal(true); }}>
                  <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round"><line x1="18" y1="6" x2="6" y2="18"/><line x1="6" y1="6" x2="18" y2="18"/></svg>
                </button>
              )}
            </div>
          ))}
        </div>
      </div>

      {showEdit && editingProvider && (
        <div className="card">
          <h3>Edit: {editingProvider.name}</h3>
          {!isBuiltInEditing && (
            <div className="field">
              <label>Name</label>
              <input type="text" placeholder="My Provider" value={editName} onChange={e => setEditName(e.target.value)} />
            </div>
          )}
          {!isBuiltInEditing && (
            <div className="field">
              <label>Base URL</label>
              <input type="text" placeholder="https://api.example.com" value={editBaseURL} onChange={e => setEditBaseURL(e.target.value)} />
            </div>
          )}
          <div className="field">
            <label>API Key</label>
            <div className="input-with-icon">
              <input type="password" placeholder={editingProvider.api_key ? 'Leave blank to keep current key' : 'Enter API key'} value={editAPIKey} onChange={e => setEditAPIKey(e.target.value)} />
            </div>
          </div>
          <div className="btn-row">
            <button className="btn btn-primary" onClick={saveEdit}>Save Changes</button>
            {!isBuiltInEditing && <button className="btn btn-danger" onClick={() => { setPendingDeleteId(editingId); setPendingDeleteName(editingProvider.name); setShowDeleteModal(true); }}>Delete Provider</button>}
          </div>
        </div>
      )}

      <div className="card">
        <h3>Add Custom Provider</h3>
        <div className="field"><label>Name</label><input type="text" placeholder="e.g. My OpenAI Endpoint" value={addName} onChange={e => setAddName(e.target.value)} /></div>
        <div className="field"><label>Base URL</label><input type="text" placeholder="https://api.example.com" value={addBaseURL} onChange={e => setAddBaseURL(e.target.value)} /></div>
        <div className="field"><label>API Key</label><div className="input-with-icon"><input type="password" placeholder="sk-..." value={addAPIKey} onChange={e => setAddAPIKey(e.target.value)} /></div></div>
        <div className="btn-row"><button className="btn btn-primary" onClick={addProvider}>Add Provider</button></div>
      </div>

      {!oauthAcct && (
        <div className="card">
          <h3>API Key — {providerName}</h3>
          <div className="field"><label>Current Key</label><div className="key-display">{maskKey(activeProvObj?.api_key || '')}</div></div>
          <div className="field"><label>Set New Key</label><div className="input-with-icon"><input type="password" placeholder="Enter new API key" value={newAPIKey} onChange={e => setNewAPIKey(e.target.value)} /></div></div>
          <div className="btn-row"><button className="btn btn-primary" onClick={saveAPIKey}>Update Key</button></div>
        </div>
      )}

      {showDeleteModal && (
        <div className="modal-overlay show" onClick={() => setShowDeleteModal(false)}>
          <div className="modal-card" onClick={e => e.stopPropagation()}>
            <h3>Delete Provider</h3>
            <p>Are you sure you want to delete &quot;{pendingDeleteName}&quot;? This action cannot be undone.</p>
            <div className="btn-row">
              <button className="btn btn-ghost" onClick={() => setShowDeleteModal(false)}>Cancel</button>
              <button className="btn btn-danger" onClick={confirmDelete}>Delete</button>
            </div>
          </div>
        </div>
      )}
    </>
  );
}

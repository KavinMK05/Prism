import { useEffect, useState, useCallback } from 'react';
import { api, apiPut } from '../api';
import { toast } from './ui/toast';
import { Input } from '@/components/ui/input';
import { Label } from '@/components/ui/label';
import { Button } from '@/components/ui/button';
import { Drawer, DrawerClose, DrawerContent, DrawerDescription, DrawerFooter, DrawerHeader, DrawerTitle, DrawerTrigger } from '@/components/ui/drawer';

function normalizeURL(url: string): string {
  url = url.trim();
  if (!url) return url;
  if (!/^https?:\/\//i.test(url)) url = 'https://' + url;
  return url;
}

export default function ProviderPanel() {
  const [config, setConfig] = useState<any>(null);
  const [editDrawerOpen, setEditDrawerOpen] = useState(false);
  const [editingId, setEditingId] = useState<string | null>(null);
  const [editName, setEditName] = useState('');
  const [editBaseURL, setEditBaseURL] = useState('');
  const [editAPIKey, setEditAPIKey] = useState('');
  const [addName, setAddName] = useState('');
  const [addBaseURL, setAddBaseURL] = useState('');
  const [addAPIKey, setAddAPIKey] = useState('');
  const [showDeleteModal, setShowDeleteModal] = useState(false);
  const [pendingDeleteId, setPendingDeleteId] = useState<string | null>(null);
  const [pendingDeleteName, setPendingDeleteName] = useState('');
  const [addDrawerOpen, setAddDrawerOpen] = useState(false);

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

  const openEdit = (id: string) => {
    const provider = getProviderById(id);
    if (!provider) return;
    setEditingId(id);
    setEditName(provider.name);
    setEditBaseURL(provider.base_url || '');
    setEditAPIKey('');
    setEditDrawerOpen(true);
  };

  const saveEdit = async () => {
    if (!editingId) return;
    const provider = getProviderById(editingId);
    if (!provider) { toast.add({ title: 'No provider selected', type: 'error' }); return; }
    const isBuiltIn = editingId === 'ollama_cloud' || editingId === 'opencode_go';
    if (!isBuiltIn && !editName.trim()) { toast.add({ title: 'Provider name is required', type: 'error' }); return; }
    const newName = isBuiltIn ? provider.name : editName.trim();
    const newBaseURL = isBuiltIn ? provider.base_url : normalizeURL(editBaseURL);
    provider.name = newName;
    provider.base_url = newBaseURL;
    if (editAPIKey.trim()) provider.api_key = editAPIKey.trim();
    try {
      await apiPut('/config', config);
      toast.add({ title: 'Provider updated', type: 'success' });
      setEditDrawerOpen(false);
      setEditingId(null);
      await loadConfig();
    } catch (e) {
      toast.add({ title: 'Failed to update provider: ' + (e as Error).message, type: 'error' });
      await loadConfig();
    }
  };

  const addProvider = async () => {
    if (!addName.trim()) { toast.add({ title: 'Provider name is required', type: 'error' }); return; }
    if (!addBaseURL.trim()) { toast.add({ title: 'Base URL is required', type: 'error' }); return; }
    const id = 'custom_' + addName.toLowerCase().replace(/[^a-z0-9]+/g, '_').replace(/^_|_$/g, '') + '_' + Math.random().toString(36).substr(2, 6);
    const cfg = { ...config };
    if (!cfg.custom_providers) cfg.custom_providers = [];
    cfg.custom_providers.push({ id, name: addName.trim(), base_url: normalizeURL(addBaseURL), api_key: addAPIKey.trim() });
    cfg.default_provider = id;
    try {
      await apiPut('/config', cfg);
      toast.add({ title: 'Provider "' + addName + '" added', type: 'success' });
      setAddName(''); setAddBaseURL(''); setAddAPIKey('');
      await loadConfig();
    } catch (e) {
      toast.add({ title: 'Failed to add provider: ' + (e as Error).message, type: 'error' });
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
      toast.add({ title: 'Provider deleted', type: 'success' });
      setEditDrawerOpen(false);
      setEditingId(null);
      await loadConfig();
    } catch (e) {
      toast.add({ title: 'Failed to delete provider: ' + (e as Error).message, type: 'error' });
      await loadConfig();
    }
    setPendingDeleteId(null);
  };

  const isBuiltInEditing = editingId === 'ollama_cloud' || editingId === 'opencode_go';
  const editingProvider = editingId ? getProviderById(editingId) : null;

  return (
    <>
      <div className="flex items-center justify-between mb-5">
        <h2 className="text-2xl font-semibold tracking-tight text-foreground font-[system-ui]">Provider</h2>
        <Drawer open={addDrawerOpen} onOpenChange={setAddDrawerOpen} direction="right">
          <DrawerTrigger asChild>
            <Button size="sm">Add Provider</Button>
          </DrawerTrigger>
          <DrawerContent>
            <DrawerHeader>
              <DrawerTitle>Add Custom Provider</DrawerTitle>
              <DrawerDescription>Add a new custom provider to the list.</DrawerDescription>
            </DrawerHeader>
            <div className="px-4 pb-4 flex-1 overflow-y-auto">
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
            </div>
            <DrawerFooter>
              <Button onClick={() => { addProvider(); setAddDrawerOpen(false); }}>Add Provider</Button>
              <DrawerClose asChild>
                <Button variant="outline">Cancel</Button>
              </DrawerClose>
            </DrawerFooter>
          </DrawerContent>
        </Drawer>
      </div>

      <div className="flex flex-col gap-3 mb-4">
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

      <Drawer open={editDrawerOpen} onOpenChange={(open) => { setEditDrawerOpen(open); if (!open) setEditingId(null); }} direction="right">
        <DrawerContent>
          <DrawerHeader>
            <DrawerTitle>Edit: {editingProvider?.name}</DrawerTitle>
            <DrawerDescription>Update this provider's settings.</DrawerDescription>
          </DrawerHeader>
          <div className="px-4 pb-4 flex-1 overflow-y-auto">
            {editingProvider && !isBuiltInEditing && (
              <div className="mb-5 last:mb-0">
                <Label>Name</Label>
                <Input type="text" placeholder="My Provider" value={editName} onChange={e => setEditName(e.target.value)} className="mt-1.5" />
              </div>
            )}
            {editingProvider && !isBuiltInEditing && (
              <div className="mb-5 last:mb-0">
                <Label>Base URL</Label>
                <Input type="text" placeholder="https://api.example.com" value={editBaseURL} onChange={e => setEditBaseURL(e.target.value)} className="mt-1.5" />
              </div>
            )}
            {editingProvider && (
              <div className="mb-5 last:mb-0">
                <Label>API Key</Label>
                <div className="flex gap-2 items-center mt-1.5">
                  <Input type="password" placeholder={editingProvider.api_key ? 'Leave blank to keep current key' : 'Enter API key'} value={editAPIKey} onChange={e => setEditAPIKey(e.target.value)} />
                </div>
              </div>
            )}
          </div>
          <DrawerFooter>
            <Button onClick={saveEdit}>Save Changes</Button>
            {editingProvider && !isBuiltInEditing && <Button variant="destructive" onClick={() => { setPendingDeleteId(editingId); setPendingDeleteName(editingProvider.name); setShowDeleteModal(true); }}>Delete Provider</Button>}
            <DrawerClose asChild>
              <Button variant="outline">Cancel</Button>
            </DrawerClose>
          </DrawerFooter>
        </DrawerContent>
      </Drawer>

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

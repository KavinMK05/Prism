import { useEffect, useState, useCallback, useRef } from 'react';
import { api, apiPut } from '../api';
import { useToast } from '../ToastContext';
import { Button } from '@/components/ui/button';
import { Input } from '@/components/ui/input';
import { Label } from '@/components/ui/label';
import { Select, SelectContent, SelectGroup, SelectItem, SelectLabel, SelectTrigger, SelectValue } from '@/components/ui/select';
import { Checkbox } from '@/components/ui/checkbox';
import { Drawer, DrawerClose, DrawerContent, DrawerDescription, DrawerFooter, DrawerHeader, DrawerTitle, DrawerTrigger } from '@/components/ui/drawer';

function getProviderDisplayName(providerId: string, config: any): string {
  if (!config) return providerId;
  if (providerId === 'ollama_cloud') return 'Ollama Cloud';
  if (providerId === 'opencode_go') return 'OpenCode Go';
  const custom = (config.custom_providers || []).find((p: any) => p.id === providerId);
  if (custom) return custom.name;
  const oauth = (config.oauth_accounts || []).find((a: any) => a.id === providerId);
  if (oauth) return oauth.email || oauth.label || oauth.id;
  return providerId;
}

function buildProviderOptions(config: any): { value: string; label: string }[] {
  const providers = [
    { value: 'ollama_cloud', label: 'Ollama Cloud' },
    { value: 'opencode_go', label: 'OpenCode Go' },
  ];
  (config.custom_providers || []).forEach((p: any) => providers.push({ value: p.id, label: p.name }));
  (config.oauth_accounts || []).forEach((a: any) => providers.push({ value: a.id, label: (a.email || a.label || a.id) + ' (OAuth)' }));
  return providers;
}

export default function ModelsPanel() {
  const { toast } = useToast();
  const [config, setConfig] = useState<any>(null);
  const [remap, setRemap] = useState<any>(null);
  const [expandedRows, setExpandedRows] = useState<Set<number>>(new Set());
  const [searchQuery, setSearchQuery] = useState('');
  const [searchResults, setSearchResults] = useState<any[]>([]);
  const [showDropdown, setShowDropdown] = useState(false);
  const [newModel, setNewModel] = useState({ id: '', provider: '', ctxLen: '', maxOut: '', effort: '', reasoning: false, toolCall: false, struct: false, vision: false, infoStatus: '' });
  const [drawerOpen, setDrawerOpen] = useState(false);
  const [newAliasFrom, setNewAliasFrom] = useState('');
  const [newAliasTo, setNewAliasTo] = useState('');
  const [editStates, setEditStates] = useState<Record<number, any>>({});
  const searchTimer = useRef<ReturnType<typeof setTimeout> | null>(null);

  const loadConfig = useCallback(async () => { setConfig(await api('/config')); }, []);
  const loadRemap = useCallback(async () => { setRemap(await api('/model-remap')); }, []);

  useEffect(() => { loadConfig(); loadRemap(); }, [loadConfig, loadRemap]);

  const knownModels: any[] = remap?.known_models || [];
  const aliases: Record<string, string> = remap?.aliases || {};

  const buildModelOptions = () => {
    const models = knownModels;
    const groups: Record<string, string[]> = {};
    models.forEach(m => {
      const id = typeof m === 'string' ? m : m.id;
      const prov = typeof m === 'string' ? (config?.default_provider || 'ollama_cloud') : (m.provider || config?.default_provider || 'ollama_cloud');
      const provName = getProviderDisplayName(prov, config);
      if (!groups[provName]) groups[provName] = [];
      groups[provName].push(id);
    });
    return groups;
  };

  const saveRemap = async (updated: any) => {
    try {
      await apiPut('/model-remap', updated);
      toast('Model config saved');
      await loadRemap();
    } catch (e) { toast('Save failed: ' + (e as Error).message, 'error'); }
  };

  const collectAndSave = async (overrideKnown?: any[], overrideDefault?: string, overrideAliases?: Record<string, string>) => {
    const knownModelsList = (overrideKnown || knownModels).map((m: any) => {
      if (typeof m === 'string') return { id: m, provider: config?.default_provider || 'ollama_cloud' };
      return m;
    });
    const remap = {
      default_model: overrideDefault || '',
      known_models: knownModelsList,
      aliases: overrideAliases || {},
    };
    await saveRemap(remap);
  };

  const handleDefaultModelChange = (val: string) => {
    setRemap((prev: any) => ({ ...prev, default_model: val }));
    collectAndSave(undefined, val, Object.fromEntries(Object.entries(aliases).map(([k, v]) => [k, v])));
  };

  const toggleRow = (i: number) => {
    setExpandedRows(prev => { const s = new Set(prev); s.has(i) ? s.delete(i) : s.add(i); return s; });
    if (!editStates[i] && typeof knownModels[i] !== 'string') {
      const m = knownModels[i];
      setEditStates(prev => ({ ...prev, [i]: { provider: m.provider || '', ctxLen: m.context_length || 0, maxOut: m.max_output_tokens || 0, effort: Array.isArray(m.reasoning_effort) ? m.reasoning_effort.join(',') : (m.reasoning_effort || ''), reasoning: m.reasoning || false, toolCall: m.capabilities?.tool_calling || false, struct: m.capabilities?.structured_outputs || false, vision: m.capabilities?.vision || false } }));
    }
  };

  const saveModelEdit = (i: number) => {
    const models = [...knownModels];
    const m = models[i];
    const id = typeof m === 'string' ? m : m.id;
    const es = editStates[i];
    if (!es) return;
    models[i] = {
      id, provider: es.provider, context_length: parseInt(es.ctxLen) || 0, max_output_tokens: parseInt(es.maxOut) || 0,
      reasoning: es.reasoning, reasoning_effort: es.effort ? es.effort.split(',').map((s: string) => s.trim()).filter((s: string) => s) : [],
      capabilities: { tool_calling: es.toolCall, structured_outputs: es.struct, vision: es.vision },
    };
    const updated = { ...remap, known_models: models };
    setRemap(updated);
    saveRemap(updated);
  };

  const removeKnownModel = (i: number) => {
    const models = knownModels.filter((_, idx) => idx !== i);
    const updated = { ...remap, known_models: models };
    setRemap(updated);
    saveRemap(updated);
  };

  const doModelSearch = async (query: string) => {
    try {
      const res = await fetch('/admin/model-search?q=' + encodeURIComponent(query) + (newModel.provider ? '&provider=' + encodeURIComponent(newModel.provider) : ''));
      if (!res.ok) throw new Error(await res.text());
      const results = await res.json();
      setSearchResults(results || []);
      setShowDropdown(results && results.length > 0);
    } catch { setShowDropdown(false); }
  };

  const onSearchInput = (val: string) => {
    setSearchQuery(val);
    setNewModel(prev => ({ ...prev, id: val }));
    if (searchTimer.current) clearTimeout(searchTimer.current);
    if (!val.trim()) { setShowDropdown(false); return; }
    searchTimer.current = setTimeout(() => doModelSearch(val), 250);
  };

  const selectSearchModel = (id: string) => {
    setNewModel(prev => ({ ...prev, id }));
    setSearchQuery(id);
    setShowDropdown(false);
    fetchModelInfo(id);
  };

  const fetchModelInfo = async (id?: string) => {
    const modelId = id || newModel.id;
    if (!modelId.trim()) return;
    setNewModel(prev => ({ ...prev, infoStatus: 'Fetching...' }));
    try {
      const res = await fetch('/admin/model-info?id=' + encodeURIComponent(modelId) + (newModel.provider ? '&provider=' + encodeURIComponent(newModel.provider) : ''));
      if (!res.ok) throw new Error(await res.text());
      const data = await res.json();
      if (!data.found) { setNewModel(prev => ({ ...prev, infoStatus: 'Model not found on models.dev' })); return; }
      setNewModel(prev => ({ ...prev, ctxLen: String(data.context_length || ''), maxOut: String(data.max_output_tokens || ''), effort: Array.isArray(data.reasoning_effort) ? data.reasoning_effort.join(',') : (data.reasoning_effort || ''), reasoning: !!data.reasoning, toolCall: !!data.tool_calling, struct: !!data.structured_outputs, vision: !!data.vision, infoStatus: 'Fetched info for ' + (data.name || modelId) }));
    } catch (e) { setNewModel(prev => ({ ...prev, infoStatus: 'Fetch failed: ' + (e as Error).message })); }
  };

  const addKnownModel = () => {
    if (!newModel.id.trim()) return;
    const models = [...knownModels];
    if (models.find(m => (typeof m === 'string' ? m : m.id) === newModel.id.trim())) return;
    const effortArr = newModel.effort.trim() ? newModel.effort.split(',').map(s => s.trim()).filter(s => s) : [];
    models.push({
      id: newModel.id.trim(), provider: newModel.provider || (config?.default_provider || 'ollama_cloud'),
      reasoning: newModel.reasoning, context_length: parseInt(newModel.ctxLen) || 0, max_output_tokens: parseInt(newModel.maxOut) || 0,
      reasoning_effort: effortArr, capabilities: { tool_calling: newModel.toolCall, structured_outputs: newModel.struct, vision: newModel.vision },
    });
    const updated = { ...remap, known_models: models };
    setRemap(updated);
    saveRemap(updated);
    setNewModel({ id: '', provider: '', ctxLen: '', maxOut: '', effort: '', reasoning: false, toolCall: false, struct: false, vision: false, infoStatus: '' });
    setSearchQuery('');
  };

  const addAlias = () => {
    if (!newAliasFrom.trim() || !newAliasTo.trim()) return;
    const updatedAliases = { ...aliases, [newAliasFrom.trim()]: newAliasTo.trim() };
    const updated = { ...remap, aliases: updatedAliases };
    setRemap(updated);
    saveRemap(updated);
    setNewAliasFrom(''); setNewAliasTo('');
  };

  const removeAlias = (key: string) => {
    const updatedAliases = { ...aliases };
    delete updatedAliases[key];
    const updated = { ...remap, aliases: updatedAliases };
    setRemap(updated);
    saveRemap(updated);
  };

  if (!config || !remap) return null;
  const modelGroups = buildModelOptions();

  return (
    <>
      <div className="rounded-xl border border-border bg-card p-6 mb-4">
        <h3 className="text-sm font-semibold tracking-tight mb-1">Default Model</h3>
        <p className="text-[13px] text-muted-foreground mb-4">When an unknown model is requested, route to this model instead.</p>
        <Select value={remap.default_model || ''} onValueChange={handleDefaultModelChange}>
          <SelectTrigger className="w-full">
            <SelectValue placeholder="Select a model..." />
          </SelectTrigger>
          <SelectContent>
            {Object.entries(modelGroups).map(([provName, ids]) => (
              <SelectGroup key={provName}>
                <SelectLabel>{provName}</SelectLabel>
                {ids.map(id => <SelectItem key={id} value={id}>{id}</SelectItem>)}
              </SelectGroup>
            ))}
          </SelectContent>
        </Select>
      </div>

      <div className="rounded-xl border border-border bg-card p-6 mb-4">
        <div className="flex items-center justify-between mb-1">
          <h3 className="text-sm font-semibold tracking-tight">Known Models</h3>
          <Drawer open={drawerOpen} onOpenChange={setDrawerOpen} direction="right">
            <DrawerTrigger asChild>
              <Button size="sm">Add Model</Button>
            </DrawerTrigger>
            <DrawerContent>
              <DrawerHeader>
                <DrawerTitle>Add Model</DrawerTitle>
                <DrawerDescription>Add a new model to the known models list.</DrawerDescription>
              </DrawerHeader>
              <div className="px-4 pb-4 flex-1 overflow-y-auto">
                <div className="flex flex-wrap items-start gap-2.5 relative">
                  <div className="relative flex-1 min-w-[200px]">
                  <Input type="text" placeholder="Model ID (e.g. deepseek-v4-flash:cloud)" value={searchQuery} onChange={e => onSearchInput(e.target.value)} onKeyDown={e => { if (e.key === 'Enter') { addKnownModel(); return false; } }} autoComplete="off" className="w-full" />
                  {showDropdown && (
                    <div className="absolute top-full left-0 min-w-[300px] max-w-[420px] max-h-[240px] overflow-y-auto bg-popover border border-border rounded-md shadow-[0_8px_24px_rgba(0,0,0,0.15)] z-[100] mt-0.5">
                      {searchResults.map(r => (
                        <div key={r.id} className="px-3 py-2 cursor-pointer text-[13px] border-b border-border last:border-b-0 flex justify-between items-center hover:bg-accent" onClick={() => selectSearchModel(r.id)}>
                          <span className="font-medium">{r.id}</span>
                          <span className="text-muted-foreground text-xs ml-3">{r.name || ''}</span>
                        </div>
                      ))}
                    </div>
                  )}
                  </div>
                  <Select value={newModel.provider} onValueChange={(val) => setNewModel(prev => ({ ...prev, provider: val }))}>
                    <SelectTrigger className="min-w-[140px]"><SelectValue /></SelectTrigger>
                    <SelectContent>
                      <SelectItem value="ollama_cloud">Ollama Cloud</SelectItem>
                      <SelectItem value="opencode_go">OpenCode Go</SelectItem>
                      {(config.custom_providers || []).map((p: any) => <SelectItem key={p.id} value={p.id}>{p.name}</SelectItem>)}
                      {(config.oauth_accounts || []).map((a: any) => <SelectItem key={a.id} value={a.id}>{a.email || a.label || a.id} (OAuth)</SelectItem>)}
                    </SelectContent>
                  </Select>
                  <Button variant="outline" onClick={() => fetchModelInfo()} title="Fetch info from models.dev">Fetch</Button>
                </div>
                <div className="grid grid-cols-2 gap-2 mt-2">
                  <div><Label className="text-xs text-muted-foreground mb-0.5">Context Length</Label><Input type="number" placeholder="e.g. 128000" value={newModel.ctxLen} onChange={e => setNewModel(prev => ({ ...prev, ctxLen: e.target.value }))} /></div>
                  <div><Label className="text-xs text-muted-foreground mb-0.5">Max Output Tokens</Label><Input type="number" placeholder="e.g. 16384" value={newModel.maxOut} onChange={e => setNewModel(prev => ({ ...prev, maxOut: e.target.value }))} /></div>
                  <div className="col-span-2"><Label className="text-xs text-muted-foreground mb-0.5">Reasoning Effort Levels</Label><Input type="text" placeholder="low,medium,high" value={newModel.effort} onChange={e => setNewModel(prev => ({ ...prev, effort: e.target.value }))} /></div>
                </div>
                <div className="flex flex-wrap gap-4 mt-3">
                  <Label title="Reasoning model" className="flex items-center gap-1 cursor-pointer text-xs">
                    <Checkbox checked={newModel.reasoning} onCheckedChange={(v) => setNewModel(prev => ({ ...prev, reasoning: !!v }))} /> Reasoning
                  </Label>
                  <Label className="flex items-center gap-1 cursor-pointer text-xs" title="Supports tool/function calling"><Checkbox checked={newModel.toolCall} onCheckedChange={(v) => setNewModel(prev => ({ ...prev, toolCall: !!v }))} /> Tools</Label>
                  <Label className="flex items-center gap-1 cursor-pointer text-xs" title="Supports structured/JSON output"><Checkbox checked={newModel.struct} onCheckedChange={(v) => setNewModel(prev => ({ ...prev, struct: !!v }))} /> Struct</Label>
                  <Label className="flex items-center gap-1 cursor-pointer text-xs" title="Supports image input"><Checkbox checked={newModel.vision} onCheckedChange={(v) => setNewModel(prev => ({ ...prev, vision: !!v }))} /> Vision</Label>
                </div>
                {newModel.infoStatus && <div className="text-xs text-muted-foreground mt-1">{newModel.infoStatus}</div>}
              </div>
              <DrawerFooter>
                <Button onClick={() => { addKnownModel(); setDrawerOpen(false); }}>Add Model</Button>
                <DrawerClose asChild>
                  <Button variant="outline">Cancel</Button>
                </DrawerClose>
              </DrawerFooter>
            </DrawerContent>
          </Drawer>
        </div>
        <p className="text-[13px] text-muted-foreground mb-4">Models in this list pass through without remapping.</p>
        <div className="flex flex-col">
          {knownModels.length === 0 && <div className="text-muted-foreground/60 text-[13px] italic py-2">No known models yet.</div>}
          {knownModels.map((m: any, i: number) => {
            const id = typeof m === 'string' ? m : m.id;
            const provider = typeof m === 'string' ? '' : (m.provider || '');
            const reasoning = typeof m === 'string' ? false : (m.reasoning || false);
            const caps = typeof m === 'string' ? null : m.capabilities;
            const hasTools = caps?.tool_calling; const hasStruct = caps?.structured_outputs; const hasVision = caps?.vision;
            const es = editStates[i] || {};
            return (
              <div className="border-b border-border last:border-b-0" key={i}>
                <div className="flex items-center gap-3 px-6 py-3.5 cursor-pointer transition-colors hover:bg-accent" onClick={() => toggleRow(i)}>
                  <span className="text-sm font-medium text-foreground flex-1">{id}</span>
                  {provider && <span className="text-[11px] text-muted-foreground bg-muted border border-border rounded-full px-2 py-0.5">{getProviderDisplayName(provider, config)}</span>}
                  <div className="flex gap-1">
                    <span className={`w-[7px] h-[7px] rounded-full ${reasoning ? 'bg-purple-500' : 'bg-border-strong'}`} />
                    <span className={`w-[7px] h-[7px] rounded-full ${hasTools ? 'bg-green-500' : 'bg-border-strong'}`} />
                    <span className={`w-[7px] h-[7px] rounded-full ${hasStruct ? 'bg-green-500' : 'bg-border-strong'}`} />
                    <span className={`w-[7px] h-[7px] rounded-full ${hasVision ? 'bg-green-500' : 'bg-border-strong'}`} />
                  </div>
                  <button className="w-6 h-6 border-none bg-transparent text-muted-foreground hover:text-foreground hover:bg-accent flex items-center justify-center rounded-sm transition-colors" onClick={e => { e.stopPropagation(); toggleRow(i); }}>
                    <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round"><polyline points="6 9 12 15 18 9"/></svg>
                  </button>
                </div>
                {expandedRows.has(i) && (
                  <div className="px-6 pb-5">
                    <div className="grid grid-cols-2 gap-3 mt-3">
                      <div>
                        <Label className="text-xs text-muted-foreground mb-1">Provider</Label>
                        <Select value={es.provider || ''} onValueChange={(val) => setEditStates(prev => ({ ...prev, [i]: { ...prev[i], provider: val } }))}>
                          <SelectTrigger className="w-full"><SelectValue /></SelectTrigger>
                          <SelectContent>
                            {buildProviderOptions(config).map(o => <SelectItem key={o.value} value={o.value}>{o.label}</SelectItem>)}
                          </SelectContent>
                        </Select>
                      </div>
                      <div>
                        <Label className="text-xs text-muted-foreground mb-1">Context Length</Label>
                        <Input type="number" value={es.ctxLen || 0} onChange={e => setEditStates(prev => ({ ...prev, [i]: { ...prev[i], ctxLen: e.target.value } }))} />
                      </div>
                      <div>
                        <Label className="text-xs text-muted-foreground mb-1">Max Output Tokens</Label>
                        <Input type="number" value={es.maxOut || 0} onChange={e => setEditStates(prev => ({ ...prev, [i]: { ...prev[i], maxOut: e.target.value } }))} />
                      </div>
                      <div className="col-span-2">
                        <Label className="text-xs text-muted-foreground mb-1">Reasoning Effort Levels</Label>
                        <Input type="text" placeholder="low,medium,high" value={es.effort || ''} onChange={e => setEditStates(prev => ({ ...prev, [i]: { ...prev[i], effort: e.target.value } }))} />
                      </div>
                      <div className="col-span-2">
                        <div className="flex gap-4 flex-wrap">
                          {[['reasoning', 'Reasoning'], ['toolCall', 'Tools'], ['struct', 'Struct'], ['vision', 'Vision']].map(([key, label]) => (
                            <Label key={key} className="text-xs flex items-center gap-1 cursor-pointer">
                              <Checkbox checked={!!es[key]} onCheckedChange={(v) => setEditStates(prev => ({ ...prev, [i]: { ...prev[i], [key]: !!v } }))} /> {label}
                            </Label>
                          ))}
                        </div>
                      </div>
                    </div>
                    <div className="flex gap-2 mt-3">
                      <Button onClick={() => saveModelEdit(i)}>Save Changes</Button>
                      <Button variant="destructive" onClick={() => removeKnownModel(i)}>Delete Model</Button>
                    </div>
                  </div>
                )}
              </div>
            );
          })}
        </div>

      </div>

      <div className="rounded-xl border border-border bg-card p-6 mb-4">
        <h3 className="text-sm font-semibold tracking-tight mb-1">Aliases</h3>
        <p className="text-[13px] text-muted-foreground mb-4">Remap incoming model names to known models.</p>
        <div className="flex flex-col gap-2.5">
          {Object.entries(aliases).length === 0 && <span className="text-muted-foreground/60 text-[13px] italic">No aliases yet.</span>}
          {Object.entries(aliases).map(([k, v]) => (
            <div className="flex items-center gap-2.5" key={k}>
              <Input type="text" value={k} readOnly className="flex-1" />
              <span className="text-muted-foreground text-sm font-medium shrink-0">&rarr;</span>
              <Input type="text" value={v} readOnly className="flex-1" />
              <Button variant="outline" size="icon-sm" onClick={() => removeAlias(k)}>&times;</Button>
            </div>
          ))}
          <div className="flex items-center gap-2.5 mt-2">
            <Input type="text" placeholder="Incoming model name" value={newAliasFrom} onChange={e => setNewAliasFrom(e.target.value)} className="flex-1" />
            <span className="text-muted-foreground text-sm font-medium shrink-0">&rarr;</span>
            <Select value={newAliasTo} onValueChange={setNewAliasTo}>
              <SelectTrigger className="flex-1"><SelectValue placeholder="Select a model..." /></SelectTrigger>
              <SelectContent>
                {Object.entries(modelGroups).map(([provName, ids]) => (
                  <SelectGroup key={provName}>
                    <SelectLabel>{provName}</SelectLabel>
                    {ids.map(id => <SelectItem key={id} value={id}>{id}</SelectItem>)}
                  </SelectGroup>
                ))}
              </SelectContent>
            </Select>
            <Button variant="outline" onClick={addAlias}>Add</Button>
          </div>
        </div>
      </div>
    </>
  );
}

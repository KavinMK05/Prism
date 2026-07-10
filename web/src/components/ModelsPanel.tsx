import { useEffect, useState, useCallback, useRef } from 'react';
import { api, apiPut } from '../api';
import { useToast } from '../ToastContext';

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
      <div className="card">
        <h3>Default Model</h3>
        <p className="card-description">When an unknown model is requested, route to this model instead.</p>
        <div className="field" style={{ marginBottom: 0 }}>
          <select value={remap.default_model || ''} onChange={e => handleDefaultModelChange(e.target.value)} style={{ width: '100%', padding: '10px 12px', borderRadius: 'var(--radius-md)', border: '1px solid var(--border)', background: 'var(--surface)', color: 'var(--text)', fontSize: '14px' }}>
            <option value="">Select a model...</option>
            {Object.entries(modelGroups).map(([provName, ids]) => (
              <optgroup key={provName} label={provName}>
                {ids.map(id => <option key={id} value={id}>{id}</option>)}
              </optgroup>
            ))}
          </select>
        </div>
      </div>

      <div className="card">
        <h3>Known Models</h3>
        <p className="card-description">Models in this list pass through without remapping.</p>
        <div className="model-rows">
          {knownModels.length === 0 && <div style={{ color: 'var(--text-tertiary)', fontSize: '13px', fontStyle: 'italic', padding: '8px 0' }}>No known models yet.</div>}
          {knownModels.map((m: any, i: number) => {
            const id = typeof m === 'string' ? m : m.id;
            const provider = typeof m === 'string' ? '' : (m.provider || '');
            const reasoning = typeof m === 'string' ? false : (m.reasoning || false);
            const caps = typeof m === 'string' ? null : m.capabilities;
            const hasTools = caps?.tool_calling; const hasStruct = caps?.structured_outputs; const hasVision = caps?.vision;
            const es = editStates[i] || {};
            return (
              <div className="model-row" key={i}>
                <div className="row-main" onClick={() => toggleRow(i)}>
                  <span className="model-name">{id}</span>
                  {provider && <span className="row-provider">{getProviderDisplayName(provider, config)}</span>}
                  <div className="cap-dots">
                    <span className={`cap-dot ${reasoning ? 'reasoning' : ''}`} />
                    <span className={`cap-dot ${hasTools ? 'on' : ''}`} />
                    <span className={`cap-dot ${hasStruct ? 'on' : ''}`} />
                    <span className={`cap-dot ${hasVision ? 'on' : ''}`} />
                  </div>
                  <button className="expand-btn" onClick={e => { e.stopPropagation(); toggleRow(i); }}>
                    <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round"><polyline points="6 9 12 15 18 9"/></svg>
                  </button>
                </div>
                {expandedRows.has(i) && (
                  <div className="row-detail open">
                    <div className="detail-grid">
                      <div><label style={{ fontSize: '12px', color: 'var(--text-secondary)', display: 'block', marginBottom: '4px' }}>Provider</label>
                        <select value={es.provider || ''} onChange={e => setEditStates(prev => ({ ...prev, [i]: { ...prev[i], provider: e.target.value } }))}>
                          {buildProviderOptions(config).map(o => <option key={o.value} value={o.value}>{o.label}</option>)}
                        </select>
                      </div>
                      <div><label style={{ fontSize: '12px', color: 'var(--text-secondary)', display: 'block', marginBottom: '4px' }}>Context Length</label>
                        <input type="number" value={es.ctxLen || 0} onChange={e => setEditStates(prev => ({ ...prev, [i]: { ...prev[i], ctxLen: e.target.value } }))} />
                      </div>
                      <div><label style={{ fontSize: '12px', color: 'var(--text-secondary)', display: 'block', marginBottom: '4px' }}>Max Output Tokens</label>
                        <input type="number" value={es.maxOut || 0} onChange={e => setEditStates(prev => ({ ...prev, [i]: { ...prev[i], maxOut: e.target.value } }))} />
                      </div>
                      <div className="full"><label style={{ fontSize: '12px', color: 'var(--text-secondary)', display: 'block', marginBottom: '4px' }}>Reasoning Effort Levels</label>
                        <input type="text" placeholder="low,medium,high" value={es.effort || ''} onChange={e => setEditStates(prev => ({ ...prev, [i]: { ...prev[i], effort: e.target.value } }))} />
                      </div>
                      <div className="full">
                        <div style={{ display: 'flex', gap: '16px', flexWrap: 'wrap' }}>
                          {[['reasoning', 'Reasoning'], ['toolCall', 'Tools'], ['struct', 'Struct'], ['vision', 'Vision']].map(([key, label]) => (
                            <label key={key} style={{ fontSize: '12px', display: 'flex', alignItems: 'center', gap: '4px', cursor: 'pointer' }}>
                              <input type="checkbox" checked={!!es[key]} onChange={e => setEditStates(prev => ({ ...prev, [i]: { ...prev[i], [key]: e.target.checked } }))} /> {label}
                            </label>
                          ))}
                        </div>
                      </div>
                    </div>
                    <div className="detail-actions">
                      <button className="btn btn-primary" onClick={() => saveModelEdit(i)}>Save Changes</button>
                      <button className="btn btn-danger" onClick={() => removeKnownModel(i)}>Delete Model</button>
                    </div>
                  </div>
                )}
              </div>
            );
          })}
        </div>

        <div className="add-section">
          <h4>Add Model</h4>
          <div className="add-model-row" style={{ position: 'relative' }}>
            <input type="text" placeholder="Model ID (e.g. deepseek-v4-flash:cloud)" value={searchQuery} onChange={e => onSearchInput(e.target.value)} onKeyDown={e => { if (e.key === 'Enter') { addKnownModel(); return false; } }} autoComplete="off" />
            {showDropdown && (
              <div className="model-search-dropdown">
                {searchResults.map(r => (
                  <div key={r.id} className="ms-item" onClick={() => selectSearchModel(r.id)}>
                    <span className="ms-id">{r.id}</span>
                    <span className="ms-name">{r.name || ''}</span>
                  </div>
                ))}
              </div>
            )}
            <select style={{ padding: '10px 12px', borderRadius: 'var(--radius-md)', border: '1px solid var(--border)', background: 'var(--surface)', color: 'var(--text)', fontSize: '14px', minWidth: '140px' }} value={newModel.provider} onChange={e => setNewModel(prev => ({ ...prev, provider: e.target.value }))}>
              <option value="ollama_cloud">Ollama Cloud</option>
              <option value="opencode_go">OpenCode Go</option>
              {(config.custom_providers || []).map((p: any) => <option key={p.id} value={p.id}>{p.name}</option>)}
              {(config.oauth_accounts || []).map((a: any) => <option key={a.id} value={a.id}>{a.email || a.label || a.id} (OAuth)</option>)}
            </select>
            <button className="btn btn-ghost" onClick={() => fetchModelInfo()} title="Fetch info from models.dev">Fetch</button>
          </div>
          <div style={{ marginTop: '8px', display: 'grid', gridTemplateColumns: '1fr 1fr', gap: '8px' }}>
            <div><label style={{ fontSize: '12px', color: 'var(--text-secondary)', display: 'block', marginBottom: '2px' }}>Context Length</label>
              <input type="number" placeholder="e.g. 128000" style={{ width: '100%', padding: '8px 10px', borderRadius: 'var(--radius-md)', border: '1px solid var(--border)', background: 'var(--surface)', color: 'var(--text)', fontSize: '13px' }} value={newModel.ctxLen} onChange={e => setNewModel(prev => ({ ...prev, ctxLen: e.target.value }))} /></div>
            <div><label style={{ fontSize: '12px', color: 'var(--text-secondary)', display: 'block', marginBottom: '2px' }}>Max Output Tokens</label>
              <input type="number" placeholder="e.g. 16384" style={{ width: '100%', padding: '8px 10px', borderRadius: 'var(--radius-md)', border: '1px solid var(--border)', background: 'var(--surface)', color: 'var(--text)', fontSize: '13px' }} value={newModel.maxOut} onChange={e => setNewModel(prev => ({ ...prev, maxOut: e.target.value }))} /></div>
            <div><label style={{ fontSize: '12px', color: 'var(--text-secondary)', display: 'block', marginBottom: '2px' }}>Reasoning Effort Levels</label>
              <input type="text" placeholder="low,medium,high" style={{ width: '100%', padding: '8px 10px', borderRadius: 'var(--radius-md)', border: '1px solid var(--border)', background: 'var(--surface)', color: 'var(--text)', fontSize: '13px' }} value={newModel.effort} onChange={e => setNewModel(prev => ({ ...prev, effort: e.target.value }))} /></div>
            <div style={{ display: 'flex', alignItems: 'end', gap: '12px', paddingBottom: '2px' }}>
              <label className="reasoning-toggle" title="Reasoning model" style={{ marginBottom: 0 }}>
                <input type="checkbox" checked={newModel.reasoning} onChange={e => setNewModel(prev => ({ ...prev, reasoning: e.target.checked }))} />
                <span className="reasoning-slider" /><span className="reasoning-label">Reasoning</span>
              </label>
              <label style={{ fontSize: '12px', display: 'flex', alignItems: 'center', gap: '4px', cursor: 'pointer' }} title="Supports tool/function calling"><input type="checkbox" checked={newModel.toolCall} onChange={e => setNewModel(prev => ({ ...prev, toolCall: e.target.checked }))} /> Tools</label>
              <label style={{ fontSize: '12px', display: 'flex', alignItems: 'center', gap: '4px', cursor: 'pointer' }} title="Supports structured/JSON output"><input type="checkbox" checked={newModel.struct} onChange={e => setNewModel(prev => ({ ...prev, struct: e.target.checked }))} /> Struct</label>
              <label style={{ fontSize: '12px', display: 'flex', alignItems: 'center', gap: '4px', cursor: 'pointer' }} title="Supports image input"><input type="checkbox" checked={newModel.vision} onChange={e => setNewModel(prev => ({ ...prev, vision: e.target.checked }))} /> Vision</label>
            </div>
          </div>
          {newModel.infoStatus && <div style={{ fontSize: '12px', color: 'var(--text-secondary)', marginTop: '4px' }}>{newModel.infoStatus}</div>}
          <div className="btn-row"><button className="btn btn-primary" onClick={addKnownModel}>Add Model</button></div>
        </div>
      </div>

      <div className="card">
        <h3>Aliases</h3>
        <p className="card-description">Remap incoming model names to known models.</p>
        <div className="alias-list">
          {Object.entries(aliases).length === 0 && <span style={{ color: 'var(--text-tertiary)', fontSize: '13px', fontStyle: 'italic' }}>No aliases yet.</span>}
          {Object.entries(aliases).map(([k, v]) => (
            <div className="alias-row" key={k}>
              <input type="text" value={k} readOnly />
              <span className="arrow">→</span>
              <select value={v}>
                <option value="">Select a model...</option>
                {Object.entries(modelGroups).map(([provName, ids]) => (
                  <optgroup key={provName} label={provName}>{ids.map(id => <option key={id} value={id}>{id}</option>)}</optgroup>
                ))}
              </select>
              <button className="btn-remove" onClick={() => removeAlias(k)}>×</button>
            </div>
          ))}
          <div className="alias-row" style={{ marginTop: '8px' }}>
            <input type="text" placeholder="Incoming model name" value={newAliasFrom} onChange={e => setNewAliasFrom(e.target.value)} />
            <span className="arrow">→</span>
            <select value={newAliasTo} onChange={e => setNewAliasTo(e.target.value)}>
              <option value="">Select a model...</option>
              {Object.entries(modelGroups).map(([provName, ids]) => (
                <optgroup key={provName} label={provName}>{ids.map(id => <option key={id} value={id}>{id}</option>)}</optgroup>
              ))}
            </select>
            <button className="btn btn-ghost" onClick={addAlias}>Add</button>
          </div>
        </div>
      </div>
    </>
  );
}

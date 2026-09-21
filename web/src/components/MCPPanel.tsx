import { useCallback, useEffect, useState, type ReactNode } from 'react';
import { api, apiPost, apiPut } from '../api';
import { toast } from './ui/toast';
import { Button } from '@/components/ui/button';
import { Input } from '@/components/ui/input';
import { Label } from '@/components/ui/label';
import { Switch } from '@/components/ui/switch';
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select';
import { PlusIcon, RefreshCwIcon, Trash2Icon, PlugIcon, UnplugIcon, SearchIcon, RotateCcwIcon, PencilIcon } from 'lucide-react';

// Prism's MCP gateway panel: upstream servers, their credentials, and the
// per-agent wiring that points each agent at Prism instead of at the server.

type MCPServer = {
  id: string;
  name: string;
  transport: string;
  source?: string;
  enabled: boolean;
  command?: string;
  args?: string[];
  env?: Record<string, string>;
  cwd?: string;
  url?: string;
  headers?: Record<string, string>;
  auth_mode?: string;
  tool_allowlist?: string[];
  registry_name?: string;
  publisher?: string;
  repository?: string;
  oauth?: { client_id?: string; client_secret?: string; access_token?: string; refresh_token?: string; scopes?: string[]; token_auth_method?: string; authorized_at?: number; registration_mode?: string; as_url?: string; issuer?: string; expires_at?: number };
};

type ServerStatus = {
  id: string;
  state: string;
  last_error?: string;
  tool_count: number;
  tools?: string[];
  authorized: boolean;
  auth_mode: string;
  last_used?: number;
};

type ServerEntry = { server: MCPServer; status: ServerStatus | null; agents: string[] };
type AgentEntry = { id: string; name: string; installed: boolean; mcp_supported: boolean; mcp_active: boolean; servers: string[]; endpoint: string };

const STATE_STYLES: Record<string, { label: string; className: string }> = {
  ready: { label: 'Ready', className: 'text-green-600 dark:text-green-500' },
  idle: { label: 'Idle', className: 'text-muted-foreground' },
  needs_auth: { label: 'Needs authorization', className: 'text-amber-600 dark:text-amber-500' },
  runtime_missing: { label: 'Runtime missing', className: 'text-amber-600 dark:text-amber-500' },
  error: { label: 'Error', className: 'text-destructive' },
  disabled: { label: 'Disabled', className: 'text-muted-foreground' },
  unknown: { label: 'Unknown', className: 'text-muted-foreground' },
};

function parseKV(text: string): Record<string, string> {
  const out: Record<string, string> = {};
  text.split('\n').forEach((line) => {
    const trimmed = line.trim();
    if (!trimmed || trimmed.startsWith('#')) return;
    const idx = trimmed.indexOf('=');
    if (idx <= 0) return;
    out[trimmed.slice(0, idx).trim()] = trimmed.slice(idx + 1).trim();
  });
  return out;
}

function kvText(map?: Record<string, string>): string {
  if (!map) return '';
  return Object.entries(map).map(([k, v]) => `${k}=${v}`).join('\n');
}

export default function MCPPanel() {
  const [data, setData] = useState<any>(null);
  const [loadError, setLoadError] = useState('');
  const [busy, setBusy] = useState('');
  const [addOpen, setAddOpen] = useState(false);
  const [editing, setEditing] = useState<MCPServer | null>(null);
  const [expanded, setExpanded] = useState<string | null>(null);

  const load = useCallback(async () => {
    try {
      setData(await api('/mcp/config'));
      setLoadError('');
    } catch (e) {
      setLoadError((e as Error).message);
    }
  }, []);

  useEffect(() => { load(); }, [load]);

  const servers: ServerEntry[] = data?.servers || [];
  const agents: AgentEntry[] = data?.agents || [];
  const settings = data?.settings || {};
  const act = async (key: string, fn: () => Promise<any>, success: string) => {
    setBusy(key);
    try {
      await fn();
      toast.add({ title: success, type: 'success' });
      await load();
    } catch (e) {
      toast.add({ title: (e as Error).message, type: 'error' });
    } finally {
      setBusy('');
    }
  };

  const probe = (id: string) => act('probe:' + id, () => apiPost('/mcp/servers/control', { action: 'probe', id }), 'Tools refreshed');
  const restart = (id: string) => act('restart:' + id, () => apiPost('/mcp/servers/control', { action: 'restart', id }), 'Server restarted');
  const remove = (id: string) => {
    if (!confirm('Remove this MCP server and its stored credentials?')) return;
    return act('remove:' + id, () => apiPost('/mcp/servers/remove', { id }), 'Server removed');
  };
  const toggleEnabled = (s: MCPServer) => act('enable:' + s.id, () => apiPost('/mcp/servers/enable', { id: s.id, enabled: !s.enabled }), s.enabled ? 'Server disabled' : 'Server enabled');

  const connect = async (s: MCPServer) => {
    setBusy('auth:' + s.id);
    try {
      const res = await apiPost('/mcp/auth/login', { id: s.id });
      if (res.authorizeUrl) window.open(res.authorizeUrl, '_blank', 'width=520,height=680');
      toast.add({ title: 'Complete the sign-in in the window that just opened.' });
    } catch (e) {
      toast.add({ title: 'Could not start sign-in: ' + (e as Error).message, type: 'error' });
    } finally {
      setBusy('');
    }
  };

  const disconnect = (s: MCPServer) => act('logout:' + s.id, () => apiPost('/mcp/auth/logout', { id: s.id }), 'Credentials cleared');

  const setAgentServers = (agent: string, ids: string[]) => act('agent:' + agent, () => apiPost('/mcp/agents/servers', { agent, servers: ids }), 'Agent access updated');

  const agentToggle = (agent: string, serverId: string, on: boolean) => {
    const entry = agents.find((a) => a.id === agent);
    const current = entry?.servers || [];
    const next = on ? [...current, serverId] : current.filter((x) => x !== serverId);
    return setAgentServers(agent, next);
  };

  const agentSetup = (agent: string, remove: boolean) =>
    act('setup:' + agent, () => apiPost('/mcp/agents/setup', { agent, remove }), remove ? 'Prism entry removed from ' + agent : 'Prism entry written for ' + agent);

  const saveSettings = (patch: Record<string, unknown>) =>
    act('settings', () => apiPut('/mcp/settings', { idle_timeout_sec: settings.idle_timeout_sec, client_id_metadata_url: settings.client_id_metadata_url, ...patch }), 'Settings saved');

  return (
    <>
      <div className="mb-5 rounded-lg border border-border bg-card p-4">
        <div className="flex items-start justify-between gap-4">
          <div>
            <p className="text-sm text-foreground">
              Prism connects to MCP servers once and re-exposes their tools to your agents as
              <code className="mx-1 rounded bg-muted px-1 py-0.5 text-[12px]">mcp__&lt;server&gt;__&lt;tool&gt;</code>.
              Credentials stay here; agents only ever hold Prism's own token.
            </p>
            <p className="mt-2 text-xs text-muted-foreground">
              Aggregate endpoint <code className="rounded bg-muted px-1 py-0.5">/mcp</code> — per-agent endpoints
              <code className="ml-1 rounded bg-muted px-1 py-0.5">/mcp/&lt;agent&gt;</code>.
              {settings.proxy_running === false && ' The proxy is stopped, so live server state is unavailable.'}
            </p>
          </div>
          <Button onClick={() => setAddOpen(true)} className="shrink-0">
            <PlusIcon /> Add server
          </Button>
        </div>
      </div>

      {loadError && <p className="mb-4 text-sm text-destructive">{loadError}</p>}

      {servers.length === 0 ? (
        <p className="text-sm text-muted-foreground">No MCP servers yet. Add one from the registry, or paste a git repository that ships an mcp.json.</p>
      ) : (
        <div className="flex flex-col gap-3">
          {servers.map(({ server, status, agents: allowed }) => {
            const state = status?.state || (server.enabled ? 'unknown' : 'disabled');
            const style = STATE_STYLES[state] || STATE_STYLES.unknown;
            const isOpen = expanded === server.id;
            return (
              <div key={server.id} className="rounded-lg border border-border bg-card p-4">
                <div className="flex items-start justify-between gap-4">
                  <div className="min-w-0">
                    <div className="flex items-center gap-2">
                      <span className="truncate text-sm font-semibold text-foreground">{server.name}</span>
                      <span className="rounded bg-muted px-1.5 py-0.5 text-[11px] text-muted-foreground">{server.transport}</span>
                      <span className={'text-[11px] font-medium ' + style.className}>{style.label}</span>
                    </div>
                    <div className="mt-1 truncate font-mono text-[11px] text-muted-foreground">
                      {server.transport === 'stdio' ? [server.command, ...(server.args || [])].join(' ') : server.url}
                    </div>
                    <div className="mt-1 flex flex-wrap items-center gap-x-3 gap-y-1 text-[11px] text-muted-foreground">
                      <span>{status ? status.tool_count : 0} tools</span>
                      <span>auth: {server.auth_mode || 'none'}</span>
                      {server.tool_allowlist && server.tool_allowlist.length > 0 && <span>allowlist: {server.tool_allowlist.length}</span>}
                      {allowed.length > 0 && <span>agents: {allowed.join(', ')}</span>}
                    </div>
                    {status?.last_error && <p className="mt-1 text-[11px] text-destructive">{status.last_error}</p>}
                  </div>
                  <div className="flex shrink-0 flex-wrap items-center justify-end gap-2">
                    <Switch checked={server.enabled} onCheckedChange={() => toggleEnabled(server)} />
                    {server.transport !== 'stdio' && server.auth_mode === 'oauth' && (
                      status?.authorized
                        ? <Button size="sm" variant="outline" disabled={busy === 'logout:' + server.id} onClick={() => disconnect(server)}><UnplugIcon /> Sign out</Button>
                        : <Button size="sm" variant="outline" disabled={busy === 'auth:' + server.id} onClick={() => connect(server)}><PlugIcon /> Connect</Button>
                    )}
                    <Button size="sm" variant="outline" disabled={busy === 'probe:' + server.id} onClick={() => probe(server.id)}><RefreshCwIcon /> Tools</Button>
                    <Button size="sm" variant="outline" disabled={busy === 'restart:' + server.id} onClick={() => restart(server.id)}><RotateCcwIcon /> Restart</Button>
                    <Button size="sm" variant="outline" onClick={() => setEditing(server)}><PencilIcon /> Edit</Button>
                    <Button size="sm" variant="ghost" onClick={() => remove(server.id)}><Trash2Icon /></Button>
                  </div>
                </div>
                {status && (status.tools?.length || 0) > 0 && (
                  <button className="mt-2 text-[11px] text-muted-foreground underline" onClick={() => setExpanded(isOpen ? null : server.id)}>
                    {isOpen ? 'Hide tools' : 'Show ' + status.tools!.length + ' tools'}
                  </button>
                )}
                {isOpen && status?.tools && (
                  <div className="mt-2 flex flex-wrap gap-1">
                    {status.tools.map((t) => (
                      <code key={t} className="rounded bg-muted px-1.5 py-0.5 text-[11px] text-muted-foreground">{t}</code>
                    ))}
                  </div>
                )}
              </div>
            );
          })}
        </div>
      )}

      <div className="mt-8">
        <h3 className="mb-1 text-sm font-semibold text-foreground">Agent access</h3>
        <p className="mb-3 text-xs text-muted-foreground">
          Each agent reaches Prism at its own endpoint with only the servers you allow here. "Install" writes a single Prism entry into that agent's config; it never touches the agent's other servers.
        </p>
        <div className="overflow-x-auto rounded-lg border border-border bg-card">
          <table className="w-full min-w-[560px] text-sm">
            <thead>
              <tr className="border-b border-border text-left text-[11px] uppercase tracking-wide text-muted-foreground">
                <th className="p-3 font-medium">Agent</th>
                <th className="p-3 font-medium">Prism entry</th>
                {servers.map(({ server }) => <th key={server.id} className="p-3 text-center font-medium">{server.name}</th>)}
              </tr>
            </thead>
            <tbody>
              {agents.map((agent) => (
                <tr key={agent.id} className="border-b border-border last:border-0">
                  <td className="p-3">
                    <div className="font-medium text-foreground">{agent.name}</div>
                    <div className="text-[11px] text-muted-foreground">
                      {agent.installed ? agent.endpoint : 'not installed'}
                    </div>
                  </td>
                  <td className="p-3">
                    {!agent.mcp_supported ? (
                      <span className="text-[11px] text-muted-foreground">MCP config unsupported</span>
                    ) : !agent.installed ? (
                      <span className="text-[11px] text-muted-foreground">Not installed</span>
                    ) : agent.mcp_active ? (
                      <div className="flex items-center gap-2">
                        <span className="inline-flex items-center gap-1 text-[11px] text-green-600 dark:text-green-500"><span className="size-1.5 rounded-full bg-green-500" />Installed</span>
                        <Button size="xs" variant="ghost" disabled={busy === 'setup:' + agent.id} onClick={() => agentSetup(agent.id, true)}>Remove</Button>
                      </div>
                    ) : (
                      <Button size="xs" variant="outline" disabled={busy === 'setup:' + agent.id} onClick={() => agentSetup(agent.id, false)}>Install</Button>
                    )}
                  </td>
                  {servers.map(({ server }) => (
                    <td key={server.id} className="p-3 text-center">
                      <div className="flex justify-center">
                        <Switch
                          size="sm"
                          checked={(agent.servers || []).includes(server.id)}
                          disabled={!agent.mcp_supported || busy === 'agent:' + agent.id}
                          onCheckedChange={(on: boolean) => agentToggle(agent.id, server.id, on)}
                        />
                      </div>
                    </td>
                  ))}
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      </div>

      <div className="mt-6 rounded-lg border border-border bg-card p-4">
        <h3 className="mb-3 text-sm font-semibold text-foreground">Gateway settings</h3>
        <div className="grid gap-4 sm:grid-cols-2">
          <div>
            <Label htmlFor="mcp-idle">Idle shutdown (seconds)</Label>
            <Input
              id="mcp-idle"
              type="number"
              defaultValue={settings.idle_timeout_sec}
              onBlur={(e) => {
                const value = parseInt(e.target.value, 10);
                if (value && value !== settings.idle_timeout_sec) saveSettings({ idle_timeout_sec: value });
              }}
            />
            <p className="mt-1 text-[11px] text-muted-foreground">Local (stdio) servers are stopped after this long without a call.</p>
          </div>
          <div>
            <Label htmlFor="mcp-cimd">Client metadata document URL</Label>
            <Input
              id="mcp-cimd"
              placeholder="https://example.com/oauth/client.json"
              defaultValue={settings.client_id_metadata_url || ''}
              onBlur={(e) => {
                if ((e.target.value || '') !== (settings.client_id_metadata_url || '')) saveSettings({ client_id_metadata_url: e.target.value });
              }}
            />
            <p className="mt-1 text-[11px] text-muted-foreground">Optional. Used for CIMD registration when an authorization server does not offer dynamic registration.</p>
          </div>
        </div>
      </div>

      {addOpen && <AddServerModal onClose={() => setAddOpen(false)} onAdded={load} />}
      {editing && <EditServerModal server={editing} onClose={() => setEditing(null)} onSaved={load} />}
    </>
  );
}

// -- add server --

function AddServerModal({ onClose, onAdded }: { onClose: () => void; onAdded: () => void }) {
  const [mode, setMode] = useState<'registry' | 'manual' | 'git'>('registry');
  return (
    <Modal title="Add MCP server" onClose={onClose}>
      <div className="mb-4 flex gap-2">
        {(['registry', 'manual', 'git'] as const).map((m) => (
          <Button key={m} size="xs" variant={mode === m ? 'default' : 'outline'} onClick={() => setMode(m)}>
            {m === 'registry' ? 'Registry' : m === 'manual' ? 'Manual' : 'From git'}
          </Button>
        ))}
      </div>
      {mode === 'registry' && <RegistrySearch onAdded={onAdded} />}
      {mode === 'manual' && <ServerForm onAdded={onAdded} />}
      {mode === 'git' && <GitImport onAdded={onAdded} />}
    </Modal>
  );
}

function RegistrySearch({ onAdded }: { onAdded: () => void }) {
  const [query, setQuery] = useState('');
  const [results, setResults] = useState<any[]>([]);
  const [loading, setLoading] = useState(false);
  const [envValues, setEnvValues] = useState<Record<string, string>>({});

  const search = async () => {
    setLoading(true);
    try {
      const res = await api('/mcp/registry/search?q=' + encodeURIComponent(query));
      setResults(res.results || []);
      if (!res.results?.length) toast.add({ title: 'No servers matched that search.' });
    } catch (e) {
      toast.add({ title: 'Registry search failed: ' + (e as Error).message, type: 'error' });
    } finally {
      setLoading(false);
    }
  };

  const add = async (item: any) => {
    const env = { ...(item.server.env || {}) };
    Object.entries(envValues).forEach(([k, v]) => { if (v) env[k] = v; });
    try {
      await apiPost('/mcp/servers', { ...item.server, env });
      toast.add({ title: item.name + ' added', type: 'success' });
      onAdded();
    } catch (e) {
      toast.add({ title: 'Could not add server: ' + (e as Error).message, type: 'error' });
    }
  };

  return (
    <div>
      <div className="flex gap-2">
        <div className="relative flex-1">
          <SearchIcon className="absolute left-3 top-1/2 size-4 -translate-y-1/2 text-muted-foreground" />
          <Input className="pl-9" placeholder="Search the MCP registry (e.g. notion, github, postgres)" value={query} onChange={(e) => setQuery(e.target.value)} onKeyDown={(e) => { if (e.key === 'Enter') search(); }} />
        </div>
        <Button onClick={search} disabled={loading}>{loading ? 'Searching...' : 'Search'}</Button>
      </div>

      <div className="mt-4 max-h-[420px] overflow-y-auto">
        {results.map((item) => (
          <div key={item.name} className="mb-2 rounded-md border border-border p-3">
            <div className="flex items-start justify-between gap-3">
              <div className="min-w-0">
                <div className="text-sm font-medium text-foreground">{item.title || item.name}</div>
                <div className="text-[11px] text-muted-foreground">{item.name}{item.version ? ' · v' + item.version : ''}</div>
                {item.description && <p className="mt-1 text-xs text-muted-foreground">{item.description}</p>}
                <code className="mt-1 block truncate font-mono text-[11px] text-muted-foreground">{item.preview}</code>
              </div>
              <Button size="sm" variant="outline" onClick={() => add(item)}>Add</Button>
            </div>
            {(item.required_env || []).length > 0 && (
              <div className="mt-2 flex flex-col gap-1">
                {item.required_env.map((v: any) => (
                  <div key={v.name} className="flex items-center gap-2">
                    <Label className="w-40 shrink-0 text-[11px]">{v.name}{v.isSecret ? ' (secret)' : ''}</Label>
                    <Input
                      type={v.isSecret ? 'password' : 'text'}
                      className="h-7 text-xs"
                      placeholder={v.default || ''}
                      onChange={(e) => setEnvValues((prev) => ({ ...prev, [v.name]: e.target.value }))}
                    />
                  </div>
                ))}
              </div>
            )}
          </div>
        ))}
      </div>
    </div>
  );
}

function GitImport({ onAdded }: { onAdded: () => void }) {
  const [url, setUrl] = useState('');
  const [plugin, setPlugin] = useState<any>(null);
  const [loading, setLoading] = useState(false);

  const run = async () => {
    setLoading(true);
    try {
      const res = await apiPost('/mcp/import/git', { url });
      setPlugin(res.plugin);
      toast.add({ title: 'Imported ' + (res.plugin.servers?.length || 0) + ' server(s) from ' + res.plugin.slug, type: 'success' });
    } catch (e) {
      toast.add({ title: 'Import failed: ' + (e as Error).message, type: 'error' });
    } finally {
      setLoading(false);
    }
  };

  const add = async (server: any) => {
    try {
      await apiPost('/mcp/servers', server);
      toast.add({ title: server.name + ' added', type: 'success' });
      onAdded();
    } catch (e) {
      toast.add({ title: 'Could not add server: ' + (e as Error).message, type: 'error' });
    }
  };

  return (
    <div>
      <Label htmlFor="git-url">Git repository with an mcp.json</Label>
      <div className="mt-1 flex gap-2">
        <Input id="git-url" placeholder="https://github.com/owner/repo" value={url} onChange={(e) => setUrl(e.target.value)} />
        <Button onClick={run} disabled={loading || !url}>{loading ? 'Cloning...' : 'Import'}</Button>
      </div>
      <p className="mt-1 text-[11px] text-muted-foreground">Prism clones the repository (shallow) under its config directory and reads mcp.json / plugin.json.</p>

      {plugin && (
        <div className="mt-3">
          <p className="text-xs text-muted-foreground">Checkout: {plugin.dir}</p>
          {plugin.servers.map((server: any) => (
            <div key={server.id} className="mt-2 flex items-center justify-between gap-3 rounded-md border border-border p-3">
              <div className="min-w-0">
                <div className="text-sm font-medium text-foreground">{server.name}</div>
                <code className="block truncate font-mono text-[11px] text-muted-foreground">
                  {server.transport === 'stdio' ? [server.command, ...(server.args || [])].join(' ') : server.url}
                </code>
              </div>
              <Button size="sm" variant="outline" onClick={() => add(server)}>Add</Button>
            </div>
          ))}
        </div>
      )}
    </div>
  );
}

function ServerForm({ server, onSaved, onAdded }: { server?: MCPServer; onSaved?: () => void; onAdded?: () => void }) {
  const [name, setName] = useState(server?.name || '');
  const [transport, setTransport] = useState(server?.transport || 'stdio');
  const [command, setCommand] = useState(server?.command || 'npx');
  const [args, setArgs] = useState((server?.args || []).join(' '));
  const [cwd, setCwd] = useState(server?.cwd || '');
  const [url, setUrl] = useState(server?.url || '');
  const [authMode, setAuthMode] = useState(server?.auth_mode || 'none');
  const [env, setEnv] = useState(kvText(server?.env));
  const [headers, setHeaders] = useState(kvText(server?.headers));
  const [allowlist, setAllowlist] = useState((server?.tool_allowlist || []).join(', '));
  const [clientId, setClientId] = useState(server?.oauth?.client_id || '');
  const [clientSecret, setClientSecret] = useState('');
  const [saving, setSaving] = useState(false);

  const save = async () => {
    setSaving(true);
    const payload = {
      id: server?.id,
      name,
      transport,
      command,
      args: args.split(' ').map((a) => a.trim()).filter(Boolean),
      cwd,
      url,
      env: parseKV(env),
      headers: parseKV(headers),
      auth_mode: authMode,
      tool_allowlist: allowlist.split(',').map((t) => t.trim()).filter(Boolean),
      oauth_client_id: clientId,
      oauth_client_secret: clientSecret,
    };
    try {
      if (server) await apiPost('/mcp/servers/update', payload);
      else await apiPost('/mcp/servers', payload);
      toast.add({ title: server ? 'Server updated' : 'Server added', type: 'success' });
      onSaved?.();
      onAdded?.();
    } catch (e) {
      toast.add({ title: 'Save failed: ' + (e as Error).message, type: 'error' });
    } finally {
      setSaving(false);
    }
  };

  return (
    <div className="flex flex-col gap-3">
      <div>
        <Label htmlFor="srv-name">Name</Label>
        <Input id="srv-name" value={name} onChange={(e) => setName(e.target.value)} placeholder="Notion" />
      </div>
      <div>
        <Label>Transport</Label>
        <Select value={transport} onValueChange={setTransport}>
          <SelectTrigger><SelectValue /></SelectTrigger>
          <SelectContent>
            <SelectItem value="stdio">stdio (local process)</SelectItem>
            <SelectItem value="http">HTTP (streamable)</SelectItem>
            <SelectItem value="sse">SSE (legacy)</SelectItem>
          </SelectContent>
        </Select>
      </div>
      {transport === 'stdio' ? (
        <>
          <div>
            <Label htmlFor="srv-command">Command</Label>
            <Input id="srv-command" value={command} onChange={(e) => setCommand(e.target.value)} placeholder="npx" />
            <p className="mt-1 text-[11px] text-muted-foreground">A single executable: npx, uvx, docker, or a path.</p>
          </div>
          <div>
            <Label htmlFor="srv-args">Arguments</Label>
            <Input id="srv-args" value={args} onChange={(e) => setArgs(e.target.value)} placeholder="-y @notionhq/notion-mcp-server" />
          </div>
          <div>
            <Label htmlFor="srv-env">Environment (KEY=value per line)</Label>
            <textarea id="srv-env" className="h-20 w-full rounded-md border border-input bg-transparent p-2 font-mono text-xs" value={env} onChange={(e) => setEnv(e.target.value)} />
          </div>
          <div>
            <Label htmlFor="srv-cwd">Working directory (optional)</Label>
            <Input id="srv-cwd" value={cwd} onChange={(e) => setCwd(e.target.value)} />
          </div>
        </>
      ) : (
        <>
          <div>
            <Label htmlFor="srv-url">URL</Label>
            <Input id="srv-url" value={url} onChange={(e) => setUrl(e.target.value)} placeholder="https://mcp.notion.com/mcp" />
          </div>
          <div>
            <Label htmlFor="srv-headers">Headers (KEY=value per line)</Label>
            <textarea id="srv-headers" className="h-20 w-full rounded-md border border-input bg-transparent p-2 font-mono text-xs" value={headers} onChange={(e) => setHeaders(e.target.value)} placeholder="Authorization=Bearer ..." />
          </div>
        </>
      )}
      <div>
        <Label>Authentication</Label>
        <Select value={authMode} onValueChange={setAuthMode}>
          <SelectTrigger><SelectValue /></SelectTrigger>
          <SelectContent>
            <SelectItem value="none">None</SelectItem>
            <SelectItem value="static">Static headers / API key</SelectItem>
            <SelectItem value="oauth">OAuth (sign in with Prism)</SelectItem>
          </SelectContent>
        </Select>
      </div>
      {authMode === 'oauth' && (
        <div className="grid gap-2 sm:grid-cols-2">
          <div>
            <Label htmlFor="srv-client-id">Pre-registered client ID (optional)</Label>
            <Input id="srv-client-id" value={clientId} onChange={(e) => setClientId(e.target.value)} />
          </div>
          <div>
            <Label htmlFor="srv-client-secret">Client secret (optional)</Label>
            <Input id="srv-client-secret" type="password" value={clientSecret} onChange={(e) => setClientSecret(e.target.value)} placeholder={server?.oauth?.client_secret || ''} />
          </div>
        </div>
      )}
      <div>
        <Label htmlFor="srv-allowlist">Tool allowlist (comma separated, empty = all)</Label>
        <Input id="srv-allowlist" value={allowlist} onChange={(e) => setAllowlist(e.target.value)} placeholder="search, fetch" />
      </div>
      <div className="flex justify-end gap-2">
        <Button onClick={save} disabled={saving || !name}>{saving ? 'Saving...' : 'Save'}</Button>
      </div>
    </div>
  );
}

function EditServerModal({ server, onClose, onSaved }: { server: MCPServer; onClose: () => void; onSaved: () => void }) {
  return (
    <Modal title={'Edit ' + server.name} onClose={onClose}>
      <ServerForm server={server} onSaved={() => { onSaved(); onClose(); }} />
    </Modal>
  );
}

function Modal({ title, onClose, children }: { title: string; onClose: () => void; children: ReactNode }) {
  return (
    <div className="fixed inset-0 z-50 flex items-center justify-center bg-black/40 p-4" onClick={onClose}>
      <div className="max-h-[85vh] w-full max-w-2xl overflow-y-auto rounded-lg border border-border bg-card p-5 shadow-lg" onClick={(e) => e.stopPropagation()}>
        <div className="mb-4 flex items-center justify-between">
          <h3 className="text-base font-semibold text-foreground">{title}</h3>
          <Button size="sm" variant="ghost" onClick={onClose}>Close</Button>
        </div>
        {children}
      </div>
    </div>
  );
}

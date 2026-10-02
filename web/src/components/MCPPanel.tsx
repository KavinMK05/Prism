import {
  useCallback,
  useEffect,
  useRef,
  useState,
  type FormEvent,
  type ReactNode,
} from 'react';
import { api, apiPost, apiPut } from '../api';
import { toast } from './ui/toast';
import { Button } from '@/components/ui/button';
import {
  Drawer,
  DrawerClose,
  DrawerContent,
  DrawerDescription,
  DrawerFooter,
  DrawerHeader,
  DrawerTitle,
} from '@/components/ui/drawer';
import { Input } from '@/components/ui/input';
import {
  InputGroup,
  InputGroupAddon,
  InputGroupInput,
} from '@/components/ui/input-group';
import { Label } from '@/components/ui/label';
import { Popover, PopoverContent, PopoverTrigger } from '@/components/ui/popover';
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select';
import { Switch } from '@/components/ui/switch';
import { Tabs, TabsContent, TabsList, TabsTrigger } from '@/components/ui/tabs';
import { Textarea } from '@/components/ui/textarea';
import {
  ChevronDownIcon,
  CircleAlertIcon,
  ExternalLinkIcon,
  GitBranchIcon,
  LoaderCircleIcon,
  MoreHorizontalIcon,
  PencilIcon,
  PlugIcon,
  PlusIcon,
  RefreshCwIcon,
  RotateCcwIcon,
  SearchIcon,
  ServerIcon,
  Settings2Icon,
  ShieldCheckIcon,
  Trash2Icon,
  UnplugIcon,
  UsersIcon,
} from 'lucide-react';

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
  oauth?: {
    client_id?: string;
    client_secret?: string;
    access_token?: string;
    refresh_token?: string;
    scopes?: string[];
    token_auth_method?: string;
    authorized_at?: number;
    registration_mode?: string;
    as_url?: string;
    issuer?: string;
    expires_at?: number;
  };
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
type AgentEntry = {
  id: string;
  name: string;
  installed: boolean;
  mcp_supported: boolean;
  mcp_active: boolean;
  servers: string[];
  endpoint: string;
};

type MCPSettings = {
  proxy_running?: boolean;
  idle_timeout_sec?: number;
  client_id_metadata_url?: string;
  auto_connect?: boolean;
};

type MCPConfig = {
  servers: ServerEntry[];
  agents: AgentEntry[];
  settings: MCPSettings;
};

type AddMode = 'registry' | 'manual' | 'git';

const STATE_STYLES: Record<
  string,
  { label: string; className: string }
> = {
  ready: {
    label: 'Ready',
    className: 'bg-green-500/10 text-green-700 dark:text-green-400',
  },
  idle: {
    label: 'Idle',
    className: 'bg-muted text-muted-foreground',
  },
  needs_auth: {
    label: 'Needs authorization',
    className: 'bg-amber-500/10 text-amber-700 dark:text-amber-400',
  },
  runtime_missing: {
    label: 'Runtime missing',
    className: 'bg-amber-500/10 text-amber-700 dark:text-amber-400',
  },
  error: {
    label: 'Error',
    className: 'bg-destructive/10 text-destructive',
  },
  disabled: {
    label: 'Disabled',
    className: 'bg-muted text-muted-foreground',
  },
  unknown: {
    label: 'Not checked',
    className: 'bg-muted text-muted-foreground',
  },
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
  return Object.entries(map).map(([key, value]) => `${key}=${value}`).join('\n');
}

function serverTarget(server: MCPServer): string {
  if (server.transport === 'stdio') {
    return [server.command, ...(server.args || [])].filter(Boolean).join(' ');
  }
  return server.url || server.name;
}

function authLabel(mode?: string): string {
  if (mode === 'oauth') return 'OAuth';
  if (mode === 'static') return 'API key';
  return 'No auth';
}

function resultKey(item: any): string {
  return `${item.source_id || ''}/${item.name}`;
}

export default function MCPPanel() {
  const [data, setData] = useState<MCPConfig | null>(null);
  const [initialLoading, setInitialLoading] = useState(true);
  const [loadError, setLoadError] = useState('');
  const [busy, setBusy] = useState('');
  const [warming, setWarming] = useState(false);
  const [preloadError, setPreloadError] = useState('');
  const [addOpen, setAddOpen] = useState(false);
  const [editing, setEditing] = useState<MCPServer | null>(null);
  const [pendingRemove, setPendingRemove] = useState<MCPServer | null>(null);
  const [expanded, setExpanded] = useState<string | null>(null);

  // Only warm the servers once per visit. An explicit refresh stays available
  // for a single server and a new request can refresh every enabled server.
  const warmed = useRef(false);

  const load = useCallback(async () => {
    try {
      const next = await api('/mcp/config');
      setData(next);
      setLoadError('');
    } catch (error) {
      setLoadError((error as Error).message);
    } finally {
      setInitialLoading(false);
    }
  }, []);

  useEffect(() => {
    load();
  }, [load]);

  // Opening the panel should not require a click per server before tool names
  // appear, so ask the proxy to connect every enabled server and list its
  // tools. The statuses the proxy records are picked up by the reload after.
  const preload = useCallback(async () => {
    setWarming(true);
    try {
      await apiPost('/mcp/servers/control', { action: 'probe' });
      setPreloadError('');
    } catch (error) {
      setPreloadError((error as Error).message);
    } finally {
      setWarming(false);
      await load();
    }
  }, [load]);

  const servers = data?.servers || [];
  const agents = data?.agents || [];
  const settings = data?.settings || {};
  const enabledServers = servers.filter(({ server }) => server.enabled).length;
  const hasEnabledServers = enabledServers > 0;

  useEffect(() => {
    if (!data || warmed.current || settings.proxy_running === false || !hasEnabledServers) return;
    warmed.current = true;
    preload();
  }, [data, hasEnabledServers, settings.proxy_running, preload]);

  const act = async (
    key: string,
    fn: () => Promise<any>,
    success: string,
  ): Promise<boolean> => {
    setBusy(key);
    try {
      await fn();
      toast.add({ title: success, type: 'success' });
      await load();
      return true;
    } catch (error) {
      toast.add({ title: (error as Error).message, type: 'error' });
      return false;
    } finally {
      setBusy('');
    }
  };

  const probe = (id: string) =>
    act(
      `probe:${id}`,
      () => apiPost('/mcp/servers/control', { action: 'probe', id }),
      'Tools refreshed',
    );

  const restart = (id: string) =>
    act(
      `restart:${id}`,
      () => apiPost('/mcp/servers/control', { action: 'restart', id }),
      'Server restarted',
    );

  const toggleEnabled = (server: MCPServer, enabled: boolean) =>
    act(
      `enable:${server.id}`,
      () => apiPost('/mcp/servers/enable', { id: server.id, enabled }),
      enabled ? 'Server enabled' : 'Server disabled',
    );

  const connect = async (server: MCPServer) => {
    setBusy(`auth:${server.id}`);
    try {
      const res = await apiPost('/mcp/auth/login', { id: server.id });
      if (res.authorizeUrl) window.open(res.authorizeUrl, '_blank', 'width=520,height=680');
      toast.add({ title: 'Complete the sign-in in the window that just opened.' });
    } catch (error) {
      toast.add({
        title: 'Could not start sign-in: ' + (error as Error).message,
        type: 'error',
      });
    } finally {
      setBusy('');
    }
  };

  const disconnect = (server: MCPServer) =>
    act(
      `logout:${server.id}`,
      () => apiPost('/mcp/auth/logout', { id: server.id }),
      'Credentials cleared',
    );

  const setAgentServers = (agent: string, ids: string[]) =>
    act(
      `agent:${agent}`,
      () => apiPost('/mcp/agents/servers', { agent, servers: ids }),
      'Agent access updated',
    );

  const agentToggle = (agent: string, serverId: string, on: boolean) => {
    const entry = agents.find((item) => item.id === agent);
    const current = entry?.servers || [];
    const next = on
      ? [...current, serverId]
      : current.filter((id) => id !== serverId);
    return setAgentServers(agent, next);
  };

  const agentSetup = (agent: string, remove: boolean) =>
    act(
      `setup:${agent}`,
      () => apiPost('/mcp/agents/setup', { agent, remove }),
      remove
        ? 'Prism entry removed from ' + agent
        : 'Prism entry written for ' + agent,
    );

  const saveSettings = (patch: Record<string, unknown>) =>
    act(
      'settings',
      () =>
        apiPut('/mcp/settings', {
          idle_timeout_sec: settings.idle_timeout_sec,
          client_id_metadata_url: settings.client_id_metadata_url,
          auto_connect: settings.auto_connect ?? true,
          ...patch,
        }),
      'Settings saved',
    );

  const retryLoad = async () => {
    setInitialLoading(true);
    await load();
  };

  const confirmRemove = async () => {
    if (!pendingRemove) return;
    const removed = await act(
      `remove:${pendingRemove.id}`,
      () => apiPost('/mcp/servers/remove', { id: pendingRemove.id }),
      'Server removed',
    );
    if (removed) setPendingRemove(null);
  };

  return (
    <>
      <div className="flex flex-col gap-4 sm:flex-row sm:items-start sm:justify-between">
        <div className="min-w-0 max-w-2xl">
          <p className="text-[13px] leading-relaxed text-muted-foreground">
            Connect each MCP server once, then control which agents can use its tools. Credentials
            stay inside Prism, and agents receive only Prism&apos;s endpoint token.
          </p>
          <div className="mt-2 flex flex-wrap items-center gap-x-2 gap-y-1 text-xs text-muted-foreground">
            <span>Aggregate endpoint</span>
            <code className="rounded bg-muted px-1.5 py-0.5 font-mono">/mcp</code>
            <span>Per-agent endpoint</span>
            <code className="rounded bg-muted px-1.5 py-0.5 font-mono">
              /mcp/&lt;agent&gt;
            </code>
          </div>
        </div>
        <div className="flex shrink-0 flex-wrap gap-2">
          <Button
            variant="outline"
            onClick={preload}
            disabled={warming || !hasEnabledServers || settings.proxy_running === false}
          >
            {warming ? (
              <LoaderCircleIcon className="animate-spin" />
            ) : (
              <RefreshCwIcon />
            )}
            {warming ? 'Refreshing...' : 'Refresh tools'}
          </Button>
          <Button onClick={() => setAddOpen(true)}>
            <PlusIcon />
            Add server
          </Button>
        </div>
      </div>

      <div className="mt-5 space-y-3">
        {settings.proxy_running === false && (
          <Notice
            title="Proxy stopped"
            description="The proxy is stopped, so live server state is unavailable. Start Prism to connect and inspect servers."
          />
        )}
        {preloadError && (
          <Notice
            title="Could not refresh tools"
            description={preloadError}
            action={
              <Button variant="outline" size="xs" onClick={preload} disabled={warming}>
                Retry
              </Button>
            }
          />
        )}
        {loadError && (
          <Notice
            title="Could not load MCP configuration"
            description={loadError}
            action={
              <Button variant="outline" size="xs" onClick={retryLoad}>
                Retry
              </Button>
            }
          />
        )}
      </div>

      {initialLoading ? (
        <ServerListSkeleton />
      ) : data ? (
        <Tabs defaultValue="servers" className="mt-5">
          <TabsList className="h-auto w-full justify-start overflow-x-auto sm:w-fit">
            <TabsTrigger value="servers" className="min-w-[120px]">
              <ServerIcon />
              Servers
              <span className="text-xs text-muted-foreground">{servers.length}</span>
            </TabsTrigger>
            <TabsTrigger value="agents" className="min-w-[130px]">
              <UsersIcon />
              Agent access
            </TabsTrigger>
            <TabsTrigger value="settings" className="min-w-[110px]">
              <Settings2Icon />
              Settings
            </TabsTrigger>
          </TabsList>

          <TabsContent value="servers">
            {servers.length === 0 ? (
              <EmptyServers />
            ) : (
              <div className="overflow-hidden rounded-xl border border-border bg-card">
                <div className="divide-y divide-border">
                  {servers.map(({ server, status, agents: allowed }) => {
                    const state = server.enabled
                      ? status?.state || 'unknown'
                      : 'disabled';
                    const stateStyle = STATE_STYLES[state] || STATE_STYLES.unknown;
                    const isOpen = expanded === server.id;
                    const toolCount = status?.tool_count || 0;
                    const hasOAuth =
                      server.transport !== 'stdio' && server.auth_mode === 'oauth';

                    return (
                      <div
                        key={server.id}
                        className="p-4 transition-colors hover:bg-accent/25 sm:p-5"
                      >
                        <div className="flex flex-col gap-4 sm:flex-row sm:items-start sm:justify-between">
                          <div className="flex min-w-0 flex-1 gap-3">
                            <div className="flex size-9 shrink-0 items-center justify-center rounded-lg border border-border bg-muted text-muted-foreground">
                              <ServerIcon className="size-4" />
                            </div>
                            <div className="min-w-0 flex-1">
                              <div className="flex min-w-0 flex-wrap items-center gap-2">
                                <h3 className="truncate text-sm font-semibold text-foreground">
                                  {server.name}
                                </h3>
                                <span className="rounded-md bg-muted px-1.5 py-0.5 font-mono text-[10px] font-medium text-muted-foreground">
                                  {server.transport}
                                </span>
                                <span
                                  className={`inline-flex rounded-md px-1.5 py-0.5 text-[11px] font-medium ${stateStyle.className}`}
                                >
                                  {stateStyle.label}
                                </span>
                              </div>
                              <code
                                className="mt-1.5 block truncate font-mono text-xs text-muted-foreground"
                                title={serverTarget(server)}
                              >
                                {serverTarget(server)}
                              </code>
                              <div className="mt-2 flex flex-wrap gap-x-4 gap-y-1 text-xs text-muted-foreground">
                                <span>{toolCount} tools</span>
                                <span>{authLabel(server.auth_mode)}</span>
                                {(server.tool_allowlist?.length || 0) > 0 && (
                                  <span>{server.tool_allowlist!.length} allowlisted</span>
                                )}
                                <span>
                                  {allowed.length} {allowed.length === 1 ? 'agent' : 'agents'}
                                </span>
                              </div>
                            </div>
                          </div>

                          <div className="flex shrink-0 items-center gap-2 border-t border-border pt-3 sm:border-0 sm:pt-0">
                            {hasOAuth && !status?.authorized && (
                              <Button
                                variant="outline"
                                size="sm"
                                disabled={busy === `auth:${server.id}`}
                                onClick={() => connect(server)}
                              >
                                {busy === `auth:${server.id}` ? (
                                  <LoaderCircleIcon className="animate-spin" />
                                ) : (
                                  <PlugIcon />
                                )}
                                Connect
                              </Button>
                            )}
                            <Switch
                              checked={server.enabled}
                              disabled={busy === `enable:${server.id}`}
                              onCheckedChange={(enabled) => toggleEnabled(server, enabled)}
                              aria-label={`${server.enabled ? 'Disable' : 'Enable'} ${server.name}`}
                            />
                            <ServerActions
                              server={server}
                              status={status}
                              busy={busy}
                              warming={warming}
                              onProbe={() => probe(server.id)}
                              onRestart={() => restart(server.id)}
                              onEdit={() => setEditing(server)}
                              onRemove={() => setPendingRemove(server)}
                              onDisconnect={() => disconnect(server)}
                            />
                          </div>
                        </div>

                        {!status && server.enabled && settings.proxy_running !== false && (
                          <p className="mt-3 pl-12 text-xs text-muted-foreground">
                            The proxy has not reported this server yet. Restart Prism so it picks up
                            the current config.
                          </p>
                        )}
                        {status?.last_error && (
                          <p className="mt-3 pl-12 text-xs leading-relaxed text-destructive">
                            {status.last_error}
                          </p>
                        )}
                        {toolCount > 0 && (
                          <>
                            <Button
                              variant="ghost"
                              size="xs"
                              className="mt-2 -ml-2 text-muted-foreground"
                              onClick={() => setExpanded(isOpen ? null : server.id)}
                            >
                              <ChevronDownIcon
                                className={`transition-transform ${isOpen ? 'rotate-180' : ''}`}
                              />
                              {isOpen ? 'Hide tools' : `Show ${toolCount} tools`}
                            </Button>
                            {isOpen && status?.tools && (
                              <div className="mt-2 rounded-lg border border-border bg-muted/40 p-3">
                                <p className="mb-2 text-xs font-medium text-foreground">
                                  Available tools
                                </p>
                                <div className="grid gap-1.5 sm:grid-cols-2 xl:grid-cols-3">
                                  {status.tools.map((tool) => (
                                    <code
                                      key={tool}
                                      className="truncate rounded-md border border-border bg-background px-2 py-1.5 font-mono text-xs text-foreground"
                                      title={tool}
                                    >
                                      {tool}
                                    </code>
                                  ))}
                                </div>
                              </div>
                            )}
                          </>
                        )}
                      </div>
                    );
                  })}
                </div>
              </div>
            )}
          </TabsContent>

          <TabsContent value="agents">
            <AgentAccess
              agents={agents}
              servers={servers}
              busy={busy}
              onToggle={(agent, server, enabled) =>
                agentToggle(agent, server, enabled)
              }
              onSetup={agentSetup}
            />
          </TabsContent>

          <TabsContent value="settings">
            <GatewaySettings
              settings={settings}
              saving={busy === 'settings'}
              onSave={saveSettings}
            />
          </TabsContent>
        </Tabs>
      ) : null}

      {addOpen && (
        <AddServerDrawer onClose={() => setAddOpen(false)} onAdded={load} />
      )}
      {editing && (
        <EditServerDrawer
          server={editing}
          onClose={() => setEditing(null)}
          onSaved={load}
        />
      )}
      {pendingRemove && (
        <ConfirmRemoveDialog
          server={pendingRemove}
          busy={busy === `remove:${pendingRemove.id}`}
          onCancel={() => setPendingRemove(null)}
          onConfirm={confirmRemove}
        />
      )}
    </>
  );
}

function ServerActions({
  server,
  status,
  busy,
  warming,
  onProbe,
  onRestart,
  onEdit,
  onRemove,
  onDisconnect,
}: {
  server: MCPServer;
  status: ServerStatus | null;
  busy: string;
  warming: boolean;
  onProbe: () => void;
  onRestart: () => void;
  onEdit: () => void;
  onRemove: () => void;
  onDisconnect: () => void;
}) {
  const [open, setOpen] = useState(false);
  const closeAndRun = (fn: () => void) => {
    setOpen(false);
    fn();
  };

  return (
    <Popover open={open} onOpenChange={setOpen}>
      <PopoverTrigger
        className="inline-flex size-8 shrink-0 items-center justify-center rounded-md text-muted-foreground transition-colors outline-none hover:bg-accent hover:text-accent-foreground focus-visible:ring-[3px] focus-visible:ring-ring/50 disabled:pointer-events-none disabled:opacity-50"
        aria-label={`Actions for ${server.name}`}
      >
        <MoreHorizontalIcon className="size-4" />
      </PopoverTrigger>
      <PopoverContent align="end" className="w-52 p-1.5">
        <Button
          variant="ghost"
          size="sm"
          className="w-full justify-start"
          disabled={busy === `probe:${server.id}` || warming}
          onClick={() => closeAndRun(onProbe)}
        >
          {busy === `probe:${server.id}` ? (
            <LoaderCircleIcon className="animate-spin" />
          ) : (
            <RefreshCwIcon />
          )}
          Refresh tools
        </Button>
        <Button
          variant="ghost"
          size="sm"
          className="w-full justify-start"
          disabled={busy === `restart:${server.id}`}
          onClick={() => closeAndRun(onRestart)}
        >
          {busy === `restart:${server.id}` ? (
            <LoaderCircleIcon className="animate-spin" />
          ) : (
            <RotateCcwIcon />
          )}
          Restart server
        </Button>
        <Button
          variant="ghost"
          size="sm"
          className="w-full justify-start"
          onClick={() => closeAndRun(onEdit)}
        >
          <PencilIcon />
          Edit configuration
        </Button>
        {server.transport !== 'stdio' && server.auth_mode === 'oauth' && status?.authorized && (
          <Button
            variant="ghost"
            size="sm"
            className="w-full justify-start"
            disabled={busy === `logout:${server.id}`}
            onClick={() => closeAndRun(onDisconnect)}
          >
            <UnplugIcon />
            Sign out
          </Button>
        )}
        <div className="my-1 h-px bg-border" />
        <Button
          variant="ghost"
          size="sm"
          className="w-full justify-start text-destructive hover:bg-destructive/10 hover:text-destructive"
          onClick={() => closeAndRun(onRemove)}
        >
          <Trash2Icon />
          Remove server
        </Button>
      </PopoverContent>
    </Popover>
  );
}

function AgentAccess({
  agents,
  servers,
  busy,
  onToggle,
  onSetup,
}: {
  agents: AgentEntry[];
  servers: ServerEntry[];
  busy: string;
  onToggle: (agent: string, server: string, enabled: boolean) => void;
  onSetup: (agent: string, remove: boolean) => void;
}) {
  if (agents.length === 0) {
    return (
      <div className="rounded-xl border border-dashed border-border bg-card p-8 text-center">
        <div className="mx-auto flex size-10 items-center justify-center rounded-lg bg-muted text-muted-foreground">
          <UsersIcon className="size-4" />
        </div>
        <h3 className="mt-3 text-sm font-semibold">No compatible agents found</h3>
        <p className="mt-1 text-[13px] text-muted-foreground">
          Prism will list agent integrations here when MCP support is available.
        </p>
      </div>
    );
  }

  const minWidth = Math.max(640, 390 + servers.length * 112);

  return (
    <div>
      <div className="mb-4 max-w-2xl">
        <h3 className="text-sm font-semibold tracking-tight">Agent permissions</h3>
        <p className="mt-1 text-[13px] leading-relaxed text-muted-foreground">
          Each agent uses a dedicated Prism endpoint and only receives the servers enabled here.
          Installing Prism adds one entry and leaves the agent&apos;s other servers unchanged.
        </p>
      </div>
      <div className="overflow-x-auto rounded-xl border border-border bg-card">
        <table className="w-full text-sm" style={{ minWidth }}>
          <thead>
            <tr className="border-b border-border text-left">
              <th className="min-w-[220px] p-3 text-xs font-medium text-muted-foreground">
                Agent
              </th>
              <th className="w-[170px] p-3 text-xs font-medium text-muted-foreground">
                Prism entry
              </th>
              {servers.map(({ server }) => (
                <th
                  key={server.id}
                  className="w-28 p-3 text-center text-xs font-medium text-muted-foreground"
                  title={server.name}
                >
                  <span className="block truncate">{server.name}</span>
                </th>
              ))}
            </tr>
          </thead>
          <tbody>
            {agents.map((agent) => (
              <tr key={agent.id} className="border-b border-border last:border-0">
                <td className="p-3 align-top">
                  <div className="font-medium text-foreground">{agent.name}</div>
                  <code className="mt-1 block max-w-[260px] truncate font-mono text-[11px] text-muted-foreground">
                    {agent.installed ? agent.endpoint : 'Not installed'}
                  </code>
                </td>
                <td className="p-3 align-top">
                  {!agent.mcp_supported ? (
                    <span className="text-xs text-muted-foreground">Unsupported</span>
                  ) : !agent.installed ? (
                    <span className="text-xs text-muted-foreground">Not installed</span>
                    ) : agent.mcp_active ? (
                      <div className="flex flex-col gap-1">
                        <div className="flex flex-wrap items-center gap-1.5">
                          <span className="text-xs font-medium text-foreground">Configured</span>
                          <Button
                            size="xs"
                            variant="ghost"
                            disabled={busy === `setup:${agent.id}`}
                            onClick={() => onSetup(agent.id, true)}
                          >
                            Remove
                          </Button>
                        </div>
                        {agent.id === 'empryo' && (
                          <span className="text-xs text-muted-foreground">
                            Restart Empryo, or run /mcp in it, to connect.
                          </span>
                        )}
                        {agent.id === 'hermes' && (
                          <span className="text-xs text-muted-foreground">
                            Restart Hermes to pick up the new server.
                          </span>
                        )}
                        {agent.id === 'deepseek-harness' && (
                          <span className="text-xs text-muted-foreground">
                            Restart DeepSeek Harness to pick up the new server.
                          </span>
                        )}
                      </div>
                    ) : (
                    <Button
                      size="xs"
                      variant="outline"
                      disabled={busy === `setup:${agent.id}`}
                      onClick={() => onSetup(agent.id, false)}
                    >
                      Install
                    </Button>
                  )}
                </td>
                {servers.map(({ server }) => (
                  <td key={server.id} className="p-3 text-center align-top">
                    <Switch
                      size="sm"
                      checked={(agent.servers || []).includes(server.id)}
                      disabled={!agent.mcp_supported || busy === `agent:${agent.id}`}
                      onCheckedChange={(enabled) =>
                        onToggle(agent.id, server.id, enabled)
                      }
                      aria-label={`Allow ${server.name} for ${agent.name}`}
                    />
                  </td>
                ))}
              </tr>
            ))}
          </tbody>
        </table>
      </div>
    </div>
  );
}

function GatewaySettings({
  settings,
  saving,
  onSave,
}: {
  settings: MCPSettings;
  saving: boolean;
  onSave: (patch: Record<string, unknown>) => Promise<boolean>;
}) {
  const [idleTimeout, setIdleTimeout] = useState(
    String(settings.idle_timeout_sec ?? ''),
  );
  const [metadataURL, setMetadataURL] = useState(
    settings.client_id_metadata_url || '',
  );

  useEffect(() => {
    setIdleTimeout(String(settings.idle_timeout_sec ?? ''));
    setMetadataURL(settings.client_id_metadata_url || '');
  }, [settings.idle_timeout_sec, settings.client_id_metadata_url]);

  return (
    <div className="overflow-hidden rounded-xl border border-border bg-card">
      <div className="flex items-start gap-3 border-b border-border p-5">
        <div className="flex size-9 shrink-0 items-center justify-center rounded-lg bg-muted text-muted-foreground">
          <Settings2Icon className="size-4" />
        </div>
        <div>
          <h3 className="text-sm font-semibold">Gateway behavior</h3>
          <p className="mt-1 text-[13px] leading-relaxed text-muted-foreground">
            Control local process lifetime and optional OAuth client metadata. Settings save when a
            field loses focus.
          </p>
        </div>
      </div>
      <div className="grid gap-6 p-5 sm:grid-cols-2">
        <div>
          <Label htmlFor="mcp-idle">Idle shutdown</Label>
          <div className="mt-2 flex items-center gap-2">
            <Input
              id="mcp-idle"
              type="number"
              min={1}
              className="max-w-[180px]"
              value={idleTimeout}
              disabled={saving}
              onChange={(event) => setIdleTimeout(event.target.value)}
              onBlur={() => {
                const value = Number.parseInt(idleTimeout, 10);
                if (!Number.isFinite(value) || value < 1) {
                  setIdleTimeout(String(settings.idle_timeout_sec ?? ''));
                  toast.add({
                    title: 'Idle shutdown must be at least one second.',
                    type: 'error',
                  });
                  return;
                }
                if (value !== settings.idle_timeout_sec) {
                  onSave({ idle_timeout_sec: value });
                }
              }}
            />
            <span className="text-xs text-muted-foreground">seconds</span>
          </div>
          <p className="mt-2 text-xs leading-relaxed text-muted-foreground">
            Local stdio servers stop after this long without a tool call.
          </p>
        </div>
        <div>
          <Label htmlFor="mcp-cimd">Client metadata document URL</Label>
          <Input
            id="mcp-cimd"
            type="url"
            autoComplete="off"
            className="mt-2"
            placeholder="https://example.com/oauth/client.json"
            value={metadataURL}
            disabled={saving}
            onChange={(event) => setMetadataURL(event.target.value)}
            onBlur={() => {
              if (metadataURL !== (settings.client_id_metadata_url || '')) {
                onSave({ client_id_metadata_url: metadataURL });
              }
            }}
          />
          <p className="mt-2 text-xs leading-relaxed text-muted-foreground">
            Optional. Used for CIMD registration when an authorization server does not offer
            dynamic client registration.
          </p>
        </div>
        <div className="sm:col-span-2">
          <div className="flex items-start justify-between gap-4">
            <div>
              <Label htmlFor="mcp-auto-connect">Auto-connect on first use</Label>
              <p className="mt-2 text-xs leading-relaxed text-muted-foreground">
                For servers you have set to OAuth, open the browser and wait for your approval
                when an agent first uses one, instead of returning an authorization error.
              </p>
            </div>
            <Switch
              id="mcp-auto-connect"
              checked={settings.auto_connect ?? true}
              disabled={saving}
              onCheckedChange={(checked) => onSave({ auto_connect: checked })}
            />
          </div>
        </div>
      </div>
    </div>
  );
}

function AddServerDrawer({
  onClose,
  onAdded,
}: {
  onClose: () => void;
  onAdded: () => Promise<void>;
}) {
  const [mode, setMode] = useState<AddMode>('registry');
  const [saving, setSaving] = useState(false);

  return (
    <Drawer
      open
      onOpenChange={(open) => {
        if (!open) onClose();
      }}
      direction="right"
    >
      <DrawerContent className="sm:max-w-2xl">
        <DrawerHeader>
          <DrawerTitle>Add MCP server</DrawerTitle>
          <DrawerDescription>
            Browse a registry, configure a server manually, or import a repository containing
            mcp.json.
          </DrawerDescription>
        </DrawerHeader>
        <div className="flex-1 overflow-y-auto px-4 pb-4">
          <Tabs
            value={mode}
            onValueChange={(value) => setMode(value as AddMode)}
          >
            <TabsList className="mb-5 grid h-auto w-full grid-cols-3">
              <TabsTrigger value="registry">Marketplace</TabsTrigger>
              <TabsTrigger value="manual">Manual</TabsTrigger>
              <TabsTrigger value="git">From git</TabsTrigger>
            </TabsList>
            <TabsContent value="registry">
              <RegistrySearch onAdded={onAdded} />
            </TabsContent>
            <TabsContent value="manual">
              <ServerForm
                formId="mcp-add-server-form"
                idPrefix="mcp-add"
                setSaving={setSaving}
                onSaved={async () => {
                  await onAdded();
                  onClose();
                }}
              />
            </TabsContent>
            <TabsContent value="git">
              <GitImport onAdded={onAdded} />
            </TabsContent>
          </Tabs>
        </div>
        <DrawerFooter className="flex-row justify-end">
          {mode === 'manual' && (
            <Button
              type="submit"
              form="mcp-add-server-form"
              disabled={saving}
            >
              {saving && <LoaderCircleIcon className="animate-spin" />}
              Add server
            </Button>
          )}
          <DrawerClose asChild>
            <Button variant="outline">Close</Button>
          </DrawerClose>
        </DrawerFooter>
      </DrawerContent>
    </Drawer>
  );
}

function EditServerDrawer({
  server,
  onClose,
  onSaved,
}: {
  server: MCPServer;
  onClose: () => void;
  onSaved: () => Promise<void>;
}) {
  const [saving, setSaving] = useState(false);

  return (
    <Drawer
      open
      onOpenChange={(open) => {
        if (!open) onClose();
      }}
      direction="right"
    >
      <DrawerContent className="sm:max-w-xl">
        <DrawerHeader>
          <DrawerTitle>Edit {server.name}</DrawerTitle>
          <DrawerDescription>
            Update the connection, authentication, and tool access settings for this server.
          </DrawerDescription>
        </DrawerHeader>
        <div className="flex-1 overflow-y-auto px-4 pb-4">
          <ServerForm
            server={server}
            formId="mcp-edit-server-form"
            idPrefix="mcp-edit"
            setSaving={setSaving}
            onSaved={async () => {
              await onSaved();
              onClose();
            }}
          />
        </div>
        <DrawerFooter className="flex-row justify-end">
          <Button type="submit" form="mcp-edit-server-form" disabled={saving}>
            {saving && <LoaderCircleIcon className="animate-spin" />}
            Save changes
          </Button>
          <DrawerClose asChild>
            <Button variant="outline">Cancel</Button>
          </DrawerClose>
        </DrawerFooter>
      </DrawerContent>
    </Drawer>
  );
}

function RegistrySearch({ onAdded }: { onAdded: () => Promise<void> }) {
  const [query, setQuery] = useState('');
  const [results, setResults] = useState<any[]>([]);
  const [loading, setLoading] = useState(false);
  const [catalogLoading, setCatalogLoading] = useState(true);
  const [hasSearched, setHasSearched] = useState(false);
  const [syncing, setSyncing] = useState(false);
  const [envValues, setEnvValues] = useState<
    Record<string, Record<string, string>>
  >({});
  const [source, setSource] = useState('');
  const [sources, setSources] = useState<any[]>([]);
  const [catalog, setCatalog] = useState<any[]>([]);
  const [sourceErrors, setSourceErrors] = useState<Record<string, string>>({});
  const [fromCache, setFromCache] = useState(false);
  const [selectedVersions, setSelectedVersions] = useState<
    Record<string, string | undefined>
  >({});
  const [versionOptions, setVersionOptions] = useState<
    Record<string, string[]>
  >({});
  const [versionLoading, setVersionLoading] = useState('');
  const [adding, setAdding] = useState('');
  const [sourceManagerOpen, setSourceManagerOpen] = useState(false);

  const apply = (res: any) => {
    setResults(res.results || []);
    if (res.sources) setSources(res.sources);
    if (res.catalog) setCatalog(res.catalog);
    setSourceErrors(res.source_errors || {});
    setFromCache(!!res.from_cache);
  };

  const loadCatalog = useCallback(async () => {
    try {
      const res = await api('/mcp/registry/catalog');
      setCatalog(res.catalog || []);
      return res;
    } catch {
      return null;
    } finally {
      setCatalogLoading(false);
    }
  }, []);

  const search = async (opts?: { cached?: boolean }) => {
    setLoading(true);
    setHasSearched(true);
    try {
      const params = new URLSearchParams();
      if (query) params.set('q', query);
      if (source) params.set('source', source);
      if (opts?.cached) params.set('cached', '1');
      const res = await api('/mcp/registry/search?' + params.toString());
      apply(res);
    } catch (error) {
      toast.add({
        title: 'Registry search failed: ' + (error as Error).message,
        type: 'error',
      });
    } finally {
      setLoading(false);
    }
  };

  // The catalog is what makes search work offline, so load its state on mount
  // and browse it immediately when it already holds data.
  useEffect(() => {
    (async () => {
      const res = await loadCatalog();
      if (res?.total > 0) await search({ cached: true });
    })();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  const sync = async () => {
    setSyncing(true);
    try {
      const res = await apiPost('/mcp/registry/sync', { source });
      const added = (res.results || []).reduce(
        (total: number, item: any) => total + (item.added || 0),
        0,
      );
      const failed = (res.results || []).filter((item: any) => item.error);
      if (failed.length) {
        toast.add({
          title:
            `Synced ${added} servers; ${failed.length} source(s) failed: ` +
            failed
              .map((item: any) => item.source_name + ': ' + item.error)
              .join('; '),
          type: 'error',
        });
      } else {
        toast.add({
          title: `Catalog refreshed: ${added} servers`,
          type: 'success',
        });
      }
      await loadCatalog();
      await search({ cached: true });
    } catch (error) {
      toast.add({
        title: 'Catalog refresh failed: ' + (error as Error).message,
        type: 'error',
      });
    } finally {
      setSyncing(false);
    }
  };

  const add = async (item: any) => {
    const key = resultKey(item);
    setAdding(key);
    const selectedVersion = selectedVersions[key];
    try {
      const env = { ...(item.server.env || {}) };
      Object.entries(envValues[key] || {}).forEach(([name, value]) => {
        if (value) env[name] = value;
      });

      // A pinned version is resolved through the registry instead of the
      // cached latest entry, which is what makes version selection meaningful.
      let server = { ...item.server, env };
      if (selectedVersion && selectedVersion !== item.version) {
        const params = new URLSearchParams({
          name: item.name,
          version: selectedVersion,
        });
        if (item.source_id) params.set('source', item.source_id);
        const res = await api('/mcp/registry/resolve?' + params.toString());
        server = { ...res.item.server, env };
      }
      if (item.source_id) server.registry_source_id = item.source_id;

      // An MCPB package is a downloadable bundle, so it is unpacked locally
      // and the verified digest is carried into the saved server config.
      if (server.integrity_sha256 && server.command === 'mcpb') {
        const installed = await apiPost('/mcp/import/mcpb', {
          url: (server.args || [])[0],
          sha256: server.integrity_sha256,
          slug: item.name,
          name: item.title || item.name,
        });
        server = {
          ...server,
          command: installed.command,
          args: installed.args || [],
          env: { ...env, ...(installed.env || {}) },
        };
      }
      await apiPost('/mcp/servers', server);
      toast.add({
        title: item.title || item.name + ' added',
        type: 'success',
      });
      await onAdded();
    } catch (error) {
      toast.add({
        title: 'Could not add server: ' + (error as Error).message,
        type: 'error',
      });
    } finally {
      setAdding('');
    }
  };

  const openVersions = async (item: any) => {
    const key = resultKey(item);
    setVersionLoading(key);
    const params = new URLSearchParams({ name: item.name });
    if (item.source_id) params.set('source', item.source_id);
    try {
      const res = await api('/mcp/registry/versions?' + params.toString());
      const versions = res.versions?.length
        ? res.versions
        : item.version
          ? [item.version]
          : [];
      setVersionOptions((previous) => ({ ...previous, [key]: versions }));
      setSelectedVersions((previous) => ({
        ...previous,
        [key]: versions[0] || item.version,
      }));
      if (!versions.length) {
        toast.add({ title: 'That server publishes no version list.' });
      }
    } catch (error) {
      toast.add({
        title: 'Could not load versions: ' + (error as Error).message,
        type: 'error',
      });
    } finally {
      setVersionLoading('');
    }
  };

  const refreshCatalog = async () => {
    await loadCatalog();
    await search({ cached: true });
  };

  if (sourceManagerOpen) {
    return (
      <SourceManager
        initialSources={sources}
        onBack={() => setSourceManagerOpen(false)}
        onChanged={refreshCatalog}
      />
    );
  }

  const availableSources = (
    sources.length
      ? sources
      : catalog.map((item) => ({
          id: item.source_id,
          name: item.source_name,
          enabled: true,
        }))
  ).filter((item: any) => item.enabled);
  const errorList = Object.entries(sourceErrors);
  const syncedSources = catalog.filter((item: any) => item.last_synced_at);

  return (
    <div>
      <div className="space-y-2">
        <InputGroup>
          <InputGroupAddon>
            <SearchIcon className="size-4" />
          </InputGroupAddon>
          <InputGroupInput
            aria-label="Search MCP servers"
            placeholder="Search MCP servers"
            value={query}
            onChange={(event) => setQuery(event.target.value)}
            onKeyDown={(event) => {
              if (event.key === 'Enter') search();
            }}
          />
        </InputGroup>
        <div className="grid grid-cols-2 gap-2 sm:grid-cols-[minmax(0,1fr)_auto_auto_auto]">
          <Select
            value={source || 'all'}
            onValueChange={(value) => setSource(value === 'all' ? '' : value)}
          >
            <SelectTrigger className="w-full">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value="all">All sources</SelectItem>
              {availableSources.map((item: any) => (
                <SelectItem key={item.id} value={item.id}>
                  {item.name || item.id}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
          <Button onClick={() => search()} disabled={loading || catalogLoading}>
            {loading ? (
              <LoaderCircleIcon className="animate-spin" />
            ) : (
              <SearchIcon />
            )}
            Search
          </Button>
          <Button
            variant="outline"
            onClick={sync}
            disabled={syncing || catalogLoading}
          >
            {syncing ? (
              <LoaderCircleIcon className="animate-spin" />
            ) : (
              <RefreshCwIcon />
            )}
            Sync
          </Button>
          <Button
            variant="ghost"
            onClick={() => setSourceManagerOpen(true)}
          >
            Sources
          </Button>
        </div>
      </div>

      <div className="mt-3 rounded-md bg-muted/50 px-3 py-2 text-xs text-muted-foreground">
        {catalogLoading ? (
          <span>Loading catalog...</span>
        ) : syncedSources.length ? (
          <div className="flex flex-wrap gap-x-4 gap-y-1">
            {syncedSources.map((item: any) => (
              <span key={item.source_id}>
                {item.source_name || item.source_id}: {item.server_count} servers
              </span>
            ))}
            {fromCache && <span>Results loaded from cache</span>}
          </div>
        ) : (
          <span>The catalog has not been synced yet.</span>
        )}
      </div>

      {errorList.length > 0 && (
        <div className="mt-3 rounded-md border border-destructive/30 bg-destructive/5 p-3 text-xs text-destructive">
          {errorList.map(([id, message]) => (
            <p key={id}>{id}: {message}</p>
          ))}
        </div>
      )}

      <div className="mt-4">
        {loading && results.length === 0 ? (
          <RegistrySkeleton />
        ) : results.length === 0 ? (
          <div className="rounded-xl border border-dashed border-border p-8 text-center">
            <div className="mx-auto flex size-10 items-center justify-center rounded-lg bg-muted text-muted-foreground">
              <SearchIcon className="size-4" />
            </div>
            <h3 className="mt-3 text-sm font-semibold">
              {hasSearched ? 'No matching servers' : 'Search the catalog'}
            </h3>
            <p className="mt-1 text-[13px] leading-relaxed text-muted-foreground">
              {hasSearched
                ? 'Try a broader search or choose a different registry source.'
                : 'Sync a catalog first, then search for the tool you need.'}
            </p>
          </div>
        ) : (
          <div className="overflow-hidden rounded-xl border border-border">
            <div className="divide-y divide-border">
              {results.map((item) => {
                const key = resultKey(item);
                const selectedVersion = selectedVersions[key];
                const versions = versionOptions[key] || [];
                const environment = [
                  ...(item.required_env || []),
                  ...(item.optional_env || []),
                ];
                return (
                  <div key={key} className="p-4">
                    <div className="flex items-start justify-between gap-4">
                      <div className="min-w-0 flex-1">
                        <div className="flex flex-wrap items-center gap-2">
                          <h3 className="text-sm font-semibold text-foreground">
                            {item.title || item.name}
                          </h3>
                          {item.trusted && (
                            <span
                              className="inline-flex items-center gap-1 rounded-md bg-green-500/10 px-1.5 py-0.5 text-[11px] font-medium text-green-700 dark:text-green-400"
                              title="The publisher namespace and source repository name the same owner"
                            >
                              <ShieldCheckIcon className="size-3" />
                              Verified publisher
                            </span>
                          )}
                          {item.status === 'deprecated' && (
                            <span className="rounded-md bg-amber-500/10 px-1.5 py-0.5 text-[11px] font-medium text-amber-700 dark:text-amber-400">
                              Deprecated
                            </span>
                          )}
                        </div>
                        <p className="mt-1 text-xs text-muted-foreground">
                          {item.name}
                          {item.version ? ` v${item.version}` : ''}
                          {item.publisher ? ` by ${item.publisher}` : ''}
                        </p>
                        {item.description && (
                          <p className="mt-2 text-[13px] leading-relaxed text-muted-foreground">
                            {item.description}
                          </p>
                        )}
                        {item.preview && (
                          <code className="mt-2 block truncate rounded-md bg-muted px-2 py-1.5 font-mono text-[11px] text-muted-foreground">
                            {item.preview}
                          </code>
                        )}
                        {item.repository && (
                          <a
                            className="mt-2 inline-flex max-w-full items-center gap-1 text-xs text-muted-foreground underline underline-offset-4 hover:text-foreground"
                            href={item.repository}
                            target="_blank"
                            rel="noreferrer"
                          >
                            <span className="truncate">{item.repository}</span>
                            <ExternalLinkIcon className="size-3 shrink-0" />
                          </a>
                        )}
                      </div>
                      <Button
                        size="sm"
                        className="shrink-0"
                        disabled={adding === key}
                        onClick={() => add(item)}
                      >
                        {adding === key ? (
                          <LoaderCircleIcon className="animate-spin" />
                        ) : (
                          <PlusIcon />
                        )}
                        Add
                      </Button>
                    </div>

                    {environment.length > 0 && (
                      <div className="mt-4 border-t border-border pt-4">
                        <div className="grid gap-3 sm:grid-cols-2">
                          {environment.map((variable: any) => (
                            <div key={variable.name}>
                              <Label
                                htmlFor={`${key}-${variable.name}`}
                                className="justify-between text-xs"
                              >
                                <span className="truncate">{variable.name}</span>
                                <span className="ml-2 shrink-0 text-[10px] font-normal text-muted-foreground">
                                  {variable.isRequired ? 'Required' : 'Optional'}
                                  {variable.isSecret ? ' secret' : ''}
                                </span>
                              </Label>
                              <Input
                                id={`${key}-${variable.name}`}
                                type={variable.isSecret ? 'password' : 'text'}
                                autoComplete="off"
                                className="mt-1.5"
                                placeholder={variable.default || variable.description || ''}
                                value={envValues[key]?.[variable.name] || ''}
                                onChange={(event) =>
                                  setEnvValues((previous) => ({
                                    ...previous,
                                    [key]: {
                                      ...(previous[key] || {}),
                                      [variable.name]: event.target.value,
                                    },
                                  }))
                                }
                              />
                            </div>
                          ))}
                        </div>
                      </div>
                    )}

                    <div className="mt-3 flex flex-wrap items-center gap-2">
                      {versions.length > 0 ? (
                        <Select
                          value={selectedVersion}
                          onValueChange={(value) =>
                            setSelectedVersions((previous) => ({
                              ...previous,
                              [key]: value,
                            }))
                          }
                        >
                          <SelectTrigger className="h-8 w-[160px] text-xs">
                            <SelectValue />
                          </SelectTrigger>
                          <SelectContent>
                            {versions.map((version) => (
                              <SelectItem key={version} value={version}>
                                {version}
                              </SelectItem>
                            ))}
                          </SelectContent>
                        </Select>
                      ) : (
                        <Button
                          variant="ghost"
                          size="xs"
                          disabled={versionLoading === key}
                          onClick={() => openVersions(item)}
                        >
                          {versionLoading === key ? (
                            <LoaderCircleIcon className="animate-spin" />
                          ) : null}
                          Versions
                        </Button>
                      )}
                      {item.source_name && (
                        <span className="text-[11px] text-muted-foreground">
                          {item.source_name}
                        </span>
                      )}
                    </div>
                  </div>
                );
              })}
            </div>
          </div>
        )}
      </div>
    </div>
  );
}

// SourceManager adds, edits, and removes the registries the marketplace
// searches. Every source speaks the same API shape, so a private registry is a
// base URL plus an optional auth header.
function SourceManager({
  initialSources,
  onBack,
  onChanged,
}: {
  initialSources: any[];
  onBack: () => void;
  onChanged: () => Promise<void>;
}) {
  const [list, setList] = useState<any[]>(initialSources);
  const [name, setName] = useState('');
  const [baseUrl, setBaseUrl] = useState('');
  const [authHeader, setAuthHeader] = useState('');
  const [busy, setBusy] = useState('');
  const [confirming, setConfirming] = useState('');

  const load = useCallback(async () => {
    try {
      const res = await api('/mcp/registry/sources');
      setList(res.sources || []);
    } catch (error) {
      toast.add({
        title: 'Could not load sources: ' + (error as Error).message,
        type: 'error',
      });
    }
  }, []);

  useEffect(() => {
    load();
  }, [load]);

  const add = async () => {
    setBusy('add');
    try {
      await apiPost('/mcp/registry/sources', {
        name: name.trim(),
        base_url: baseUrl.trim(),
        auth_header: authHeader.trim(),
      });
      toast.add({ title: 'Registry added', type: 'success' });
      setName('');
      setBaseUrl('');
      setAuthHeader('');
      await load();
      await onChanged();
    } catch (error) {
      toast.add({
        title: 'Could not add registry: ' + (error as Error).message,
        type: 'error',
      });
    } finally {
      setBusy('');
    }
  };

  const toggle = async (source: any) => {
    setBusy(`toggle:${source.id}`);
    try {
      await apiPut('/mcp/registry/sources', {
        id: source.id,
        enabled: !source.enabled,
      });
      await load();
      await onChanged();
    } catch (error) {
      toast.add({
        title: 'Could not update registry: ' + (error as Error).message,
        type: 'error',
      });
    } finally {
      setBusy('');
    }
  };

  const remove = async (source: any) => {
    setBusy(`remove:${source.id}`);
    try {
      await apiPost('/mcp/registry/sources/remove', { id: source.id });
      toast.add({ title: 'Registry removed', type: 'success' });
      setConfirming('');
      await load();
      await onChanged();
    } catch (error) {
      toast.add({
        title: 'Could not remove registry: ' + (error as Error).message,
        type: 'error',
      });
    } finally {
      setBusy('');
    }
  };

  return (
    <div>
      <div className="flex items-center justify-between gap-3">
        <div>
          <h3 className="text-sm font-semibold">Registry sources</h3>
          <p className="mt-1 text-[13px] text-muted-foreground">
            Add any registry that implements GET /v0.1/servers.
          </p>
        </div>
        <Button variant="ghost" size="sm" onClick={onBack}>
          Back
        </Button>
      </div>

      <div className="mt-4 overflow-hidden rounded-xl border border-border">
        {list.length === 0 ? (
          <p className="p-6 text-center text-[13px] text-muted-foreground">
            No registry sources found.
          </p>
        ) : (
          <div className="divide-y divide-border">
            {list.map((source) =>
              confirming === source.id ? (
                <div
                  key={source.id}
                  className="flex flex-col gap-3 bg-destructive/5 p-4 sm:flex-row sm:items-center"
                >
                  <div className="min-w-0 flex-1">
                    <p className="text-sm font-medium text-foreground">
                      Remove {source.name || source.id}?
                    </p>
                    <p className="mt-1 text-xs text-muted-foreground">
                      Its cached servers will also be removed from the catalog.
                    </p>
                  </div>
                  <div className="flex gap-2">
                    <Button
                      variant="ghost"
                      size="sm"
                      onClick={() => setConfirming('')}
                    >
                      Cancel
                    </Button>
                    <Button
                      variant="destructive"
                      size="sm"
                      disabled={busy === `remove:${source.id}`}
                      onClick={() => remove(source)}
                    >
                      {busy === `remove:${source.id}` && (
                        <LoaderCircleIcon className="animate-spin" />
                      )}
                      Remove
                    </Button>
                  </div>
                </div>
              ) : (
                <div
                  key={source.id}
                  className="flex items-center justify-between gap-4 p-4"
                >
                  <div className="min-w-0">
                    <div className="flex items-center gap-2">
                      <span className="truncate text-sm font-medium text-foreground">
                        {source.name || source.id}
                      </span>
                      {source.builtin && (
                        <span className="rounded-md bg-muted px-1.5 py-0.5 text-[10px] text-muted-foreground">
                          Built in
                        </span>
                      )}
                    </div>
                    <code className="mt-1 block truncate font-mono text-[11px] text-muted-foreground">
                      {source.base_url}
                    </code>
                  </div>
                  <div className="flex shrink-0 items-center gap-2">
                    <Switch
                      checked={!!source.enabled}
                      disabled={!!source.builtin || busy === `toggle:${source.id}`}
                      onCheckedChange={() => toggle(source)}
                      aria-label={`${source.enabled ? 'Disable' : 'Enable'} ${source.name || source.id}`}
                    />
                    <Button
                      size="icon-sm"
                      variant="ghost"
                      disabled={!!source.builtin}
                      aria-label={`Remove ${source.name || source.id}`}
                      onClick={() => setConfirming(source.id)}
                    >
                      <Trash2Icon />
                    </Button>
                  </div>
                </div>
              ),
            )}
          </div>
        )}
      </div>

      <div className="mt-5 rounded-xl border border-border bg-muted/30 p-4">
        <h3 className="text-sm font-semibold">Add registry</h3>
        <p className="mt-1 text-xs leading-relaxed text-muted-foreground">
          Private and organization registries use the same API. Add an auth header when the
          catalog requires one.
        </p>
        <div className="mt-4 space-y-3">
          <div>
            <Label htmlFor="registry-name">Name</Label>
            <Input
              id="registry-name"
              className="mt-1.5"
              placeholder="Internal Registry"
              value={name}
              onChange={(event) => setName(event.target.value)}
            />
          </div>
          <div>
            <Label htmlFor="registry-url">Base URL</Label>
            <Input
              id="registry-url"
              type="url"
              className="mt-1.5 font-mono"
              placeholder="https://registry.example.com"
              value={baseUrl}
              onChange={(event) => setBaseUrl(event.target.value)}
            />
          </div>
          <div>
            <Label htmlFor="registry-auth">Auth header</Label>
            <Input
              id="registry-auth"
              type="password"
              autoComplete="off"
              className="mt-1.5 font-mono"
              placeholder="Authorization: Bearer ..."
              value={authHeader}
              onChange={(event) => setAuthHeader(event.target.value)}
            />
          </div>
          <div className="flex justify-end">
            <Button
              onClick={add}
              disabled={busy === 'add' || !name.trim() || !baseUrl.trim()}
            >
              {busy === 'add' && <LoaderCircleIcon className="animate-spin" />}
              Add registry
            </Button>
          </div>
        </div>
      </div>
    </div>
  );
}

function GitImport({ onAdded }: { onAdded: () => Promise<void> }) {
  const [url, setURL] = useState('');
  const [plugin, setPlugin] = useState<any>(null);
  const [loading, setLoading] = useState(false);
  const [adding, setAdding] = useState('');

  const run = async () => {
    setLoading(true);
    setPlugin(null);
    try {
      const res = await apiPost('/mcp/import/git', { url: url.trim() });
      setPlugin(res.plugin);
      toast.add({
        title:
          'Imported ' +
          (res.plugin.servers?.length || 0) +
          ' server(s) from ' +
          res.plugin.slug,
        type: 'success',
      });
    } catch (error) {
      toast.add({
        title: 'Import failed: ' + (error as Error).message,
        type: 'error',
      });
    } finally {
      setLoading(false);
    }
  };

  const add = async (server: any) => {
    setAdding(server.id);
    try {
      await apiPost('/mcp/servers', server);
      toast.add({ title: server.name + ' added', type: 'success' });
      await onAdded();
    } catch (error) {
      toast.add({
        title: 'Could not add server: ' + (error as Error).message,
        type: 'error',
      });
    } finally {
      setAdding('');
    }
  };

  return (
    <div>
      <div className="rounded-xl border border-border bg-muted/30 p-4">
        <div className="flex items-start gap-3">
          <div className="flex size-9 shrink-0 items-center justify-center rounded-lg bg-background text-muted-foreground">
            <GitBranchIcon className="size-4" />
          </div>
          <div>
            <h3 className="text-sm font-semibold">Import from Git</h3>
            <p className="mt-1 text-[13px] leading-relaxed text-muted-foreground">
              Prism performs a shallow clone under its config directory and reads mcp.json or
              plugin.json.
            </p>
          </div>
        </div>
        <Label htmlFor="git-url" className="mt-4 block">
          Repository URL
        </Label>
        <div className="mt-2 flex flex-col gap-2 sm:flex-row">
          <Input
            id="git-url"
            type="url"
            autoComplete="off"
            className="font-mono"
            placeholder="https://github.com/owner/repo"
            value={url}
            onChange={(event) => setURL(event.target.value)}
          />
          <Button
            className="shrink-0"
            onClick={run}
            disabled={loading || !url.trim()}
          >
            {loading ? (
              <LoaderCircleIcon className="animate-spin" />
            ) : (
              <GitBranchIcon />
            )}
            {loading ? 'Cloning...' : 'Import'}
          </Button>
        </div>
      </div>

      {plugin && (
        <div className="mt-5">
          <div className="mb-3 flex items-center justify-between gap-3">
            <div>
              <h3 className="text-sm font-semibold">Imported servers</h3>
              <p className="mt-1 text-xs text-muted-foreground">
                Add each server you want to expose through Prism.
              </p>
            </div>
          </div>
          <code className="mb-3 block truncate rounded-md bg-muted px-2 py-1.5 font-mono text-[11px] text-muted-foreground">
            {plugin.dir}
          </code>
          {plugin.servers?.length ? (
            <div className="overflow-hidden rounded-xl border border-border">
              <div className="divide-y divide-border">
                {plugin.servers.map((server: any) => (
                  <div
                    key={server.id}
                    className="flex items-center justify-between gap-4 p-4"
                  >
                    <div className="min-w-0">
                      <h4 className="text-sm font-medium text-foreground">
                        {server.name}
                      </h4>
                      <code className="mt-1 block truncate font-mono text-[11px] text-muted-foreground">
                        {server.transport === 'stdio'
                          ? [server.command, ...(server.args || [])]
                              .filter(Boolean)
                              .join(' ')
                          : server.url}
                      </code>
                    </div>
                    <Button
                      size="sm"
                      variant="outline"
                      disabled={adding === server.id}
                      onClick={() => add(server)}
                    >
                      {adding === server.id ? (
                        <LoaderCircleIcon className="animate-spin" />
                      ) : (
                        <PlusIcon />
                      )}
                      Add
                    </Button>
                  </div>
                ))}
              </div>
            </div>
          ) : (
            <div className="rounded-xl border border-dashed border-border p-6 text-center text-[13px] text-muted-foreground">
              This repository does not declare any MCP servers.
            </div>
          )}
        </div>
      )}
    </div>
  );
}

function ServerForm({
  server,
  formId,
  idPrefix,
  setSaving,
  onSaved,
}: {
  server?: MCPServer;
  formId: string;
  idPrefix: string;
  setSaving: (saving: boolean) => void;
  onSaved?: () => Promise<void>;
}) {
  const [name, setName] = useState(server?.name || '');
  const [transport, setTransport] = useState(server?.transport || 'stdio');
  const [command, setCommand] = useState(server?.command || 'npx');
  const [args, setArgs] = useState((server?.args || []).join(' '));
  const [cwd, setCwd] = useState(server?.cwd || '');
  const [url, setURL] = useState(server?.url || '');
  const [authMode, setAuthMode] = useState(server?.auth_mode || 'none');
  const [env, setEnv] = useState(kvText(server?.env));
  const [headers, setHeaders] = useState(kvText(server?.headers));
  const [allowlist, setAllowlist] = useState(
    (server?.tool_allowlist || []).join(', '),
  );
  const [clientId, setClientId] = useState(server?.oauth?.client_id || '');
  const [clientSecret, setClientSecret] = useState('');

  const save = async (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault();
    setSaving(true);
    const payload = {
      id: server?.id,
      name: name.trim(),
      transport,
      command,
      args: args
        .split(' ')
        .map((value) => value.trim())
        .filter(Boolean),
      cwd: cwd.trim(),
      url: url.trim(),
      env: parseKV(env),
      headers: parseKV(headers),
      auth_mode: authMode,
      tool_allowlist: allowlist
        .split(',')
        .map((value) => value.trim())
        .filter(Boolean),
      oauth_client_id: clientId.trim(),
      oauth_client_secret: clientSecret,
    };

    try {
      if (server) await apiPost('/mcp/servers/update', payload);
      else await apiPost('/mcp/servers', payload);
      toast.add({
        title: server ? 'Server updated' : 'Server added',
        type: 'success',
      });
      setSaving(false);
      await onSaved?.();
    } catch (error) {
      setSaving(false);
      toast.add({
        title: 'Save failed: ' + (error as Error).message,
        type: 'error',
      });
    }
  };

  return (
    <form id={formId} onSubmit={save} className="space-y-5">
      <div>
        <Label htmlFor={`${idPrefix}-name`}>Name</Label>
        <Input
          id={`${idPrefix}-name`}
          className="mt-1.5"
          value={name}
          onChange={(event) => setName(event.target.value)}
          placeholder="Notion"
          required
        />
      </div>
      <div>
        <Label htmlFor={`${idPrefix}-transport`}>Transport</Label>
        <Select value={transport} onValueChange={setTransport}>
          <SelectTrigger id={`${idPrefix}-transport`} className="mt-1.5 w-full">
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            <SelectItem value="stdio">stdio, local process</SelectItem>
            <SelectItem value="http">HTTP, streamable</SelectItem>
            <SelectItem value="sse">SSE, legacy</SelectItem>
          </SelectContent>
        </Select>
      </div>

      {transport === 'stdio' ? (
        <div className="space-y-4 rounded-xl border border-border bg-muted/25 p-4">
          <div>
            <Label htmlFor={`${idPrefix}-command`}>Command</Label>
            <Input
              id={`${idPrefix}-command`}
              className="mt-1.5 font-mono"
              value={command}
              onChange={(event) => setCommand(event.target.value)}
              placeholder="npx"
            />
            <p className="mt-1.5 text-xs text-muted-foreground">
              Use one executable such as npx, uvx, docker, or a local path.
            </p>
          </div>
          <div>
            <Label htmlFor={`${idPrefix}-args`}>Arguments</Label>
            <Input
              id={`${idPrefix}-args`}
              className="mt-1.5 font-mono"
              value={args}
              onChange={(event) => setArgs(event.target.value)}
              placeholder="-y @notionhq/notion-mcp-server"
            />
          </div>
          <div>
            <Label htmlFor={`${idPrefix}-env`}>Environment</Label>
            <Textarea
              id={`${idPrefix}-env`}
              className="mt-1.5 min-h-24 font-mono text-xs"
              value={env}
              onChange={(event) => setEnv(event.target.value)}
              placeholder={'API_KEY=your-key\nREGION=us-east-1'}
            />
            <p className="mt-1.5 text-xs text-muted-foreground">
              Enter one KEY=value pair per line.
            </p>
          </div>
          <div>
            <Label htmlFor={`${idPrefix}-cwd`}>Working directory</Label>
            <Input
              id={`${idPrefix}-cwd`}
              className="mt-1.5 font-mono"
              value={cwd}
              onChange={(event) => setCwd(event.target.value)}
              placeholder="Optional"
            />
          </div>
        </div>
      ) : (
        <div className="space-y-4 rounded-xl border border-border bg-muted/25 p-4">
          <div>
            <Label htmlFor={`${idPrefix}-url`}>Server URL</Label>
            <Input
              id={`${idPrefix}-url`}
              type="url"
              className="mt-1.5 font-mono"
              value={url}
              onChange={(event) => setURL(event.target.value)}
              placeholder="https://mcp.notion.com/mcp"
            />
          </div>
          <div>
            <Label htmlFor={`${idPrefix}-headers`}>Headers</Label>
            <Textarea
              id={`${idPrefix}-headers`}
              className="mt-1.5 min-h-24 font-mono text-xs"
              value={headers}
              onChange={(event) => setHeaders(event.target.value)}
              placeholder="Authorization=Bearer ..."
            />
            <p className="mt-1.5 text-xs text-muted-foreground">
              Enter one KEY=value pair per line.
            </p>
          </div>
        </div>
      )}

      <div>
        <Label htmlFor={`${idPrefix}-auth`}>Authentication</Label>
        <Select value={authMode} onValueChange={setAuthMode}>
          <SelectTrigger id={`${idPrefix}-auth`} className="mt-1.5 w-full">
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            <SelectItem value="none">None</SelectItem>
            <SelectItem value="static">Static headers or API key</SelectItem>
            <SelectItem value="oauth">OAuth through Prism</SelectItem>
          </SelectContent>
        </Select>
        <p className="mt-1.5 text-xs text-muted-foreground">
          OAuth credentials are stored by Prism and never passed to agents.
        </p>
      </div>

      {authMode === 'oauth' && (
        <div className="grid gap-4 rounded-xl border border-border bg-muted/25 p-4 sm:grid-cols-2">
          <div>
            <Label htmlFor={`${idPrefix}-client-id`}>Client ID</Label>
            <Input
              id={`${idPrefix}-client-id`}
              className="mt-1.5 font-mono"
              value={clientId}
              onChange={(event) => setClientId(event.target.value)}
              placeholder="Optional"
            />
          </div>
          <div>
            <Label htmlFor={`${idPrefix}-client-secret`}>Client secret</Label>
            <Input
              id={`${idPrefix}-client-secret`}
              type="password"
              autoComplete="off"
              className="mt-1.5 font-mono"
              value={clientSecret}
              onChange={(event) => setClientSecret(event.target.value)}
              placeholder={
                server?.oauth?.client_secret
                  ? 'Leave blank to keep current'
                  : 'Optional'
              }
            />
          </div>
        </div>
      )}

      <div>
        <Label htmlFor={`${idPrefix}-allowlist`}>Tool allowlist</Label>
        <Input
          id={`${idPrefix}-allowlist`}
          className="mt-1.5"
          value={allowlist}
          onChange={(event) => setAllowlist(event.target.value)}
          placeholder="search, fetch"
        />
        <p className="mt-1.5 text-xs text-muted-foreground">
          Separate tool names with commas. Leave blank to expose every tool.
        </p>
      </div>
    </form>
  );
}

function Notice({
  title,
  description,
  action,
}: {
  title: string;
  description: string;
  action?: ReactNode;
}) {
  return (
    <div className="flex items-start gap-3 rounded-lg border border-border bg-card p-4">
      <CircleAlertIcon className="mt-0.5 size-4 shrink-0 text-muted-foreground" />
      <div className="min-w-0 flex-1">
        <p className="text-sm font-medium text-foreground">{title}</p>
        <p className="mt-1 text-[13px] leading-relaxed text-muted-foreground">
          {description}
        </p>
      </div>
      {action && <div className="shrink-0">{action}</div>}
    </div>
  );
}

function EmptyServers() {
  return (
    <div className="rounded-xl border border-dashed border-border bg-card p-8 text-center">
      <div className="mx-auto flex size-11 items-center justify-center rounded-lg bg-muted text-muted-foreground">
        <ServerIcon className="size-5" />
      </div>
      <h3 className="mt-4 text-sm font-semibold">No MCP servers connected</h3>
      <p className="mx-auto mt-1 max-w-md text-[13px] leading-relaxed text-muted-foreground">
        Use Add server to browse a registry, enter connection details manually, or import a
        repository with an mcp.json file.
      </p>
    </div>
  );
}

function ServerListSkeleton() {
  return (
    <div className="mt-5 overflow-hidden rounded-xl border border-border bg-card">
      <div className="divide-y divide-border">
        {[0, 1, 2].map((item) => (
          <div key={item} className="flex animate-pulse items-center gap-4 p-5 motion-reduce:animate-none">
            <div className="size-9 shrink-0 rounded-lg bg-muted" />
            <div className="flex-1 space-y-2">
              <div className="h-3 w-36 rounded bg-muted" />
              <div className="h-3 w-2/3 rounded bg-muted" />
            </div>
            <div className="h-5 w-11 rounded-full bg-muted" />
          </div>
        ))}
      </div>
    </div>
  );
}

function RegistrySkeleton() {
  return (
    <div className="space-y-3">
      {[0, 1, 2].map((item) => (
        <div
          key={item}
          className="animate-pulse rounded-xl border border-border p-4 motion-reduce:animate-none"
        >
          <div className="h-4 w-40 rounded bg-muted" />
          <div className="mt-3 h-3 w-full rounded bg-muted" />
          <div className="mt-2 h-3 w-2/3 rounded bg-muted" />
        </div>
      ))}
    </div>
  );
}

function ConfirmRemoveDialog({
  server,
  busy,
  onCancel,
  onConfirm,
}: {
  server: MCPServer;
  busy: boolean;
  onCancel: () => void;
  onConfirm: () => Promise<void>;
}) {
  return (
    <div
      className="fixed inset-0 z-50 flex items-center justify-center bg-black/40 p-4"
      role="dialog"
      aria-modal="true"
      aria-labelledby="mcp-remove-title"
      onClick={onCancel}
      onKeyDown={(event) => {
        if (event.key === 'Escape') onCancel();
      }}
    >
      <div
        className="w-full max-w-sm rounded-xl border border-border bg-card p-5 shadow-lg"
        onClick={(event) => event.stopPropagation()}
      >
        <h3 id="mcp-remove-title" className="text-base font-semibold text-foreground">
          Remove {server.name}?
        </h3>
        <p className="mt-2 text-[13px] leading-relaxed text-muted-foreground">
          This removes the server configuration and any stored OAuth credentials. Agent access
          entries that point to this server will also be removed.
        </p>
        <div className="mt-5 flex justify-end gap-2">
          <Button variant="outline" onClick={onCancel} disabled={busy} autoFocus>
            Cancel
          </Button>
          <Button variant="destructive" onClick={onConfirm} disabled={busy}>
            {busy && <LoaderCircleIcon className="animate-spin" />}
            Remove server
          </Button>
        </div>
      </div>
    </div>
  );
}

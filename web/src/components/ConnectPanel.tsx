import {
  useCallback,
  useEffect,
  useRef,
  useState,
} from 'react';
import { api } from '../api';
import { Button } from '@/components/ui/button';
import { Tabs, TabsContent, TabsList, TabsTrigger } from '@/components/ui/tabs';
import {
  CheckIcon,
  CopyIcon,
  GlobeIcon,
  LockIcon,
  MonitorIcon,
  PlugIcon,
  RefreshCwIcon,
  SearchIcon,
  ServerIcon,
  TerminalIcon,
} from 'lucide-react';
import { cn } from '@/lib/utils';
import { copyText } from '../lib/clipboard';
import { AgentIcon } from './agentIcons';

// Live connection reference: what Prism is running right now, every endpoint it
// exposes, the MCP URLs agents attach to, and generic copy-paste examples.
// Everything comes from GET /admin/connect, which reads local process state.

type ConnectEndpoint = {
  method: string;
  path: string;
  protocol: string;
  auth: boolean;
  description: string;
};

type ConnectService = {
  id: string;
  name: string;
  url: string;
  running: boolean;
  installed?: boolean;
  detail?: string;
};

type ConnectMCPAgent = {
  id: string;
  name: string;
  path: string;
  url: string;
  active: boolean;
  installed: boolean;
};

type ConnectInfo = {
  version: string;
  token: string;
  services: ConnectService[];
  endpoints: ConnectEndpoint[];
  mcp: {
    aggregate_url: string;
    per_agent_pattern: string;
    agents: ConnectMCPAgent[];
  };
};

type Example = {
  id: string;
  label: string;
  code: string;
  disabled?: boolean;
  disabledReason?: string;
};

const POLL_MS = 5000;

function statusLabel(service: ConnectService): string {
  if (service.running) return 'Running';
  if (service.installed === false) return 'Not installed';
  return 'Stopped';
}

function ServiceIcon({ id, className }: { id: string; className?: string }) {
  if (id === 'admin') return <MonitorIcon className={className} strokeWidth={1.5} />;
  if (id === 'searxng') return <SearchIcon className={className} strokeWidth={1.5} />;
  return <ServerIcon className={className} strokeWidth={1.5} />;
}

export default function ConnectPanel({
  onNavigate,
}: {
  onNavigate?: (tab: 'agents' | 'mcp') => void;
}) {
  const [data, setData] = useState<ConnectInfo | null>(null);
  const [error, setError] = useState('');

  const load = useCallback(async () => {
    try {
      const next = await api('/connect');
      setData(next);
      setError('');
    } catch (e) {
      setError((e as Error).message || 'Could not load connection info');
    }
  }, []);

  useEffect(() => {
    load();
    const interval = setInterval(load, POLL_MS);
    return () => clearInterval(interval);
  }, [load]);

  if (!data) {
    return error ? (
      <div className="flex items-start gap-3 rounded-xl border border-border bg-card p-4">
        <GlobeIcon className="mt-0.5 size-4 shrink-0 text-muted-foreground" strokeWidth={1.5} />
        <div className="min-w-0 flex-1">
          <p className="text-sm font-medium text-foreground">Could not load connection info</p>
          <p className="mt-1 text-[13px] leading-relaxed text-muted-foreground">{error}</p>
        </div>
        <Button variant="outline" size="sm" onClick={load}>
          <RefreshCwIcon />
          Retry
        </Button>
      </div>
    ) : (
      <ConnectSkeleton />
    );
  }

  const proxyService = data.services.find((s) => s.id === 'proxy');
  const proxyBase = proxyService?.url ?? '';
  const token = data.token || 'prism';
  const searxngService = data.services.find((s) => s.id === 'searxng');
  const searxngURL = searxngService?.url ?? 'http://127.0.0.1:8888';
  const searxngRunning = !!searxngService?.running;

  const fullURL = (path: string) => (proxyBase ? proxyBase + path : path);

  const examples = buildExamples(proxyBase, token, searxngURL, searxngRunning);

  const proxyStopped = !proxyService?.running;

  return (
    <div className="flex flex-col gap-7">
      <p className="max-w-2xl text-[13px] leading-relaxed text-muted-foreground">
        Point any agent or client at these endpoints. Everything below reflects what Prism is
        running right now and updates on its own — the shared token is{' '}
        <code className="rounded bg-muted px-1.5 py-0.5 font-mono text-[11px]">{token}</code>, and
        no provider keys ever leave this machine.
      </p>

      {/* Services */}
      <section aria-labelledby="connect-services">
        <SectionHeading id="connect-services" title="Live services" />
        <div className="grid gap-3 sm:grid-cols-2 lg:grid-cols-3">
          {data.services.map((service) => (
            <div key={service.id} className="rounded-xl border border-border bg-card p-4">
              <div className="flex items-center gap-2.5">
                <span className="flex size-8 items-center justify-center rounded-md bg-muted text-muted-foreground">
                  <ServiceIcon id={service.id} className="size-4" />
                </span>
                <span className="min-w-0 flex-1 truncate text-sm font-semibold">
                  {service.name}
                </span>
                <span className="inline-flex items-center gap-1.5 text-[11px] font-medium text-muted-foreground">
                  <span
                    className={cn(
                      'inline-block size-2 rounded-full',
                      service.running
                        ? 'bg-green-500 shadow-[0_0_0_3px_rgba(34,197,94,0.18)]'
                        : 'bg-destructive',
                    )}
                  />
                  {statusLabel(service)}
                </span>
              </div>

              <div className="mt-3 flex items-center gap-1.5">
                <code className="min-w-0 flex-1 truncate rounded-md bg-muted px-2 py-1.5 font-mono text-[11px] text-muted-foreground">
                  {service.url}
                </code>
                <CopyButton text={service.url} label={`Copy ${service.name} URL`} />
              </div>

              {service.detail && (
                <p className="mt-2.5 text-[12px] leading-relaxed text-muted-foreground">
                  {service.detail}
                </p>
              )}
            </div>
          ))}
        </div>
      </section>

      {/* API endpoints */}
      <section aria-labelledby="connect-endpoints">
        <SectionHeading
          id="connect-endpoints"
          title="API endpoints"
          hint={proxyBase ? `Prefix everything with ${proxyBase}` : undefined}
        />
        <div className="overflow-hidden rounded-xl border border-border bg-card">
          <div className="overflow-x-auto">
            <table className="w-full min-w-[720px] text-left text-[13px]">
              <thead>
                <tr className="border-b border-border text-[11px] uppercase tracking-wider text-muted-foreground">
                  <th className="px-4 py-2.5 font-semibold">Method</th>
                  <th className="px-4 py-2.5 font-semibold">Path</th>
                  <th className="px-4 py-2.5 font-semibold">Protocol</th>
                  <th className="px-4 py-2.5 font-semibold">Auth</th>
                  <th className="px-4 py-2.5 font-semibold">Description</th>
                  <th className="px-4 py-2.5" />
                </tr>
              </thead>
              <tbody className="divide-y divide-border">
                {data.endpoints.map((endpoint) => (
                  <tr key={`${endpoint.method} ${endpoint.path}`} className="group hover:bg-accent/40">
                    <td className="px-4 py-3 align-top">
                      <span
                        className={cn(
                          'inline-block rounded-md px-1.5 py-0.5 font-mono text-[10px] font-semibold',
                          endpoint.method === 'POST'
                            ? 'bg-primary/10 text-primary'
                            : 'bg-muted text-muted-foreground',
                        )}
                      >
                        {endpoint.method}
                      </span>
                    </td>
                    <td className="px-4 py-3 align-top font-mono text-[12px] text-foreground">
                      {endpoint.path}
                    </td>
                    <td className="px-4 py-3 align-top text-muted-foreground">
                      {endpoint.protocol}
                    </td>
                    <td className="px-4 py-3 align-top">
                      {endpoint.auth ? (
                        <span className="inline-flex items-center gap-1 text-[12px] text-muted-foreground">
                          <LockIcon className="size-3" strokeWidth={1.5} />
                          {token}
                        </span>
                      ) : (
                        <span className="text-[12px] text-muted-foreground">Open</span>
                      )}
                    </td>
                    <td className="px-4 py-3 align-top text-muted-foreground">
                      {endpoint.description}
                    </td>
                    <td className="px-4 py-3 align-top text-right">
                      <CopyButton
                        text={fullURL(endpoint.path)}
                        label={`Copy ${endpoint.path} URL`}
                        className="opacity-0 group-hover:opacity-100 focus-visible:opacity-100 [@media(hover:none)]:opacity-100"
                      />
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        </div>
        {proxyStopped && (
          <p className="mt-2 text-[12px] text-muted-foreground">
            The proxy is stopped, so these endpoints are not accepting requests. Start it from the
            Proxy tab.
          </p>
        )}
      </section>

      {/* MCP */}
      <section aria-labelledby="connect-mcp">
        <SectionHeading
          id="connect-mcp"
          title="MCP endpoints"
          hint="One gateway for every enabled server, with optional per-agent scoping"
        />
        <div className="rounded-xl border border-border bg-card p-4">
          <div className="grid gap-3 sm:grid-cols-2">
            <LabeledURL
              label="Aggregate — every enabled server"
              url={data.mcp.aggregate_url}
              copyLabel="Copy aggregate MCP URL"
            />
            <LabeledURL
              label="Per-agent — that agent's allowlist"
              url={data.mcp.per_agent_pattern}
              copyLabel="Copy per-agent MCP pattern"
            />
          </div>

          {data.mcp.agents.length > 0 && (
            <div className="mt-4 border-t border-border pt-4">
              <p className="mb-2.5 text-[11px] font-semibold uppercase tracking-wider text-muted-foreground">
                Agent endpoints
              </p>
              <div className="grid gap-2 sm:grid-cols-2">
                {data.mcp.agents.map((agent) => (
                  <div
                    key={agent.id}
                    className="flex items-center gap-3 rounded-lg border border-border p-2.5"
                  >
                    <AgentIcon id={agent.id} />
                    <div className="min-w-0 flex-1">
                      <div className="flex items-center gap-2">
                        <span className="truncate text-[13px] font-medium">{agent.name}</span>
                        {agent.active ? (
                          <span className="inline-flex shrink-0 items-center gap-1 text-[11px] text-green-600 dark:text-green-500">
                            <span className="size-1.5 rounded-full bg-green-500" />
                            Connected
                          </span>
                        ) : (
                          <span className="shrink-0 text-[11px] text-muted-foreground">
                            {agent.installed ? 'Not configured' : 'Not installed'}
                          </span>
                        )}
                      </div>
                      <code className="mt-1 block truncate font-mono text-[11px] text-muted-foreground">
                        {agent.url}
                      </code>
                    </div>
                    <CopyButton text={agent.url} label={`Copy ${agent.name} MCP URL`} />
                  </div>
                ))}
              </div>
            </div>
          )}
        </div>
      </section>

      {/* Token */}
      <section aria-labelledby="connect-token">
        <SectionHeading id="connect-token" title="Shared token" />
        <div className="flex items-center gap-3 rounded-xl border border-border bg-card p-4">
          <span className="flex size-8 items-center justify-center rounded-md bg-muted text-muted-foreground">
            <LockIcon className="size-4" strokeWidth={1.5} />
          </span>
          <div className="min-w-0 flex-1">
            <code className="block truncate font-mono text-[12px] text-foreground">{token}</code>
            <p className="mt-0.5 text-[12px] text-muted-foreground">
              Send it as <code className="font-mono">Authorization: Bearer {token}</code> or{' '}
              <code className="font-mono">x-api-key: {token}</code>.
            </p>
          </div>
          <CopyButton text={token} label="Copy shared token" />
        </div>
      </section>

      {/* Examples */}
      <section aria-labelledby="connect-examples">
        <SectionHeading
          id="connect-examples"
          title="Examples"
          hint="Copy-paste ready, with your live address filled in"
        />
        <Tabs defaultValue={examples[0]?.id}>
          <TabsList className="h-auto w-full justify-start overflow-x-auto sm:w-fit">
            {examples.map((example) => (
              <TabsTrigger
                key={example.id}
                value={example.id}
                disabled={example.disabled}
                title={example.disabled ? example.disabledReason : undefined}
                className="min-w-[120px]"
              >
                {example.label}
              </TabsTrigger>
            ))}
          </TabsList>
          {examples.map((example) => (
            <TabsContent key={example.id} value={example.id}>
              <div className="relative rounded-xl border border-border bg-card p-2">
                <pre className="overflow-x-auto rounded-lg bg-muted p-4 pr-14 font-mono text-[12px] leading-relaxed">
                  <code>{example.code}</code>
                </pre>
                <CopyButton
                  text={example.code}
                  label={`Copy ${example.label} example`}
                  className="absolute right-4 top-4"
                />
              </div>
            </TabsContent>
          ))}
        </Tabs>
      </section>

      {onNavigate && (
        <div className="flex flex-wrap items-center gap-2 border-t border-border pt-5 text-[13px] text-muted-foreground">
          <span>Prefer one-click setup?</span>
          <Button variant="outline" size="sm" onClick={() => onNavigate('agents')}>
            <PlugIcon />
            Open Agents
          </Button>
          <Button variant="outline" size="sm" onClick={() => onNavigate('mcp')}>
            <TerminalIcon />
            Open MCP
          </Button>
        </div>
      )}
    </div>
  );
}

function SectionHeading({
  id,
  title,
  hint,
}: {
  id: string;
  title: string;
  hint?: string;
}) {
  return (
    <div className="mb-3 flex flex-wrap items-baseline gap-x-3 gap-y-1">
      <h3 id={id} className="text-sm font-semibold tracking-tight">
        {title}
      </h3>
      {hint && <p className="text-[12px] text-muted-foreground">{hint}</p>}
    </div>
  );
}

function LabeledURL({
  label,
  url,
  copyLabel,
}: {
  label: string;
  url: string;
  copyLabel: string;
}) {
  return (
    <div>
      <p className="mb-1.5 text-[12px] text-muted-foreground">{label}</p>
      <div className="flex items-center gap-1.5">
        <code className="min-w-0 flex-1 truncate rounded-md bg-muted px-2 py-1.5 font-mono text-[11px] text-muted-foreground">
          {url}
        </code>
        <CopyButton text={url} label={copyLabel} />
      </div>
    </div>
  );
}

// CopyButton keeps both glyphs in the DOM and cross-fades them (opacity, scale,
// blur) so enter and exit both animate; the check glyph is the static cue that
// the copy landed. No motion library is installed, hence the CSS approach.
function CopyButton({
  text,
  label,
  className,
}: {
  text: string;
  label: string;
  className?: string;
}) {
  const [copied, setCopied] = useState(false);
  const timer = useRef<number | null>(null);

  useEffect(
    () => () => {
      if (timer.current !== null) window.clearTimeout(timer.current);
    },
    [],
  );

  const handleCopy = async () => {
    const ok = await copyText(text);
    if (!ok) return;
    setCopied(true);
    if (timer.current !== null) window.clearTimeout(timer.current);
    timer.current = window.setTimeout(() => setCopied(false), 1500);
  };

  return (
    <button
      type="button"
      onClick={handleCopy}
      aria-label={copied ? 'Copied' : label}
      title={copied ? 'Copied' : label}
      data-copied={copied ? 'true' : undefined}
      className={cn(
        'inline-flex size-7 shrink-0 items-center justify-center rounded-md text-muted-foreground',
        'transition-[scale,color,background-color] duration-150',
        'hover:bg-accent hover:text-foreground',
        'focus-visible:outline-none focus-visible:ring-[3px] focus-visible:ring-ring/50',
        'active:scale-[0.96] motion-reduce:active:scale-100',
        'data-[copied=true]:text-green-600 dark:data-[copied=true]:text-green-500',
        className,
      )}
    >
      <span className="relative flex items-center justify-center">
        <CopyIcon
          strokeWidth={1.5}
          className={cn(
            'size-3.5 -translate-x-[0.5px] -translate-y-[0.5px]',
            'transition-[opacity,filter,scale] duration-300 ease-[cubic-bezier(0.2,0,0,1)]',
            'motion-reduce:transition-none',
            copied ? 'scale-[0.25] opacity-0 blur-[4px]' : 'scale-100 opacity-100 blur-0',
          )}
        />
        <CheckIcon
          strokeWidth={1.5}
          className={cn(
            'absolute size-3.5',
            'transition-[opacity,filter,scale] duration-300 ease-[cubic-bezier(0.2,0,0,1)]',
            'motion-reduce:transition-none',
            copied ? 'scale-100 opacity-100 blur-0' : 'scale-[0.25] opacity-0 blur-[4px]',
          )}
        />
      </span>
    </button>
  );
}

function buildExamples(
  base: string,
  token: string,
  searxngURL: string,
  searxngRunning: boolean,
): Example[] {
  const model = '<your-model>';
  const openaiBase = base ? `${base}/v1` : 'http://127.0.0.1:11434/v1';

  return [
    {
      id: 'chat',
      label: 'Chat Completions',
      code: `curl ${base}/v1/chat/completions \\
  -H "Authorization: Bearer ${token}" \\
  -H "Content-Type: application/json" \\
  -d '{"model":"${model}","messages":[{"role":"user","content":"Hello!"}]}'`,
    },
    {
      id: 'messages',
      label: 'Anthropic',
      code: `curl ${base}/v1/messages \\
  -H "x-api-key: ${token}" \\
  -H "anthropic-version: 2023-06-01" \\
  -H "Content-Type: application/json" \\
  -d '{"model":"${model}","max_tokens":1024,"messages":[{"role":"user","content":"Hello!"}]}'`,
    },
    {
      id: 'responses',
      label: 'Responses',
      code: `curl ${base}/v1/responses \\
  -H "Authorization: Bearer ${token}" \\
  -H "Content-Type: application/json" \\
  -d '{"model":"${model}","input":"Hello!"}'`,
    },
    {
      id: 'python',
      label: 'Python SDK',
      code: `from openai import OpenAI

client = OpenAI(base_url="${openaiBase}", api_key="${token}")
response = client.chat.completions.create(
    model="${model}",
    messages=[{"role": "user", "content": "Hello!"}],
)
print(response.choices[0].message.content)`,
    },
    {
      id: 'mcp',
      label: 'MCP client',
      code: JSON.stringify(
        {
          mcpServers: {
            prism: {
              type: 'http',
              url: `${base}/mcp`,
              headers: { Authorization: `Bearer ${token}` },
            },
          },
        },
        null,
        2,
      ),
    },
    {
      id: 'search',
      label: 'Web search',
      disabled: !searxngRunning,
      disabledReason: 'Start SearXNG in the SearXNG tab to use this endpoint',
      code: `curl "${searxngURL}/search?q=latest+go+release&format=json"`,
    },
  ];
}

function ConnectSkeleton() {
  return (
    <div className="flex flex-col gap-7" aria-hidden="true">
      <div className="h-4 w-full max-w-2xl animate-pulse rounded bg-muted motion-reduce:animate-none" />
      <div className="grid gap-3 sm:grid-cols-2 lg:grid-cols-3">
        {[0, 1, 2].map((item) => (
          <div
            key={item}
            className="animate-pulse rounded-xl border border-border bg-card p-4 motion-reduce:animate-none"
          >
            <div className="h-4 w-32 rounded bg-muted" />
            <div className="mt-4 h-7 w-full rounded-md bg-muted" />
            <div className="mt-3 h-3 w-2/3 rounded bg-muted" />
          </div>
        ))}
      </div>
      <div className="h-40 animate-pulse rounded-xl border border-border bg-card motion-reduce:animate-none" />
    </div>
  );
}

import { useEffect, useState, useCallback, useRef, memo } from 'react';
import { api, apiPost } from '../api';
import { useToast } from '../ToastContext';
import { useTheme } from '../ThemeContext';
import {
  Chart as ChartJS, CategoryScale, LinearScale, BarController, LineController,
  BarElement, PointElement, LineElement, Filler, Tooltip,
} from 'chart.js';
import { Bar, Line } from 'react-chartjs-2';
import { Button } from '@/components/ui/button';
import { Input } from '@/components/ui/input';
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select';

ChartJS.register(CategoryScale, LinearScale, BarController, LineController, BarElement, PointElement, LineElement, Filler, Tooltip);

const CHART_COLORS = ['#8b5cf6', '#22c55e', '#f59e0b', '#ef4444', '#3b82f6', '#ec4899', '#14b8a6'];
const MAX_LIVE_POINTS = 120;

function formatNumber(n: number): string {
  if (n >= 1000000) return (n / 1000000).toFixed(1) + 'M';
  if (n >= 1000) return (n / 1000).toFixed(1) + 'K';
  return String(n);
}

function fmtLocalDate(d: Date): string {
  return `${d.getFullYear()}-${String(d.getMonth() + 1).padStart(2, '0')}-${String(d.getDate()).padStart(2, '0')}`;
}

function getHeatmapLevel(total: number, maxTotal: number): number {
  total = Number(total) || 0;
  if (total <= 0 || maxTotal <= 0) return 0;
  const ratio = total / maxTotal;
  if (ratio <= 0.05) return 1;
  if (ratio <= 0.20) return 2;
  if (ratio <= 0.45) return 3;
  if (ratio <= 0.75) return 4;
  return 5;
}

function formatHeatmapDate(iso: string): string {
  const d = new Date(iso + 'T00:00:00');
  return d.toLocaleDateString(undefined, { weekday: 'short', year: 'numeric', month: 'short', day: 'numeric' });
}

const FilterBar = memo(function FilterBar({
  filterOpts, timeRange, onTimeRangeChange,
  filterProvider, setFilterProvider,
  filterModel, setFilterModel,
  filterClient, setFilterClient,
  showCustomDate, filterFrom, setFilterFrom, filterTo, setFilterTo,
  refreshAll,
}: {
  filterOpts: { providers: { id: string; name: string }[]; models: string[]; clients: string[] };
  timeRange: string;
  onTimeRangeChange: (val: string) => void;
  filterProvider: string;
  setFilterProvider: (v: string) => void;
  filterModel: string;
  setFilterModel: (v: string) => void;
  filterClient: string;
  setFilterClient: (v: string) => void;
  showCustomDate: boolean;
  filterFrom: string;
  setFilterFrom: (v: string) => void;
  filterTo: string;
  setFilterTo: (v: string) => void;
  refreshAll: () => void;
}) {
  return (
    <div className="flex flex-col gap-3 mb-5 items-center w-full">
      <div className="grid grid-cols-2 md:grid-cols-4 gap-3 w-full">
        <div className="flex flex-col gap-1.5"><div className="text-xs text-muted-foreground font-medium">Time Range</div>
          <Select value={timeRange} onValueChange={(v) => onTimeRangeChange(v)}>
            <SelectTrigger className="w-full"><SelectValue placeholder="Time Range" /></SelectTrigger>
            <SelectContent>
              <SelectItem value="7">Last 7 Days</SelectItem>
              <SelectItem value="30">Last 30 Days</SelectItem>
              <SelectItem value="90">Last 90 Days</SelectItem>
              <SelectItem value="all">All Time</SelectItem>
              <SelectItem value="custom">Custom</SelectItem>
            </SelectContent>
          </Select>
        </div>
        <div className="flex flex-col gap-1.5"><div className="text-xs text-muted-foreground font-medium">Provider</div>
          <Select value={filterProvider} onValueChange={(v) => setFilterProvider(v)}>
            <SelectTrigger className="w-full"><SelectValue placeholder="All Providers" /></SelectTrigger>
            <SelectContent>
              <SelectItem value="">All Providers</SelectItem>
              {filterOpts.providers.map((p: { id: string; name: string }) => <SelectItem key={p.id} value={p.id}>{p.name}</SelectItem>)}
            </SelectContent>
          </Select>
        </div>
        <div className="flex flex-col gap-1.5"><div className="text-xs text-muted-foreground font-medium">Model</div>
          <Select value={filterModel} onValueChange={(v) => setFilterModel(v)}>
            <SelectTrigger className="w-full"><SelectValue placeholder="All Models" /></SelectTrigger>
            <SelectContent>
              <SelectItem value="">All Models</SelectItem>
              {filterOpts.models.map((m: string) => <SelectItem key={m} value={m}>{m}</SelectItem>)}
            </SelectContent>
          </Select>
        </div>
        <div className="flex flex-col gap-1.5"><div className="text-xs text-muted-foreground font-medium">Client</div>
          <Select value={filterClient} onValueChange={(v) => setFilterClient(v)}>
            <SelectTrigger className="w-full"><SelectValue placeholder="All Clients" /></SelectTrigger>
            <SelectContent>
              <SelectItem value="">All Clients</SelectItem>
              {filterOpts.clients.map((c: string) => <SelectItem key={c} value={c}>{c}</SelectItem>)}
            </SelectContent>
          </Select>
        </div>
      </div>
      {showCustomDate && (
        <div className="flex gap-3 items-center w-full justify-between">
          <div className="flex gap-3 items-center">
            <div className="flex items-center gap-1.5"><div className="text-xs text-muted-foreground font-medium">From</div><Input type="date" value={filterFrom} onChange={e => setFilterFrom(e.target.value)} /></div>
            <div className="flex items-center gap-1.5"><div className="text-xs text-muted-foreground font-medium">To</div><Input type="date" value={filterTo} onChange={e => setFilterTo(e.target.value)} /></div>
          </div>
          <Button variant="outline" onClick={refreshAll} title="Refresh stats">
            <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round"><polyline points="23 4 23 10 17 10"/><path d="M20.49 15a9 9 0 1 1-2.12-9.36L23 10"/></svg>
          </Button>
        </div>
      )}
    </div>
  );
});

export default function StatsPanel() {
  const { toast } = useToast();
  const { theme } = useTheme();
  const [liveData, setLiveData] = useState<any>(null);
  const [history, setHistory] = useState<any>(null);
  const [filterOpts, setFilterOpts] = useState<any>({ providers: [], models: [], clients: [] });
  const [timeRange, setTimeRange] = useState('7');
  const [showCustomDate, setShowCustomDate] = useState(false);
  const [filterFrom, setFilterFrom] = useState('');
  const [filterTo, setFilterTo] = useState('');
  const [filterProvider, setFilterProvider] = useState('');
  const [filterModel, setFilterModel] = useState('');
  const [filterClient, setFilterClient] = useState('');
  const [tpsBuffer, setTpsBuffer] = useState<number[]>([]);
  const [showClearModal, setShowClearModal] = useState(false);
  const [hoveredCell, setHoveredCell] = useState<{ x: number; y: number; date: string; input: number; output: number; total: number } | null>(null);
  const liveInterval = useRef<ReturnType<typeof setInterval> | null>(null);
  const historyInterval = useRef<ReturnType<typeof setInterval> | null>(null);

  const chartTheme = {
    grid: theme === 'dark' ? 'rgba(255,255,255,0.06)' : 'rgba(0,0,0,0.06)',
    text: theme === 'dark' ? '#a3a3a3' : '#737373',
  };

  const refreshLive = useCallback(async () => {
    try { const d = await api('/stats'); setLiveData(d); setTpsBuffer(prev => { const buf = [...prev, d.live_tokens_per_sec || 0]; if (buf.length > MAX_LIVE_POINTS) buf.shift(); return buf; }); }
    catch { /* ignore */ }
  }, []);

  const loadHistory = useCallback(async () => {
    try {
      const qs = new URLSearchParams({ from: filterFrom, to: filterTo, provider: filterProvider, model: filterModel, client: filterClient });
      setHistory(await api('/stats/history?' + qs.toString()));
    } catch { /* ignore */ }
  }, [filterFrom, filterTo, filterProvider, filterModel, filterClient]);

  const loadFilterOpts = useCallback(async () => {
    try {
      const data = await api('/stats/filters');
      setFilterOpts({ providers: data.providers || [], models: data.models || [], clients: data.clients || [] });
    } catch { /* ignore */ }
  }, []);

  useEffect(() => {
    const now = new Date();
    const sevenDaysAgo = new Date(now.getTime() - 7 * 24 * 60 * 60 * 1000);
    setFilterFrom(fmtLocalDate(sevenDaysAgo));
    setFilterTo(fmtLocalDate(now));
  }, []);

  useEffect(() => {
    if (!filterFrom || !filterTo) return;
    refreshLive();
    loadHistory();
    loadFilterOpts();
    liveInterval.current = setInterval(refreshLive, 1000);
    historyInterval.current = setInterval(loadHistory, 10000);
    return () => { if (liveInterval.current) clearInterval(liveInterval.current); if (historyInterval.current) clearInterval(historyInterval.current); };
  }, [refreshLive, loadHistory, loadFilterOpts, filterFrom, filterTo]);

  const onTimeRangeChange = useCallback((val: string) => {
    setTimeRange(val);
    if (val === 'custom') { setShowCustomDate(true); return; }
    setShowCustomDate(false);
    const now = new Date();
    let fromDate: Date;
    if (val === 'all') fromDate = new Date(0);
    else fromDate = new Date(now.getTime() - parseInt(val) * 24 * 60 * 60 * 1000);
    setFilterFrom(fmtLocalDate(fromDate));
    setFilterTo(fmtLocalDate(now));
  }, []);

  const refreshAll = useCallback(async () => { await refreshLive(); await loadHistory(); await loadFilterOpts(); }, [refreshLive, loadHistory, loadFilterOpts]);

  const clearStats = async () => {
    setShowClearModal(false);
    try { await apiPost('/stats/clear'); toast('All stats cleared'); loadHistory(); loadFilterOpts(); }
    catch (e) { toast('Failed to clear stats: ' + (e as Error).message, 'error'); }
  };

  // Build heatmap data
  const heatmapData = history?.heatmap_tokens || [];
  const today = new Date(); today.setHours(0, 0, 0, 0);
  const endDate = new Date(today); endDate.setDate(endDate.getDate() + 1);
  const rawStart = new Date(endDate); rawStart.setDate(rawStart.getDate() - 365);
  const startDate = new Date(rawStart); startDate.setDate(startDate.getDate() - startDate.getDay());
  const dayMap: Record<string, any> = {};
  heatmapData.forEach((d: any) => { if (d.date) dayMap[d.date] = { input: Number(d.input) || 0, output: Number(d.output) || 0, total: Number(d.total) || (Number(d.input || 0) + Number(d.output || 0)) }; });
  const allDays: any[] = [];
  for (let d = new Date(startDate); d < endDate; d.setDate(d.getDate() + 1)) { const iso = fmtLocalDate(d); allDays.push({ date: iso, ...(dayMap[iso] || { input: 0, output: 0, total: 0 }) }); }
  const maxTotal = Math.max(...allDays.map(d => d.total), 1);
  const weeks: any[][] = [];
  for (let i = 0; i < allDays.length; i += 7) weeks.push(allDays.slice(i, i + 7));
  while (weeks.length < 53) weeks.push([]);

  // Charts
  const dailyData = history?.daily_tokens || [];
  const monthlyData = history?.monthly_tokens || [];
  const tpsHistoryData = history?.tps_history || [];
  const byModel = history?.by_model || [];
  const byClient = history?.by_client || null;

  const liveTps = liveData?.live_tokens_per_sec || 0;
  const dailyTotal = dailyData.reduce((s: number, d: any) => s + d.total, 0);
  const monthlyTotal = monthlyData.reduce((s: number, d: any) => s + d.total, 0);

  // Client stats
  let clientArr: any[] = [];
  if (Array.isArray(byClient)) clientArr = byClient;
  else if (byClient && typeof byClient === 'object') {
    clientArr = Object.entries(byClient).map(([client, stats]: [string, any]) => ({ client, requests: stats.requests || 0, total_input: stats.input_tokens || 0, total_output: stats.output_tokens || 0, total_tokens: (stats.input_tokens || 0) + (stats.output_tokens || 0) }));
    clientArr.sort((a, b) => b.total_tokens - a.total_tokens);
  }
  const grandTotalTokens = clientArr.reduce((s, c) => s + c.total_tokens, 0);
  const grandTotalRequests = clientArr.reduce((s, c) => s + c.requests, 0);
  const top3 = clientArr.slice(0, 3);
  const maxTokens = top3[0]?.total_tokens || 1;
  const maxRequests = Math.max(...top3.map(c => c.requests), 1);

  const recentLines = (liveData?.recent_requests || []).slice().reverse().map((r: any) => {
    const t = new Date(r.timestamp).toLocaleTimeString();
    return `${t}  ${r.client || 'Unknown'}  ${r.model}  ${r.input_tokens}in/${r.output_tokens}out  ${(r.tokens_per_sec ?? 0).toFixed(1)}tok/s  ${r.duration_ms}ms`;
  }).join('\n');

  const byModelEntries = history?.by_model || [];

  return (
    <>
      {/* Filter bar */}
      <FilterBar
        filterOpts={filterOpts}
        timeRange={timeRange}
        onTimeRangeChange={onTimeRangeChange}
        filterProvider={filterProvider}
        setFilterProvider={setFilterProvider}
        filterModel={filterModel}
        setFilterModel={setFilterModel}
        filterClient={filterClient}
        setFilterClient={setFilterClient}
        showCustomDate={showCustomDate}
        filterFrom={filterFrom}
        setFilterFrom={setFilterFrom}
        filterTo={filterTo}
        setFilterTo={setFilterTo}
        refreshAll={refreshAll}
      />

      <div className="grid grid-cols-1 lg:grid-cols-2 gap-4">
        {/* Heatmap */}
        <div className="rounded-xl border border-border bg-card p-6 col-span-1 lg:col-span-2">
          <div className="flex items-center justify-between mb-3"><span className="text-[13px] font-semibold text-foreground">Token Usage Heatmap</span></div>
          <div className="w-full overflow-visible pb-1">
            <div className="grid grid-cols-[repeat(53,1fr)] grid-rows-7 grid-flow-col gap-[3px] w-full">
              {Array.from({ length: 7 * 53 }, (_, idx) => {
                const dayIdx = idx % 7;
                const weekIdx = Math.floor(idx / 7);
                const day = weeks[weekIdx]?.[dayIdx];
                if (!day) return <div key={idx} className="aspect-square rounded-[2px] border border-border bg-muted invisible" />;
                const level = getHeatmapLevel(day.total, maxTotal);
                return (
                  <div
                    key={idx}
                    className={`aspect-square rounded-[2px] cursor-pointer transition-transform hover:scale-110 hover:z-[1] hover:border-muted-foreground/50 ${
                      level === 0 ? 'bg-muted border border-border' :
                      level === 1 ? 'bg-purple-500/18 border border-purple-500/18' :
                      level === 2 ? 'bg-purple-500/35 border border-purple-500/30' :
                      level === 3 ? 'bg-purple-500/55 border border-purple-500/45' :
                      level === 4 ? 'bg-purple-500/75 border border-purple-500/60' :
                      'bg-purple-500/95 border border-purple-500/85'
                    }`}
                    onMouseEnter={(e) => setHoveredCell({ x: e.clientX, y: e.clientY, date: day.date, input: day.input, output: day.output, total: day.total })}
                    onMouseMove={(e) => setHoveredCell((prev) => prev ? { ...prev, x: e.clientX, y: e.clientY } : null)}
                    onMouseLeave={() => setHoveredCell(null)}
                  />
                );
              })}
            </div>
            {hoveredCell && (
              <div
                className="fixed bg-card border border-border-strong rounded-md px-3 py-2 text-xs leading-relaxed text-foreground shadow-[0_4px_16px_rgba(0,0,0,0.12)] whitespace-nowrap z-[9999] pointer-events-none transition-opacity"
                style={{ left: Math.min(hoveredCell.x + 12, window.innerWidth - 210), top: Math.min(hoveredCell.y + 12, window.innerHeight - 110) }}
              >
                <div className="font-semibold mb-0.5">{formatHeatmapDate(hoveredCell.date)}</div>
                <div className="flex justify-between gap-3"><span className="text-muted-foreground">Input</span><span className="font-semibold tabular-nums text-purple-500">{formatNumber(hoveredCell.input)}</span></div>
                <div className="flex justify-between gap-3"><span className="text-muted-foreground">Output</span><span className="font-semibold tabular-nums text-purple-400">{formatNumber(hoveredCell.output)}</span></div>
                <div className="flex justify-between gap-3"><span className="text-muted-foreground">Total</span><span className="font-semibold tabular-nums">{formatNumber(hoveredCell.total)}</span></div>
              </div>
            )}
          </div>
        </div>

        {/* Daily chart */}
        <div className="rounded-xl border border-border bg-card p-6">
          <div className="flex items-center justify-between mb-3"><span className="text-[13px] font-semibold text-foreground">Tokens Per Day</span></div>
          <div className="relative group">
            <div className="text-2xl font-bold text-foreground tracking-tight leading-tight">{formatNumber(dailyTotal)}<span className="text-xs font-medium text-muted-foreground ml-1">Total</span></div>
            <div className="absolute top-1/2 left-1/2 -translate-x-1/2 -translate-y-1/2 bg-card border border-border-strong rounded-md px-3 py-2.5 text-xs leading-relaxed text-foreground shadow-[0_4px_16px_rgba(0,0,0,0.12)] whitespace-nowrap opacity-0 pointer-events-none transition-opacity group-hover:opacity-100">
              <div className="flex justify-between gap-4"><span className="text-muted-foreground">Input</span><span className="font-semibold text-purple-500">{formatNumber(dailyData.reduce((s: number, d: any) => s + d.input, 0))}</span></div>
              <div className="flex justify-between gap-4"><span className="text-muted-foreground">Output</span><span className="font-semibold text-purple-400">{formatNumber(dailyData.reduce((s: number, d: any) => s + d.output, 0))}</span></div>
            </div>
          </div>
          <div className="relative h-[180px] w-full"><Bar data={{ labels: dailyData.map((d: any) => d.date.slice(5)), datasets: [{ label: 'Input', data: dailyData.map((d: any) => d.input), backgroundColor: 'rgba(139,92,246,0.35)', borderColor: '#8b5cf6', borderWidth: 1, borderRadius: 4, barPercentage: 0.6 }, { label: 'Output', data: dailyData.map((d: any) => d.output), backgroundColor: 'rgba(139,92,246,0.15)', borderColor: 'rgba(139,92,246,0.4)', borderWidth: 1, borderRadius: 4, barPercentage: 0.6 }] }} options={{ responsive: true, maintainAspectRatio: false, plugins: { legend: { display: false }, tooltip: { mode: 'index', intersect: false } }, scales: { x: { grid: { display: false }, ticks: { color: chartTheme.text, font: { size: 11 } } }, y: { grid: { color: chartTheme.grid }, ticks: { color: chartTheme.text, font: { size: 11 }, maxTicksLimit: 6, callback: (v: any) => formatNumber(v) } } } }} /></div>
        </div>

        {/* Monthly chart */}
        <div className="rounded-xl border border-border bg-card p-6">
          <div className="flex items-center justify-between mb-3"><span className="text-[13px] font-semibold text-foreground">Tokens Per Month</span></div>
          <div className="relative group">
            <div className="text-2xl font-bold text-foreground tracking-tight leading-tight">{formatNumber(monthlyTotal)}<span className="text-xs font-medium text-muted-foreground ml-1">Total</span></div>
            <div className="absolute top-1/2 left-1/2 -translate-x-1/2 -translate-y-1/2 bg-card border border-border-strong rounded-md px-3 py-2.5 text-xs leading-relaxed text-foreground shadow-[0_4px_16px_rgba(0,0,0,0.12)] whitespace-nowrap opacity-0 pointer-events-none transition-opacity group-hover:opacity-100">
              <div className="flex justify-between gap-4"><span className="text-muted-foreground">Input</span><span className="font-semibold text-purple-500">{formatNumber(monthlyData.reduce((s: number, d: any) => s + (d.input || 0), 0))}</span></div>
              <div className="flex justify-between gap-4"><span className="text-muted-foreground">Output</span><span className="font-semibold text-purple-400">{formatNumber(monthlyData.reduce((s: number, d: any) => s + (d.output || 0), 0))}</span></div>
            </div>
          </div>
          <div className="relative h-[180px] w-full"><Line data={{ labels: monthlyData.map((d: any) => d.month), datasets: [{ label: 'Tokens', data: monthlyData.map((d: any) => d.total), fill: true, backgroundColor: 'rgba(139,92,246,0.12)', borderColor: '#8b5cf6', borderWidth: 2, pointBackgroundColor: '#8b5cf6', pointRadius: 3, pointHoverRadius: 5, tension: 0.4 }] }} options={{ responsive: true, maintainAspectRatio: false, plugins: { legend: { display: false }, tooltip: { mode: 'index', intersect: false } }, scales: { x: { grid: { display: false }, ticks: { color: chartTheme.text, font: { size: 11 } } }, y: { grid: { color: chartTheme.grid }, ticks: { color: chartTheme.text, font: { size: 11 }, maxTicksLimit: 6, callback: (v: any) => formatNumber(v) } } } }} /></div>
        </div>

        {/* Live TPS */}
        <div className="rounded-xl border border-border bg-card p-6 col-span-1 lg:col-span-2">
          <div className="flex items-center justify-between mb-3">
            <h3 className="text-sm font-semibold m-0">Live TPS</h3>
            {liveData?.request_active && <span className="inline-flex items-center gap-1.5 text-[11px] font-semibold text-green-500 bg-green-500/8 border border-green-500/20 rounded-full px-2 py-0.5 whitespace-nowrap"><span className="w-2 h-2 rounded-full bg-green-500 animate-pulse" /> Updated just now</span>}
          </div>
          <div className="flex flex-col sm:flex-row items-start sm:items-center gap-4">
            <div className="w-full sm:w-auto sm:min-w-[160px] text-center sm:text-center py-2 sm:py-5">
              <div className={`text-4xl sm:text-5xl font-bold tracking-[-2px] leading-none ${liveData?.request_active ? 'text-green-500' : 'text-foreground'}`}>{liveData?.request_active ? (liveTps > 0 ? liveTps.toFixed(1) : '0') : '--'}</div>
              <div className="text-[13px] text-muted-foreground mt-1 font-medium">tokens/sec</div>
              <div className="text-xs text-muted-foreground/70 mt-1.5">{liveData?.request_active ? ((liveData.current_model || 'Processing...') + (liveData.current_provider ? ' via ' + liveData.current_provider : '')) : (liveData?.total_requests > 0 ? 'Idle \u2014 last: ' + (liveData.current_model || '') : 'No active request')}</div>
            </div>
            <div className="w-full sm:flex-1 relative h-[120px] min-w-0">
              <Line data={{ labels: tpsBuffer.map(() => ''), datasets: [{ data: tpsBuffer, borderColor: '#8b5cf6', backgroundColor: 'rgba(139,92,246,0.08)', borderWidth: 2, fill: true, pointRadius: 0, tension: 0.4 }] }} options={{ responsive: true, maintainAspectRatio: false, plugins: { legend: { display: false }, tooltip: { enabled: true, mode: 'index', intersect: false, callbacks: { title: () => '', label: (ctx: any) => ctx.parsed.y != null ? ctx.parsed.y.toFixed(1) + ' tok/s' : '' } } }, scales: { x: { display: false }, y: { display: false, min: 0 } }, animation: { duration: 0 } }} />
            </div>
          </div>
          <div className="grid grid-cols-4 gap-3 mt-4">
            <div className="text-center py-3 bg-muted border border-border rounded-md"><div className="text-xl font-bold text-foreground tracking-tight">{liveData?.total_requests || 0}</div><div className="text-[11px] text-muted-foreground mt-1 font-medium">Total Requests</div></div>
            <div className="text-center py-3 bg-muted border border-border rounded-md"><div className="text-xl font-bold text-foreground tracking-tight">{formatNumber(liveData?.total_input_tokens || 0)}</div><div className="text-[11px] text-muted-foreground mt-1 font-medium">Input Tokens</div></div>
            <div className="text-center py-3 bg-muted border border-border rounded-md"><div className="text-xl font-bold text-foreground tracking-tight">{formatNumber(liveData?.total_output_tokens || 0)}</div><div className="text-[11px] text-muted-foreground mt-1 font-medium">Output Tokens</div></div>
            <div className="text-center py-3 bg-muted border border-border rounded-md"><div className="text-xl font-bold text-foreground tracking-tight">{liveData?.avg_tokens_per_sec > 0 ? liveData.avg_tokens_per_sec.toFixed(1) : '--'}</div><div className="text-[11px] text-muted-foreground mt-1 font-medium">Avg tok/sec</div></div>
          </div>
        </div>

        {/* Client stats */}
        <div className="rounded-xl border border-border bg-card p-6 col-span-1 lg:col-span-2">
          <div className="flex items-start justify-between mb-5">
            <div><div className="text-sm font-semibold text-foreground tracking-tight">Usage by Client</div><div className="text-xs text-muted-foreground mt-1">Track API usage across your clients and tools.</div></div>
          </div>
          <div className="grid grid-cols-3 gap-4 mb-5 pb-4 border-b border-border">
            <div><div className="text-[11px] text-muted-foreground font-medium mb-1">Total Clients</div><div className="text-[22px] font-bold text-foreground tracking-tight">{clientArr.length}</div></div>
            <div><div className="text-[11px] text-muted-foreground font-medium mb-1">Total Tokens</div><div className="text-[22px] font-bold text-foreground tracking-tight">{formatNumber(grandTotalTokens)}</div></div>
            <div><div className="text-[11px] text-muted-foreground font-medium mb-1">Total Requests</div><div className="text-[22px] font-bold text-foreground tracking-tight">{formatNumber(grandTotalRequests)}</div></div>
          </div>
          <div className="grid grid-cols-4 gap-3 py-2 text-[11px] text-muted-foreground font-medium border-b border-border mb-1">
            <div>Client</div><div>Tokens</div><div>Requests</div><div>% of Total</div>
          </div>
          {top3.length === 0 ? <div className="text-muted-foreground text-[13px] italic py-2">No data yet.</div> : top3.map((c, i) => {
            const barColors = ['bg-purple-500', 'bg-blue-500', 'bg-green-500'];
            const barClass = barColors[i] || barColors[0];
            const tokenPct = grandTotalTokens > 0 ? ((c.total_tokens / grandTotalTokens) * 100).toFixed(1) : '0.0';
            return (
              <div className="grid grid-cols-4 gap-3 py-2.5 border-b border-border items-center" key={c.client}>
                <div><div className="text-[13px] font-semibold text-foreground">{c.client}</div><div className="text-[11px] text-muted-foreground/60 mt-0.5">{c.client.toLowerCase().replace(/\s+/g, '-')}</div></div>
                <div><div className="text-[13px] font-medium text-foreground">{formatNumber(c.total_tokens)}</div><div className="w-full h-1 bg-muted rounded-sm overflow-hidden mt-1.5"><div className={`h-full rounded-sm ${barClass} transition-[width] duration-400`} style={{ width: `${(c.total_tokens / maxTokens) * 100}%` }} /></div></div>
                <div><div className="text-[13px] font-medium text-foreground">{formatNumber(c.requests)}</div><div className="w-full h-1 bg-muted rounded-sm overflow-hidden mt-1.5"><div className={`h-full rounded-sm ${barClass} transition-[width] duration-400`} style={{ width: `${(c.requests / maxRequests) * 100}%` }} /></div></div>
                <div><div className="text-[13px] font-medium text-foreground">{tokenPct}%</div><div className="w-full h-1 bg-muted rounded-sm overflow-hidden mt-1.5"><div className={`h-full rounded-sm ${barClass} transition-[width] duration-400`} style={{ width: `${tokenPct}%` }} /></div></div>
              </div>
            );
          })}
        </div>

        {/* TPS History */}
        <div className="rounded-xl border border-border bg-card p-6 col-span-1 lg:col-span-2">
          <div className="flex items-center justify-between mb-3"><h3 className="text-sm font-semibold m-0">TPS History (Tokens / Second)</h3></div>
          <table className="w-full border-collapse text-[13px] mb-4">
            <thead><tr><th className="text-left px-3 py-2 text-muted-foreground font-medium border-b border-border text-xs">Model</th><th className="text-left px-3 py-2 text-muted-foreground font-medium border-b border-border text-xs">Provider</th><th className="text-left px-3 py-2 text-muted-foreground font-medium border-b border-border text-xs">Avg TPS</th><th className="text-left px-3 py-2 text-muted-foreground font-medium border-b border-border text-xs">Max TPS</th></tr></thead>
            <tbody>
              {byModel.length === 0 ? <tr><td colSpan={4} className="text-muted-foreground italic text-center py-4">No data yet.</td></tr> :
                byModel.map((m: any, i: number) => <tr key={i}><td className="px-3 py-2 border-b border-border"><span className="inline-block w-2 h-2 rounded-full mr-2" style={{ background: CHART_COLORS[i % CHART_COLORS.length] }} />{m.model}</td><td className="px-3 py-2 border-b border-border">{m.provider}</td><td className="px-3 py-2 border-b border-border">{(m.avg_tps ?? 0).toFixed(1)}</td><td className="px-3 py-2 border-b border-border">{(m.max_tps ?? 0).toFixed(1)}</td></tr>)}
            </tbody>
          </table>
          <div className="relative h-[220px] w-full">
            {(() => {
              const byModelMap: Record<string, any[]> = {};
              tpsHistoryData.forEach((p: any) => { if (!byModelMap[p.model]) byModelMap[p.model] = []; byModelMap[p.model].push(p); });
              const allModelNames = [...new Set([...Object.keys(byModelMap), ...byModel.map((m: any) => m.model)])];
              const timestamps: number[] = tpsHistoryData.map((p: any) => p.timestamp as number);
              const allTimestamps = [...new Set(timestamps)].sort((a, b) => a - b);
              const labels = allTimestamps.map((ts: number) => { const d = new Date(ts * 1000); return d.getHours().toString().padStart(2, '0') + ':' + d.getMinutes().toString().padStart(2, '0'); });
              const datasets = allModelNames.map((model, i) => {
                const color = CHART_COLORS[i % CHART_COLORS.length];
                const points = byModelMap[model] || [];
                const map: Record<number, number> = {}; points.forEach((p: any) => { map[p.timestamp] = p.avg_tps; });
                return { label: model, data: allTimestamps.map((ts: number) => map[ts] || null), borderColor: color, backgroundColor: color, borderWidth: 2, pointRadius: 2, pointHoverRadius: 4, tension: 0.3, spanGaps: true };
              });
              return <Line data={{ labels, datasets }} options={{ responsive: true, maintainAspectRatio: false, plugins: { legend: { display: false }, tooltip: { mode: 'index', intersect: false } }, scales: { x: { grid: { display: false }, ticks: { color: chartTheme.text, font: { size: 10 }, maxTicksLimit: 8 } }, y: { grid: { color: chartTheme.grid }, ticks: { color: chartTheme.text, font: { size: 11 }, maxTicksLimit: 6 } } } }} />;
            })()}
          </div>
        </div>

        {/* By Model */}
        <div className="rounded-xl border border-border bg-card p-6">
          <h3 className="text-sm font-semibold tracking-tight mb-4">By Model</h3>
          {byModelEntries.length > 0 ? byModelEntries.map((m: any) => (
            <div className="flex justify-between items-center py-2.5 border-b border-border last:border-b-0 text-[13px]" key={m.model}>
              <span className="font-medium text-foreground">{m.model}</span>
              <span className="text-muted-foreground">{m.requests} req &middot; {formatNumber(m.total_input ?? m.input_tokens ?? 0)} in / {formatNumber(m.total_output ?? m.output_tokens ?? 0)} out &middot; {(m.avg_tps ?? m.avg_tokens_per_sec ?? 0).toFixed(1)} tok/s</span>
            </div>
          )) : <div className="text-muted-foreground text-[13px] italic">No data yet.</div>}
        </div>

        {/* Recent Requests */}
        <div className="rounded-xl border border-border bg-card p-6">
          <h3 className="text-sm font-semibold tracking-tight mb-4">Recent Requests</h3>
          <div className="bg-muted border border-border rounded-md p-3.5 max-h-[200px] overflow-y-auto font-mono text-xs leading-relaxed text-muted-foreground whitespace-pre-wrap break-words">{recentLines || 'No data yet.'}</div>
        </div>

        {/* Data Management */}
        <div className="rounded-xl border border-border bg-card p-6 flex justify-between items-center">
          <div><h3 className="text-sm font-semibold m-0">Data Management</h3><p className="text-[13px] text-muted-foreground mt-0.5 mb-0">Delete all persisted stats. This cannot be undone.</p></div>
          <Button variant="destructive" onClick={() => setShowClearModal(true)}>Clear All Stats</Button>
        </div>
      </div>

      {showClearModal && (
        <div className="fixed inset-0 bg-black/35 z-[10000] flex items-center justify-center" onClick={() => setShowClearModal(false)}>
          <div className="bg-card border border-border rounded-xl p-6 max-w-[420px] w-[calc(100%-48px)] shadow-[0_8px_30px_rgba(0,0,0,0.12)]" onClick={e => e.stopPropagation()}>
            <h3 className="text-sm font-semibold tracking-tight mb-2">Clear All Stats</h3>
            <p className="text-[13px] text-muted-foreground leading-relaxed mb-5">Are you sure you want to delete all persisted stats? This cannot be undone.</p>
            <div className="flex gap-2.5 justify-end">
              <Button variant="outline" onClick={() => setShowClearModal(false)}>Cancel</Button>
              <Button variant="destructive" onClick={clearStats}>Delete</Button>
            </div>
          </div>
        </div>
      )}
    </>
  );
}

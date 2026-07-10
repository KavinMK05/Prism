import { useEffect, useState, useCallback, useRef } from 'react';
import { api, apiPost } from '../api';
import { useToast } from '../ToastContext';
import { useTheme } from '../ThemeContext';
import {
  Chart as ChartJS, CategoryScale, LinearScale, BarController, LineController,
  BarElement, PointElement, LineElement, Filler, Tooltip,
} from 'chart.js';
import { Bar, Line } from 'react-chartjs-2';

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

  const onTimeRangeChange = (val: string) => {
    setTimeRange(val);
    if (val === 'custom') { setShowCustomDate(true); return; }
    setShowCustomDate(false);
    const now = new Date();
    let fromDate: Date;
    if (val === 'all') fromDate = new Date(0);
    else fromDate = new Date(now.getTime() - parseInt(val) * 24 * 60 * 60 * 1000);
    setFilterFrom(fmtLocalDate(fromDate));
    setFilterTo(fmtLocalDate(now));
  };

  const refreshAll = async () => { await refreshLive(); await loadHistory(); await loadFilterOpts(); };

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
    return `${t}  ${r.client || 'Unknown'}  ${r.model}  ${r.input_tokens}in/${r.output_tokens}out  ${r.tokens_per_sec.toFixed(1)}tok/s  ${r.duration_ms}ms`;
  }).join('\n');

  const byModelEntries = liveData?.by_model ? Object.entries(liveData.by_model) : [];

  return (
    <>
      <div className="stats-filter-bar">
        <div className="filter-row center">
          <div className="filter-group"><div className="stats-filter-label">Time Range</div>
            <select value={timeRange} onChange={e => onTimeRangeChange(e.target.value)}>
              <option value="7">Last 7 Days</option><option value="30">Last 30 Days</option><option value="90">Last 90 Days</option><option value="all">All Time</option><option value="custom">Custom</option>
            </select>
          </div>
          <div className="filter-group"><div className="stats-filter-label">Provider</div>
            <select value={filterProvider} onChange={e => { setFilterProvider(e.target.value); }}><option value="">All Providers</option>{filterOpts.providers.map((p: string) => <option key={p} value={p}>{p}</option>)}</select>
          </div>
          <div className="filter-group"><div className="stats-filter-label">Model</div>
            <select value={filterModel} onChange={e => { setFilterModel(e.target.value); }}><option value="">All Models</option>{filterOpts.models.map((m: string) => <option key={m} value={m}>{m}</option>)}</select>
          </div>
          <div className="filter-group"><div className="stats-filter-label">Client</div>
            <select value={filterClient} onChange={e => { setFilterClient(e.target.value); }}><option value="">All Clients</option>{filterOpts.clients.map((c: string) => <option key={c} value={c}>{c}</option>)}</select>
          </div>
        </div>
        {showCustomDate && (
          <div className="filter-row between">
            <div style={{ display: 'flex', gap: '12px', alignItems: 'center' }}>
              <div className="filter-group"><div className="stats-filter-label">From</div><input type="date" value={filterFrom} onChange={e => setFilterFrom(e.target.value)} /></div>
              <div className="filter-group"><div className="stats-filter-label">To</div><input type="date" value={filterTo} onChange={e => setFilterTo(e.target.value)} /></div>
            </div>
            <button className="stats-refresh-btn" onClick={refreshAll} title="Refresh stats">
              <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round"><polyline points="23 4 23 10 17 10"/><path d="M20.49 15a9 9 0 1 1-2.12-9.36L23 10"/></svg>
            </button>
          </div>
        )}
      </div>

      <div className="stats-layout">
        <div className="card heatmap-card">
          <div className="chart-card-header"><span className="chart-card-title">Token Usage Heatmap</span></div>
          <div className="heatmap-wrapper">
            <div className="heatmap-grid">
              {Array.from({ length: 7 * 53 }, (_, idx) => {
                const dayIdx = idx % 7;
                const weekIdx = Math.floor(idx / 7);
                const day = weeks[weekIdx]?.[dayIdx];
                if (!day) return <div key={idx} className="heatmap-cell level-0" style={{ visibility: 'hidden', pointerEvents: 'none' }} />;
                const level = getHeatmapLevel(day.total, maxTotal);
                return <div key={idx} className={`heatmap-cell level-${level}`} title={`${formatHeatmapDate(day.date)}: ${formatNumber(day.total)} total`} />;
              })}
            </div>
          </div>
        </div>

        <div className="chart-card">
          <div className="chart-card-header"><span className="chart-card-title">Tokens Per Day</span></div>
          <div className="token-tooltip-wrapper">
            <div className="chart-card-value">{formatNumber(dailyTotal)}<span className="unit">Total</span></div>
            <div className="token-tooltip">
              <div className="token-tooltip-row"><span className="token-tooltip-label">Input</span><span className="token-tooltip-value input-color">{formatNumber(dailyData.reduce((s: number, d: any) => s + d.input, 0))}</span></div>
              <div className="token-tooltip-row"><span className="token-tooltip-label">Output</span><span className="token-tooltip-value output-color">{formatNumber(dailyData.reduce((s: number, d: any) => s + d.output, 0))}</span></div>
            </div>
          </div>
          <div className="chart-container"><Bar data={{ labels: dailyData.map((d: any) => d.date.slice(5)), datasets: [{ label: 'Input', data: dailyData.map((d: any) => d.input), backgroundColor: 'rgba(139,92,246,0.35)', borderColor: '#8b5cf6', borderWidth: 1, borderRadius: 4, barPercentage: 0.6 }, { label: 'Output', data: dailyData.map((d: any) => d.output), backgroundColor: 'rgba(139,92,246,0.15)', borderColor: 'rgba(139,92,246,0.4)', borderWidth: 1, borderRadius: 4, barPercentage: 0.6 }] }} options={{ responsive: true, maintainAspectRatio: false, plugins: { legend: { display: false }, tooltip: { mode: 'index', intersect: false } }, scales: { x: { grid: { display: false }, ticks: { color: chartTheme.text, font: { size: 11 } } }, y: { grid: { color: chartTheme.grid }, ticks: { color: chartTheme.text, font: { size: 11 }, maxTicksLimit: 6, callback: (v: any) => formatNumber(v) } } } }} /></div>
        </div>

        <div className="chart-card">
          <div className="chart-card-header"><span className="chart-card-title">Tokens Per Month</span></div>
          <div className="token-tooltip-wrapper">
            <div className="chart-card-value">{formatNumber(monthlyTotal)}<span className="unit">Total</span></div>
            <div className="token-tooltip">
              <div className="token-tooltip-row"><span className="token-tooltip-label">Input</span><span className="token-tooltip-value input-color">{formatNumber(monthlyData.reduce((s: number, d: any) => s + (d.input || 0), 0))}</span></div>
              <div className="token-tooltip-row"><span className="token-tooltip-label">Output</span><span className="token-tooltip-value output-color">{formatNumber(monthlyData.reduce((s: number, d: any) => s + (d.output || 0), 0))}</span></div>
            </div>
          </div>
          <div className="chart-container"><Line data={{ labels: monthlyData.map((d: any) => d.month), datasets: [{ label: 'Tokens', data: monthlyData.map((d: any) => d.total), fill: true, backgroundColor: 'rgba(139,92,246,0.12)', borderColor: '#8b5cf6', borderWidth: 2, pointBackgroundColor: '#8b5cf6', pointRadius: 3, pointHoverRadius: 5, tension: 0.4 }] }} options={{ responsive: true, maintainAspectRatio: false, plugins: { legend: { display: false }, tooltip: { mode: 'index', intersect: false } }, scales: { x: { grid: { display: false }, ticks: { color: chartTheme.text, font: { size: 11 } } }, y: { grid: { color: chartTheme.grid }, ticks: { color: chartTheme.text, font: { size: 11 }, maxTicksLimit: 6, callback: (v: any) => formatNumber(v) } } } }} /></div>
        </div>

        <div className="card">
          <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between', marginBottom: '12px' }}>
            <h3 style={{ margin: 0 }}>Live TPS</h3>
            {liveData?.request_active && <span className="live-badge-inline"><span className="pulse-dot" /> Updated just now</span>}
          </div>
          <div className="stats-live">
            <div className="stats-hero" style={{ flex: '0.3', minWidth: '160px' }}>
              <div className={`stats-hero-value ${liveData?.request_active ? 'active' : ''}`}>{liveData?.request_active ? (liveTps > 0 ? liveTps.toFixed(1) : '0') : '--'}</div>
              <div className="stats-hero-unit">tokens/sec</div>
              <div className="stats-hero-label">{liveData?.request_active ? ((liveData.current_model || 'Processing...') + (liveData.current_provider ? ' via ' + liveData.current_provider : '')) : (liveData?.total_requests > 0 ? 'Idle \u2014 last: ' + (liveData.current_model || '') : 'No active request')}</div>
            </div>
            <div style={{ flex: 1, position: 'relative', height: '120px' }}>
              <Line data={{ labels: tpsBuffer.map(() => ''), datasets: [{ data: tpsBuffer, borderColor: '#8b5cf6', backgroundColor: 'rgba(139,92,246,0.08)', borderWidth: 2, fill: true, pointRadius: 0, tension: 0.4 }] }} options={{ responsive: true, maintainAspectRatio: false, plugins: { legend: { display: false }, tooltip: { enabled: true, mode: 'index', intersect: false, callbacks: { title: () => '', label: (ctx: any) => ctx.parsed.y != null ? ctx.parsed.y.toFixed(1) + ' tok/s' : '' } } }, scales: { x: { display: false }, y: { display: false, min: 0 } }, animation: { duration: 0 } }} />
            </div>
          </div>
          <div className="stats-grid" style={{ marginTop: '16px' }}>
            <div className="stats-item"><div className="stats-item-value">{liveData?.total_requests || 0}</div><div className="stats-item-label">Total Requests</div></div>
            <div className="stats-item"><div className="stats-item-value">{formatNumber(liveData?.total_input_tokens || 0)}</div><div className="stats-item-label">Input Tokens</div></div>
            <div className="stats-item"><div className="stats-item-value">{formatNumber(liveData?.total_output_tokens || 0)}</div><div className="stats-item-label">Output Tokens</div></div>
            <div className="stats-item"><div className="stats-item-value">{liveData?.avg_tokens_per_sec > 0 ? liveData.avg_tokens_per_sec.toFixed(1) : '--'}</div><div className="stats-item-label">Avg tok/sec</div></div>
          </div>
        </div>

        <div className="card">
          <div className="client-card-header"><div><div className="client-card-title">Usage by Client</div><div className="client-card-subtitle">Track API usage across your clients and tools.</div></div></div>
          <div className="client-summary-row">
            <div className="client-summary-item"><div className="client-summary-label">Total Clients</div><div className="client-summary-value">{clientArr.length}</div></div>
            <div className="client-summary-item"><div className="client-summary-label">Total Tokens</div><div className="client-summary-value">{formatNumber(grandTotalTokens)}</div></div>
            <div className="client-summary-item"><div className="client-summary-label">Total Requests</div><div className="client-summary-value">{formatNumber(grandTotalRequests)}</div></div>
          </div>
          <div className="client-list-header"><div>Client</div><div>Tokens</div><div>Requests</div><div>% of Total</div></div>
          {top3.length === 0 ? <div style={{ color: 'var(--text-secondary)', fontSize: '13px', fontStyle: 'italic', padding: '8px 0' }}>No data yet.</div> : top3.map((c, i) => {
            const barClass = i === 0 ? 'client-bar-fill' : i === 1 ? 'client-bar-fill client-bar-fill-alt' : 'client-bar-fill client-bar-fill-alt2';
            const tokenPct = ((c.total_tokens / grandTotalTokens) * 100).toFixed(1);
            return (
              <div className="client-list-row" key={c.client}>
                <div><div className="client-info-name">{c.client}</div><div className="client-info-id">{c.client.toLowerCase().replace(/\s+/g, '-')}</div></div>
                <div><div className="client-metric">{formatNumber(c.total_tokens)}</div><div className="client-bar-bg"><div className={barClass} style={{ width: `${(c.total_tokens / maxTokens) * 100}%` }} /></div></div>
                <div><div className="client-metric">{formatNumber(c.requests)}</div><div className="client-bar-bg"><div className={barClass} style={{ width: `${(c.requests / maxRequests) * 100}%` }} /></div></div>
                <div><div className="client-metric">{tokenPct}%</div><div className="client-bar-bg"><div className={barClass} style={{ width: `${tokenPct}%` }} /></div></div>
              </div>
            );
          })}
        </div>

        <div className="card">
          <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between', marginBottom: '12px' }}><h3 style={{ margin: 0 }}>TPS History (Tokens / Second)</h3></div>
          <table className="tps-table">
            <thead><tr><th>Model</th><th>Provider</th><th>Avg TPS</th><th>Max TPS</th></tr></thead>
            <tbody>
              {byModel.length === 0 ? <tr><td colSpan={4} style={{ color: 'var(--text-secondary)', fontStyle: 'italic', textAlign: 'center', padding: '16px' }}>No data yet.</td></tr> :
                byModel.map((m: any, i: number) => <tr key={i}><td><span className="model-dot" style={{ background: CHART_COLORS[i % CHART_COLORS.length] }} />{m.model}</td><td>{m.provider}</td><td>{m.avg_tps.toFixed(1)}</td><td>{m.max_tps.toFixed(1)}</td></tr>)}
            </tbody>
          </table>
          <div className="chart-container" style={{ height: '220px' }}>
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

        <div className="card">
          <h3>By Model</h3>
          {byModelEntries.length > 0 ? byModelEntries.map(([model, stats]: [string, any]) => (
            <div className="model-stats-row" key={model}>
              <span className="model-stats-name">{model}</span>
              <span className="model-stats-detail">{stats.requests} req · {formatNumber(stats.input_tokens)} in / {formatNumber(stats.output_tokens)} out · {stats.avg_tokens_per_sec.toFixed(1)} tok/s</span>
            </div>
          )) : <div style={{ color: 'var(--text-secondary)', fontSize: '13px', fontStyle: 'italic' }}>No data yet.</div>}
        </div>

        <div className="card">
          <h3>Recent Requests</h3>
          <div className="log-view" style={{ maxHeight: '200px' }}>{recentLines || 'No data yet.'}</div>
        </div>

        <div className="card" style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center' }}>
          <div><h3 style={{ margin: 0 }}>Data Management</h3><p className="card-description" style={{ marginBottom: 0 }}>Delete all persisted stats. This cannot be undone.</p></div>
          <button className="btn btn-danger" onClick={() => setShowClearModal(true)}>Clear All Stats</button>
        </div>
      </div>

      {showClearModal && (
        <div className="modal-overlay show" onClick={() => setShowClearModal(false)}>
          <div className="modal-card" onClick={e => e.stopPropagation()}>
            <h3>Clear All Stats</h3>
            <p>Are you sure you want to delete all persisted stats? This cannot be undone.</p>
            <div className="btn-row">
              <button className="btn btn-ghost" onClick={() => setShowClearModal(false)}>Cancel</button>
              <button className="btn btn-danger" onClick={clearStats}>Delete</button>
            </div>
          </div>
        </div>
      )}
    </>
  );
}

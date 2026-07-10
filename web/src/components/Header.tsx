import { useTheme } from '../ThemeContext';

export default function Header({ running }: { running: boolean | null }) {
  const { theme, toggleTheme } = useTheme();

  return (
    <div className="page-header">
      <div className="page-header-left">
        <img src="/admin/icon.png" alt="Prism" className="brand-icon" />
        <h1>Prism Settings</h1>
        <span className="status-pill">
          <span className={`status-dot ${running ? 'running' : 'stopped'}`} />
          <span>{running === null ? '\u2014' : running ? 'Running' : 'Stopped'}</span>
        </span>
      </div>
      <button className="theme-toggle" onClick={toggleTheme} title="Toggle theme">
        {theme === 'dark' ? (
          <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round"><path d="M21 12.79A9 9 0 1 1 11.21 3 7 7 0 0 0 21 12.79z"/></svg>
        ) : (
          <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round"><circle cx="12" cy="12" r="5"/><path d="M12 1v2M12 21v2M4.22 4.22l1.42 1.42M18.36 18.36l1.42 1.42M1 12h2M21 12h2M4.22 19.78l1.42-1.42M18.36 5.64l1.42-1.42"/></svg>
        )}
      </button>
    </div>
  );
}

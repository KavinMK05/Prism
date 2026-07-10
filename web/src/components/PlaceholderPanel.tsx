import { useToast } from '../ToastContext';

export default function PlaceholderPanel({ name }: { name: string }) {
  const { toast } = useToast();
  return (
    <div className="placeholder-panel">
      <p>{name} panel is not yet migrated to React.</p>
      <p>Use the <a href="/admin-legacy">legacy admin page</a> for this panel.</p>
      <p style={{ marginTop: '16px' }}>
        <button className="btn btn-ghost" onClick={() => toast('This panel will be migrated soon', 'success')}>
          Dismiss
        </button>
      </p>
    </div>
  );
}

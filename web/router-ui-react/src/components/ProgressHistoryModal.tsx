import React, { useEffect, useState } from 'react';

export interface ProgressHistoryEntry {
  percent: number;
  note: string | null;
  timestamp: string;
}

interface ProgressHistoryModalProps {
  agentId: string;
  isOpen?: boolean;
  onClose: () => void;
}

export const ProgressHistoryModal: React.FC<ProgressHistoryModalProps> = ({ agentId, isOpen = true, onClose }) => {
  const [history, setHistory] = useState<ProgressHistoryEntry[]>([]);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    if (!isOpen) return;
    let isMounted = true;
    setLoading(true);
    setError(null);

    fetch(`/v1/agents/${encodeURIComponent(agentId)}/progress/history`)
      .then(async (res) => {
        if (!res.ok) {
          throw new Error(`Failed to load progress history: ${res.statusText}`);
        }
        return res.json() as Promise<ProgressHistoryEntry[]>;
      })
      .then((data) => {
        if (isMounted) {
          setHistory(Array.isArray(data) ? data : []);
          setLoading(false);
        }
      })
      .catch((err) => {
        if (isMounted) {
          setError(err instanceof Error ? err.message : String(err));
          setLoading(false);
        }
      });

    return () => {
      isMounted = false;
    };
  }, [agentId]);

  useEffect(() => {
    if (!isOpen) return;

    const handleKeyDown = (e: KeyboardEvent) => {
      if (e.key === 'Escape') {
        onClose();
      }
    };
    window.addEventListener('keydown', handleKeyDown);

    return () => {
      window.removeEventListener('keydown', handleKeyDown);
    };
  }, [isOpen, onClose]);

  if (!isOpen) return null;

  return (
    <div
      className="progress-history-backdrop"
      onClick={onClose}
      role="dialog"
      aria-label="Progress History"
      aria-modal="true"
    >
      <div
        className="progress-history-modal"
        onClick={(e) => e.stopPropagation()}
      >
        <div className="progress-history-header">
          <h2 className="progress-history-title">Progress History</h2>
          <button
            type="button"
            className="icon-btn-round"
            onClick={onClose}
            aria-label="Close"
          >
            <i className="fa-solid fa-xmark" aria-hidden="true" />
          </button>
        </div>

        {loading && <div className="progress-history-state">Loading progress history...</div>}

        {error && <div className="progress-history-state error">{error}</div>}

        {!loading && !error && history.length === 0 && (
          <div className="progress-history-state">No progress history recorded</div>
        )}

        {!loading && !error && history.length > 0 && (
          <div className="progress-history-body">
            <div className="progress-history-chart-container">
              <svg className="progress-history-chart" viewBox="0 0 500 200" preserveAspectRatio="none">
                <line x1="40" y1="20" x2="480" y2="20" stroke="#262626" strokeDasharray="3 3" />
                <line x1="40" y1="100" x2="480" y2="100" stroke="#262626" strokeDasharray="3 3" />
                <line x1="40" y1="180" x2="480" y2="180" stroke="#262626" strokeDasharray="3 3" />
                
                {history.length > 1 && (
                  <polyline
                    fill="none"
                    stroke="#3b82f6"
                    strokeWidth="2"
                    points={history
                      .map((d, i) => {
                        const x = 40 + (i / (history.length - 1)) * 440;
                        const y = 180 - (d.percent / 100) * 160;
                        return `${x},${y}`;
                      })
                      .join(' ')}
                  />
                )}

                {history.map((d, i) => {
                  const x = history.length === 1 ? 260 : 40 + (i / (history.length - 1)) * 440;
                  const y = 180 - (d.percent / 100) * 160;
                  return (
                    <circle
                      key={i}
                      cx={x}
                      cy={y}
                      r="4"
                      fill="#3b82f6"
                      stroke="#ffffff"
                      strokeWidth="1.5"
                    >
                      <title>{`${d.percent}%: ${d.note ?? 'No note'} (${d.timestamp})`}</title>
                    </circle>
                  );
                })}
              </svg>
            </div>

            <div className="progress-history-list">
              {history.map((item, idx) => (
                <div key={idx} className="progress-history-entry">
                  <span className="percent">{item.percent}%</span>
                  <span className="note">{item.note || 'No note'}</span>
                  <span className="time">{new Date(item.timestamp).toLocaleTimeString()}</span>
                </div>
              ))}
            </div>

            <div className="progress-history-footer">
              <a
                href={`/v1/agents/${encodeURIComponent(agentId)}/progress/chart.png`}
                target="_blank"
                rel="noreferrer"
                className="progress-history-link"
              >
                Open Server PNG
              </a>
            </div>
          </div>
        )}
      </div>
    </div>
  );
};


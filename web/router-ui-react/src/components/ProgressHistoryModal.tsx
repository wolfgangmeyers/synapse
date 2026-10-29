import React, { useEffect, useMemo, useState } from 'react';

export interface ProgressHistoryEntry {
  percent: number;
  note: string | null;
  timestamp: string;
}

export type TimeWindow = '1h' | '3h' | '6h' | '12h' | '24h' | '3d' | '7d' | 'all';

export const TIME_WINDOW_DURATIONS: Record<Exclude<TimeWindow, 'all'>, number> = {
  '1h': 60 * 60 * 1000,
  '3h': 3 * 3600 * 1000,
  '6h': 6 * 3600 * 1000,
  '12h': 12 * 3600 * 1000,
  '24h': 24 * 3600 * 1000,
  '3d': 3 * 86400 * 1000,
  '7d': 7 * 86400 * 1000,
};

export const TIME_WINDOW_OPTIONS: { key: TimeWindow; label: string }[] = [
  { key: '1h', label: '1h' },
  { key: '3h', label: '3h' },
  { key: '6h', label: '6h' },
  { key: '12h', label: '12h' },
  { key: '24h', label: '24h' },
  { key: '3d', label: '3d' },
  { key: '7d', label: '7d' },
  { key: 'all', label: 'All' },
];

export function formatRelativeOffset(offsetMs: number): string {
  if (offsetMs <= 0) {
    return 'Latest';
  }
  const totalMinutes = Math.round(offsetMs / 60000);
  if (totalMinutes === 0) {
    return 'Latest';
  }
  const days = Math.floor(totalMinutes / (24 * 60));
  const remMinutes = totalMinutes % (24 * 60);
  const hours = Math.floor(remMinutes / 60);
  const mins = remMinutes % 60;

  if (days > 0) {
    if (hours === 0 && mins === 0) return `-${days}d`;
    if (hours > 0 && mins === 0) return `-${days}d${hours}h`;
    return `-${days}d${hours}h`;
  }
  if (hours > 0) {
    if (mins === 0) return `-${hours}h`;
    return `-${hours}h${mins}m`;
  }
  return `-${mins}m`;
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
  const [timeWindow, setTimeWindow] = useState<TimeWindow>('1h');
  const [expandedIndices, setExpandedIndices] = useState<Record<number, boolean>>({});

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
  }, [agentId, isOpen]);

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

  const toggleExpand = (idx: number) => {
    setExpandedIndices((prev) => ({
      ...prev,
      [idx]: !prev[idx],
    }));
  };

  // Reverse chronological list (newest first)
  const sortedEntries = useMemo(() => {
    return [...history].reverse();
  }, [history]);

  // Proportional timeline calculations
  const { windowStart, windowEnd, windowEntries, priorEntry, xTicks } = useMemo(() => {
    if (history.length === 0) {
      return { windowStart: 0, windowEnd: 0, windowEntries: [], priorEntry: null, xTicks: [] };
    }

    const times = history.map((e) => new Date(e.timestamp).getTime());
    const maxTime = Math.max(...times);

    let minTime: number;
    let windowDuration: number;

    if (timeWindow === 'all') {
      const earliest = Math.min(...times);
      windowDuration = maxTime - earliest;
      if (windowDuration <= 0) {
        windowDuration = 3600 * 1000;
      }
      minTime = maxTime - windowDuration;
    } else {
      windowDuration = TIME_WINDOW_DURATIONS[timeWindow];
      minTime = maxTime - windowDuration;
    }

    // Filter entries that fall inside the window
    const filtered = history.filter((e) => {
      const t = new Date(e.timestamp).getTime();
      return t >= minTime && t <= maxTime;
    });

    // Find the latest entry that occurs strictly before minTime (if any)
    let prior: ProgressHistoryEntry | null = null;
    if (timeWindow !== 'all') {
      const earlier = history.filter((e) => new Date(e.timestamp).getTime() < minTime);
      if (earlier.length > 0) {
        prior = earlier.reduce((prev, curr) =>
          new Date(curr.timestamp).getTime() > new Date(prev.timestamp).getTime() ? curr : prev
        );
      }
    }

    // 5 X-axis relative tick markers across the window (0%, 25%, 50%, 75%, 100%)
    const ticks = [0, 0.25, 0.5, 0.75, 1].map((pct) => {
      const offsetMs = windowDuration * (1 - pct);
      const label = formatRelativeOffset(offsetMs);
      const x = 44 + pct * 436; // 44 to 480
      return { x, label, offsetMs };
    });

    return {
      windowStart: minTime,
      windowEnd: maxTime,
      windowEntries: filtered,
      priorEntry: prior,
      xTicks: ticks,
    };
  }, [history, timeWindow]);

  if (!isOpen) return null;

  const PADDING_LEFT = 44;
  const PADDING_RIGHT = 20;
  const PLOT_WIDTH = 500 - PADDING_LEFT - PADDING_RIGHT; // 436
  const PLOT_TOP = 20;
  const PLOT_BOTTOM = 180;
  const PLOT_HEIGHT = PLOT_BOTTOM - PLOT_TOP; // 160

  const getX = (timestamp: string) => {
    if (windowEnd <= windowStart) return PADDING_LEFT + PLOT_WIDTH;
    const t = new Date(timestamp).getTime();
    const clampedT = Math.max(windowStart, Math.min(windowEnd, t));
    const ratio = (clampedT - windowStart) / (windowEnd - windowStart);
    return PADDING_LEFT + ratio * PLOT_WIDTH;
  };

  const getPolylineX = (timestamp: string) => {
    if (windowEnd <= windowStart) return PADDING_LEFT + PLOT_WIDTH;
    const t = new Date(timestamp).getTime();
    const ratio = (t - windowStart) / (windowEnd - windowStart);
    return PADDING_LEFT + ratio * PLOT_WIDTH;
  };

  const polylineEntries = useMemo(() => {
    if (priorEntry) {
      return [priorEntry, ...windowEntries];
    }
    return windowEntries;
  }, [priorEntry, windowEntries]);

  const getY = (percent: number) => {
    const clampedPct = Math.max(0, Math.min(100, percent));
    return PLOT_BOTTOM - (clampedPct / 100) * PLOT_HEIGHT;
  };

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
          <div className="progress-history-title-row">
            <h2 className="progress-history-title">Progress History</h2>
            <div className="progress-window-selector" role="tablist" aria-label="Time window">
              {TIME_WINDOW_OPTIONS.map((opt) => (
                <button
                  key={opt.key}
                  type="button"
                  role="tab"
                  aria-selected={timeWindow === opt.key}
                  className={`progress-window-pill ${timeWindow === opt.key ? 'active' : ''}`}
                  onClick={() => setTimeWindow(opt.key)}
                >
                  {opt.label}
                </button>
              ))}
            </div>
          </div>
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
              <svg className="progress-history-chart" viewBox="0 0 500 215" preserveAspectRatio="none">
                <defs>
                  <clipPath id="chart-plot-clip">
                    <rect x={PADDING_LEFT} y="0" width={500 - PADDING_LEFT} height="215" />
                  </clipPath>
                </defs>

                {/* Horizontal reference grid lines */}
                <line x1={PADDING_LEFT} y1={getY(100)} x2={PADDING_LEFT + PLOT_WIDTH} y2={getY(100)} stroke="rgba(255, 255, 255, 0.1)" strokeDasharray="3 3" />
                <line x1={PADDING_LEFT} y1={getY(75)} x2={PADDING_LEFT + PLOT_WIDTH} y2={getY(75)} stroke="rgba(255, 255, 255, 0.07)" strokeDasharray="3 3" />
                <line x1={PADDING_LEFT} y1={getY(50)} x2={PADDING_LEFT + PLOT_WIDTH} y2={getY(50)} stroke="rgba(255, 255, 255, 0.1)" strokeDasharray="3 3" />
                <line x1={PADDING_LEFT} y1={getY(25)} x2={PADDING_LEFT + PLOT_WIDTH} y2={getY(25)} stroke="rgba(255, 255, 255, 0.07)" strokeDasharray="3 3" />
                <line x1={PADDING_LEFT} y1={getY(0)} x2={PADDING_LEFT + PLOT_WIDTH} y2={getY(0)} stroke="rgba(255, 255, 255, 0.1)" />

                {/* Y-axis percentage labels */}
                <text x={PADDING_LEFT - 6} y={getY(100) + 4} textAnchor="end" className="progress-chart-axis-label">100%</text>
                <text x={PADDING_LEFT - 6} y={getY(50) + 4} textAnchor="end" className="progress-chart-axis-label">50%</text>
                <text x={PADDING_LEFT - 6} y={getY(0) + 4} textAnchor="end" className="progress-chart-axis-label">0%</text>

                {/* X-axis relative offset markers */}
                {xTicks.map((tick, i) => (
                  <g key={i}>
                    <line x1={tick.x} y1={PLOT_BOTTOM} x2={tick.x} y2={PLOT_BOTTOM + 4} stroke="rgba(255, 255, 255, 0.2)" />
                    <text
                      x={tick.x}
                      y={PLOT_BOTTOM + 16}
                      textAnchor={i === 0 ? 'start' : i === xTicks.length - 1 ? 'end' : 'middle'}
                      className="progress-chart-axis-label"
                    >
                      {tick.label}
                    </text>
                  </g>
                ))}

                {/* Polyline curve connecting proportional points, extending off to the left if prior points exist */}
                {polylineEntries.length > 1 && (
                  <polyline
                    fill="none"
                    stroke="#38bdf8"
                    strokeWidth="2"
                    strokeLinecap="round"
                    strokeLinejoin="round"
                    clipPath="url(#chart-plot-clip)"
                    points={polylineEntries
                      .map((d) => `${getPolylineX(d.timestamp)},${getY(d.percent)}`)
                      .join(' ')}
                  />
                )}

                {/* Subtle point markers */}
                {windowEntries.map((d, i) => {
                  const cx = getX(d.timestamp);
                  const cy = getY(d.percent);
                  return (
                    <circle
                      key={i}
                      cx={cx}
                      cy={cy}
                      r="2.5"
                      fill="#38bdf8"
                      stroke="#0f172a"
                      strokeWidth="1.5"
                      className="progress-history-point"
                    >
                      <title>{`${d.percent}%: ${d.note ?? 'No note'} (${new Date(d.timestamp).toLocaleTimeString()})`}</title>
                    </circle>
                  );
                })}
              </svg>
            </div>

            <div className="progress-history-list">
              {sortedEntries.map((item, idx) => {
                const isExpanded = !!expandedIndices[idx];
                return (
                  <div
                    key={idx}
                    className={`progress-history-entry ${isExpanded ? 'expanded' : ''}`}
                    onClick={() => toggleExpand(idx)}
                    role="button"
                    tabIndex={0}
                    onKeyDown={(e) => {
                      if (e.key === 'Enter' || e.key === ' ') {
                        e.preventDefault();
                        toggleExpand(idx);
                      }
                    }}
                    title="Click to toggle full note"
                  >
                    <div className="progress-history-entry-row">
                      <span className="percent">{item.percent}%</span>
                      <span className={`note ${isExpanded ? 'note-expanded' : 'note-collapsed'}`}>
                        {item.note || 'No note'}
                      </span>
                      <span className="time">{new Date(item.timestamp).toLocaleTimeString()}</span>
                      <i
                        className={`fa-solid fa-chevron-${isExpanded ? 'up' : 'down'} progress-entry-chevron`}
                        aria-hidden="true"
                      />
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
};

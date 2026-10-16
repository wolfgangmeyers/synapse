import { useEffect, useState, type ReactElement } from "react";
import { DialogShell } from "./DialogShell";
import { ErrorBanner } from "./ErrorBanner";
import type {
  RouterHttpClient,
  ContextThresholdConfig,
  ModelContextThreshold,
  ModelsResponse,
  ProviderModel,
} from "../transport/http-client";

export interface ContextThresholdDialogProps {
  open: boolean;
  client: RouterHttpClient;
  catalog?: ModelsResponse | null;
  models?: ProviderModel[];
  onClose: () => void;
}

export function ContextThresholdDialog({
  open,
  client,
  catalog: propCatalog,
  models: propModels,
  onClose,
}: ContextThresholdDialogProps): ReactElement {
  const [warningPercent, setWarningPercent] = useState<number>(10);
  const [resetPercent, setResetPercent] = useState<number>(20);
  const [modelThresholds, setModelThresholds] = useState<Record<string, ModelContextThreshold>>({});

  const [catalog, setCatalog] = useState<ModelsResponse | null>(() => propCatalog ?? null);
  const [selectedModelKey, setSelectedModelKey] = useState<string>("");
  const [overrideWarning, setOverrideWarning] = useState<number>(15);
  const [overrideReset, setOverrideReset] = useState<number>(25);

  const [loading, setLoading] = useState<boolean>(false);
  const [saving, setSaving] = useState<boolean>(false);
  const [error, setError] = useState<string | null>(null);
  const [successMessage, setSuccessMessage] = useState<string | null>(null);

  const effectiveModels: ProviderModel[] = propModels && propModels.length > 0
    ? propModels
    : propCatalog?.models && propCatalog.models.length > 0
      ? propCatalog.models
      : catalog?.models ?? [];

  useEffect(() => {
    if (propCatalog) {
      setCatalog(propCatalog);
    }
  }, [propCatalog]);

  useEffect(() => {
    if (!selectedModelKey && effectiveModels.length > 0) {
      const first = effectiveModels[0];
      if (first) {
        setSelectedModelKey(`${first.provider}/${first.id}`);
      }
    }
  }, [selectedModelKey, effectiveModels]);

  useEffect(() => {
    if (!open) {
      setError(null);
      setSuccessMessage(null);
      return;
    }
    setLoading(true);
    setError(null);
    setSuccessMessage(null);

    const needsModels = (!propModels || propModels.length === 0) && (!propCatalog || !propCatalog.models || propCatalog.models.length === 0);

    Promise.all([
      client.contextThresholds(),
      needsModels ? client.models().catch(() => null) : Promise.resolve(null),
    ])
      .then(([cfg, fetchedModels]) => {
        setWarningPercent(cfg.warning_percent || 10);
        setResetPercent(cfg.reset_percent || 20);
        setModelThresholds(cfg.model_thresholds || {});
        if (fetchedModels) {
          setCatalog(fetchedModels);
          const firstModel = fetchedModels.models?.[0];
          if (firstModel && !selectedModelKey) {
            setSelectedModelKey(`${firstModel.provider}/${firstModel.id}`);
          }
        }
      })
      .catch((err) => {
        const msg = err instanceof Error ? err.message : String(err);
        setError(`Failed to load context threshold settings: ${msg}`);
      })
      .finally(() => {
        setLoading(false);
      });
  }, [open, client, propCatalog, propModels]);

  const handleGlobalWarningChange = (val: number): void => {
    const clamped = Math.max(1, Math.min(99, val));
    setWarningPercent(clamped);
    if (resetPercent <= clamped) {
      setResetPercent(Math.min(100, clamped + 1));
    }
  };

  const handleGlobalResetChange = (val: number): void => {
    const clamped = Math.max(warningPercent + 1, Math.min(100, val));
    setResetPercent(clamped);
  };

  const handleAddOverride = (): void => {
    if (!selectedModelKey.trim()) {
      setError("Please select a model for the override.");
      return;
    }
    if (overrideWarning < 1 || overrideWarning >= overrideReset || overrideReset > 100) {
      setError("Model override warning must be at least 1% and strictly less than reset threshold (up to 100%).");
      return;
    }

    setModelThresholds((prev) => ({
      ...prev,
      [selectedModelKey.trim()]: {
        warning_percent: overrideWarning,
        reset_percent: overrideReset,
      },
    }));
    setError(null);
  };

  const handleRemoveOverride = (key: string): void => {
    setModelThresholds((prev) => {
      const next = { ...prev };
      delete next[key];
      return next;
    });
  };

  const handleSave = async (): Promise<void> => {
    if (warningPercent < 1 || warningPercent >= resetPercent || resetPercent > 100) {
      setError("Global warning threshold must be between 1% and 99% and strictly less than the auto-reset threshold (up to 100%).");
      return;
    }

    for (const [key, ov] of Object.entries(modelThresholds)) {
      if (ov.warning_percent < 1 || ov.warning_percent >= ov.reset_percent || ov.reset_percent > 100) {
        setError(`Override for model "${key}" is invalid: warning must be < reset threshold.`);
        return;
      }
    }

    setSaving(true);
    setError(null);
    setSuccessMessage(null);

    try {
      const saved = await client.saveContextThresholds({
        warning_percent: warningPercent,
        reset_percent: resetPercent,
        model_thresholds: modelThresholds,
      });
      setWarningPercent(saved.warning_percent);
      setResetPercent(saved.reset_percent);
      setModelThresholds(saved.model_thresholds || {});
      setSuccessMessage("Context thresholds saved successfully.");
    } catch (err: unknown) {
      const msg = err instanceof Error ? err.message : String(err);
      setError(`Failed to save settings: ${msg}`);
    } finally {
      setSaving(false);
    }
  };

  return (
    <DialogShell
      open={open}
      title="Context Reset & Warning Thresholds"
      titleId="context-threshold-dialog-title"
      description="Configure context warning reminders and automated reset limits."
      onClose={onClose}
      className="message-dialog context-threshold-dialog"
    >
      <div className="context-threshold-form">
        {error ? <ErrorBanner message={error} onDismiss={() => setError(null)} /> : null}
        {successMessage ? (
          <div className="settings-success-banner" role="status">
            <i className="fa-solid fa-circle-check" aria-hidden="true" style={{ marginRight: 6 }} />
            {successMessage}
          </div>
        ) : null}

        <div className="threshold-explanations">
          <p className="threshold-explanation-item">
            <strong>Warning threshold:</strong> when an agent exceeds this context percentage, a reminder message is sent instructing it to update its scratchpad and clear its context.
          </p>
          <p className="threshold-explanation-item">
            <strong>Fallback Auto-Reset threshold:</strong> if the agent continues to accumulate context past this limit, the context is automatically reset.
          </p>
        </div>

        {loading ? (
          <div className="loading-state">Loading context thresholds…</div>
        ) : (
          <>
            <section className="threshold-section">
              <h4 className="threshold-section-title">Global Thresholds</h4>

              <div className="threshold-slider-group">
                <div className="threshold-label-row">
                  <label htmlFor="global-warning-input">Global Warning Threshold:</label>
                  <span className="threshold-val-display">{warningPercent}%</span>
                </div>
                <div className="threshold-controls-row">
                  <input
                    id="global-warning-slider"
                    type="range"
                    min={1}
                    max={99}
                    value={warningPercent}
                    disabled={saving}
                    onChange={(e) => handleGlobalWarningChange(Number.parseInt(e.target.value, 10))}
                    className="threshold-range-slider"
                  />
                  <input
                    id="global-warning-input"
                    type="number"
                    min={1}
                    max={99}
                    value={warningPercent}
                    disabled={saving}
                    onChange={(e) => handleGlobalWarningChange(Number.parseInt(e.target.value, 10) || 1)}
                    className="threshold-number-input"
                  />
                </div>
              </div>

              <div className="threshold-slider-group">
                <div className="threshold-label-row">
                  <label htmlFor="global-reset-input">Global Fallback Auto-Reset Threshold:</label>
                  <span className="threshold-val-display">{resetPercent}%</span>
                </div>
                <div className="threshold-controls-row">
                  <input
                    id="global-reset-slider"
                    type="range"
                    min={warningPercent + 1}
                    max={100}
                    value={resetPercent}
                    disabled={saving}
                    onChange={(e) => handleGlobalResetChange(Number.parseInt(e.target.value, 10))}
                    className="threshold-range-slider"
                  />
                  <input
                    id="global-reset-input"
                    type="number"
                    min={warningPercent + 1}
                    max={100}
                    value={resetPercent}
                    disabled={saving}
                    onChange={(e) => handleGlobalResetChange(Number.parseInt(e.target.value, 10) || (warningPercent + 1))}
                    className="threshold-number-input"
                  />
                </div>
              </div>
            </section>

            <section className="threshold-section">
              <h4 className="threshold-section-title">Per-Model Overrides</h4>
              <p className="threshold-subtext">
                Override warning and reset thresholds for specific models.
              </p>

              <div className="threshold-override-form">
                <div className="override-inputs-grid">
                  <div className="override-select-field">
                    <label htmlFor="override-model-select">Model:</label>
                    <select
                      id="override-model-select"
                      value={selectedModelKey}
                      onChange={(e) => setSelectedModelKey(e.target.value)}
                      disabled={saving}
                      className="threshold-select"
                    >
                      {effectiveModels.length > 0 ? (
                        effectiveModels.map((m: ProviderModel) => {
                          const key = `${m.provider}/${m.id}`;
                          return (
                            <option key={key} value={key}>
                              {m.name || m.id} ({m.provider})
                            </option>
                          );
                        })
                      ) : (
                        <option value="">No models available</option>
                      )}
                    </select>
                  </div>

                  <div className="override-pct-field">
                    <label htmlFor="override-warning-input">Warn %:</label>
                    <input
                      id="override-warning-input"
                      type="number"
                      min={1}
                      max={99}
                      value={overrideWarning}
                      disabled={saving}
                      onChange={(e) => setOverrideWarning(Math.max(1, Math.min(99, Number.parseInt(e.target.value, 10) || 1)))}
                      className="threshold-number-input small"
                    />
                  </div>

                  <div className="override-pct-field">
                    <label htmlFor="override-reset-input">Reset %:</label>
                    <input
                      id="override-reset-input"
                      type="number"
                      min={overrideWarning + 1}
                      max={100}
                      value={overrideReset}
                      disabled={saving}
                      onChange={(e) => setOverrideReset(Math.max(overrideWarning + 1, Math.min(100, Number.parseInt(e.target.value, 10) || (overrideWarning + 1))))}
                      className="threshold-number-input small"
                    />
                  </div>

                  <button
                    type="button"
                    className="secondary add-override-btn"
                    onClick={handleAddOverride}
                    disabled={saving || !selectedModelKey}
                  >
                    <i className="fa-solid fa-plus" aria-hidden="true" style={{ marginRight: 4 }} />
                    Add Override
                  </button>
                </div>
              </div>

              <div className="threshold-overrides-list">
                {Object.keys(modelThresholds).length === 0 ? (
                  <p className="no-overrides-text">No per-model overrides configured.</p>
                ) : (
                  <table className="threshold-table">
                    <thead>
                      <tr>
                        <th>Model</th>
                        <th>Warning %</th>
                        <th>Auto-Reset %</th>
                        <th style={{ width: 40 }}>Action</th>
                      </tr>
                    </thead>
                    <tbody>
                      {Object.entries(modelThresholds).map(([mKey, ov]) => (
                        <tr key={mKey}>
                          <td className="override-model-name">{mKey}</td>
                          <td>{ov.warning_percent}%</td>
                          <td>{ov.reset_percent}%</td>
                          <td>
                            <button
                              type="button"
                              className="icon-btn-round danger"
                              onClick={() => handleRemoveOverride(mKey)}
                              disabled={saving}
                              aria-label={`Remove override for ${mKey}`}
                              title={`Remove override for ${mKey}`}
                            >
                              <i className="fa-solid fa-trash" aria-hidden="true" />
                            </button>
                          </td>
                        </tr>
                      ))}
                    </tbody>
                  </table>
                )}
              </div>
            </section>
          </>
        )}

        <div className="context-threshold-actions">
          <button type="button" className="secondary" onClick={onClose} disabled={saving}>
            Cancel
          </button>
          <button type="button" onClick={handleSave} disabled={loading || saving}>
            {saving ? "Saving…" : "Save Thresholds"}
          </button>
        </div>
      </div>
    </DialogShell>
  );
}

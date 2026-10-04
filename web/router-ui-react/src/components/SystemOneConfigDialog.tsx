import { useEffect, useState, type ReactElement } from "react";
import { DialogShell } from "./DialogShell";
import type { RouterHttpClient, SystemOneTestResponse } from "../transport/http-client";

export interface SystemOneConfigDialogProps {
  open: boolean;
  client: RouterHttpClient;
  onClose: () => void;
}

const DEFAULT_EXAMPLE_STATE = `POST /api/v1/auth/login HTTP/1.1
Host: production-service.internal
User-Agent: sqlmap/1.8.3#stable
Content-Type: application/json

{"username": "admin' OR '1'='1", "password": "test"}`;

const DEFAULT_EXAMPLE_QUESTIONS = JSON.stringify(
  {
    is_sql_injection: {
      type: "noul",
      instructions: "Determine if this payload contains an SQL injection attack attempt.",
      criteria: {
        true: "Input contains SQL injection syntax or exploitation attempts.",
        false: "Input is benign standard authentication traffic.",
      },
    },
  },
  null,
  2
);

export function SystemOneConfigDialog({ open, client, onClose }: SystemOneConfigDialogProps): ReactElement {
  const [host, setHost] = useState("");
  const [port, setPort] = useState("11434");
  const [enabled, setEnabled] = useState(false);
  const [loading, setLoading] = useState(false);
  const [saving, setSaving] = useState(false);
  const [saveStatus, setSaveStatus] = useState<{ success: boolean; message: string } | null>(null);

  // Sandbox state
  const [sandboxState, setSandboxState] = useState(DEFAULT_EXAMPLE_STATE);
  const [sandboxQuestions, setSandboxQuestions] = useState(DEFAULT_EXAMPLE_QUESTIONS);
  const [testing, setTesting] = useState(false);
  const [testResult, setTestResult] = useState<SystemOneTestResponse | null>(null);
  const [testError, setTestError] = useState<string | null>(null);

  useEffect(() => {
    if (!open) {
      setSaveStatus(null);
      setTestResult(null);
      setTestError(null);
      return;
    }
    setLoading(true);
    client
      .systemOneConfig()
      .then((cfg) => {
        setHost(cfg.host || "");
        setPort(cfg.port > 0 ? String(cfg.port) : "11434");
        setEnabled(cfg.enabled);
      })
      .catch((err) => {
        setSaveStatus({ success: false, message: `Failed to load settings: ${err.message}` });
      })
      .finally(() => setLoading(false));
  }, [open, client]);

  const handleSave = async (): Promise<void> => {
    setSaving(true);
    setSaveStatus(null);
    const parsedPort = Number.parseInt(port, 10);
    if (Number.isNaN(parsedPort) || parsedPort <= 0 || parsedPort > 65535) {
      setSaveStatus({ success: false, message: "Port must be a valid number between 1 and 65535." });
      setSaving(false);
      return;
    }
    try {
      const updated = await client.saveSystemOneConfig({
        host: host.trim(),
        port: parsedPort,
        enabled,
      });
      setHost(updated.host);
      setPort(String(updated.port));
      setEnabled(updated.enabled);
      setSaveStatus({ success: true, message: "System 1 configuration saved." });
    } catch (err: unknown) {
      const msg = err instanceof Error ? err.message : String(err);
      setSaveStatus({ success: false, message: `Failed to save: ${msg}` });
    } finally {
      setSaving(false);
    }
  };

  const handleRunTest = async (): Promise<void> => {
    setTesting(true);
    setTestResult(null);
    setTestError(null);

    let parsedQuestions: Record<string, unknown>;
    try {
      parsedQuestions = JSON.parse(sandboxQuestions);
    } catch {
      setTestError("Questions input must be valid JSON.");
      setTesting(false);
      return;
    }

    try {
      const res = await client.testSystemOne({
        state: sandboxState,
        questions: parsedQuestions,
      });
      if (res.status === "ok") {
        setTestResult(res);
      } else {
        setTestError(res.error ?? "Test execution failed.");
      }
    } catch (err: unknown) {
      const msg = err instanceof Error ? err.message : String(err);
      setTestError(`Execution error: ${msg}`);
    } finally {
      setTesting(false);
    }
  };

  return (
    <DialogShell
      open={open}
      title="System 1 Configuration"
      titleId="system-one-config-dialog-title"
      description="Configure router connection to System 1 / Clef server."
      onClose={onClose}
      closeLabel="Close System 1 configuration"
      className="message-dialog system-one-dialog"
    >
      <div className="system-one-form">
        {loading ? (
          <p className="system-one-loading">Loading configuration…</p>
        ) : (
          <>
            <section className="system-one-section">
              <h3 className="system-one-heading">Server Connection</h3>
              <div className="system-one-grid">
                <label className="system-one-field" htmlFor="system-one-host">
                  <span>Host</span>
                  <input
                    id="system-one-host"
                    type="text"
                    value={host}
                    onChange={(e) => setHost(e.target.value)}
                    placeholder="e.g. 100.115.63.19 or 127.0.0.1"
                    autoComplete="off"
                  />
                </label>
                <label className="system-one-field" htmlFor="system-one-port">
                  <span>Port</span>
                  <input
                    id="system-one-port"
                    type="number"
                    value={port}
                    onChange={(e) => setPort(e.target.value)}
                    placeholder="11434"
                    min="1"
                    max="65535"
                  />
                </label>
              </div>
              <label className="system-one-toggle-row" htmlFor="system-one-enabled">
                <input
                  id="system-one-enabled"
                  type="checkbox"
                  checked={enabled}
                  onChange={(e) => setEnabled(e.target.checked)}
                />
                <span>Enable System 1 decision engine</span>
              </label>

              {saveStatus ? (
                <p
                  className={`system-one-status ${saveStatus.success ? "success" : "error"}`}
                  role={saveStatus.success ? "status" : "alert"}
                >
                  {saveStatus.message}
                </p>
              ) : null}

              <div className="system-one-actions">
                <button
                  type="button"
                  id="system-one-save-btn"
                  className="primary"
                  disabled={saving}
                  onClick={handleSave}
                >
                  {saving ? "Saving…" : "Save Configuration"}
                </button>
              </div>
            </section>

            <section className="system-one-section system-one-sandbox">
              <h3 className="system-one-heading">Decision Sandbox / Test Playground</h3>
              <p className="system-one-subtext">
                Execute a calibrated decision question against the configured System 1 server.
              </p>

              <label className="system-one-field" htmlFor="sandbox-state">
                <span>State (Context or Input)</span>
                <textarea
                  id="sandbox-state"
                  rows={4}
                  value={sandboxState}
                  onChange={(e) => setSandboxState(e.target.value)}
                  placeholder="Enter context or state payload"
                />
              </label>

              <label className="system-one-field" htmlFor="sandbox-questions">
                <span>Questions (JSON schema)</span>
                <textarea
                  id="sandbox-questions"
                  rows={6}
                  value={sandboxQuestions}
                  onChange={(e) => setSandboxQuestions(e.target.value)}
                  placeholder="JSON map of questions"
                  spellCheck={false}
                />
              </label>

              <div className="system-one-actions">
                <button
                  type="button"
                  id="sandbox-run-test-btn"
                  className="primary"
                  disabled={testing || saving}
                  onClick={handleRunTest}
                >
                  {testing ? "Executing Decision…" : "Run Test Decision"}
                </button>
              </div>

              {testError ? (
                <div className="system-one-error-box" role="alert">
                  <strong>Decision Failed</strong>
                  <p>{testError}</p>
                </div>
              ) : null}

              {testResult?.response ? (
                <div className="system-one-result-box" role="status" aria-label="System 1 test result">
                  <div className="system-one-result-header">
                    <span className="system-one-badge success">Decision Success</span>
                    <span className="system-one-model">Model: {testResult.response.model}</span>
                    {testResult.response.usage?.input_tokens !== undefined ? (
                      <span className="system-one-tokens">
                        Tokens: {testResult.response.usage.input_tokens}
                      </span>
                    ) : null}
                  </div>

                  {testResult.response.answers ? (
                    <div className="system-one-answers">
                      {Object.entries(testResult.response.answers).map(([name, ans]) => (
                        <div key={name} className="system-one-answer-card">
                          <div className="system-one-answer-title">
                            <strong>{name}</strong>
                            <span className="system-one-type">
                              {(ans as Record<string, unknown>)?.type as string}
                            </span>
                          </div>
                          <DecisionAnswerDetail answer={ans as Record<string, unknown>} />
                        </div>
                      ))}
                    </div>
                  ) : null}

                  <details className="system-one-raw-details">
                    <summary>View Raw JSON Response</summary>
                    <pre>{JSON.stringify(testResult.response, null, 2)}</pre>
                  </details>
                </div>
              ) : null}
            </section>
          </>
        )}
      </div>
    </DialogShell>
  );
}

function DecisionAnswerDetail({ answer }: { answer: Record<string, unknown> }): ReactElement {
  if (!answer) return <span className="muted">No details</span>;
  const type = answer.type;

  if (type === "noul") {
    const val = typeof answer.noul === "number" ? answer.noul : 0;
    const percent = (val * 100).toFixed(1);
    return (
      <div className="decision-metric">
        <span className="metric-label">Probability (True):</span>
        <span className="metric-value">{percent}%</span>
        <span className="metric-raw">({val.toFixed(4)})</span>
      </div>
    );
  }

  if (type === "choice") {
    const choice = String(answer.choice ?? "");
    const confidence = typeof answer.confidence === "number" ? (answer.confidence * 100).toFixed(1) : null;
    return (
      <div className="decision-metric">
        <span className="metric-label">Selected Choice:</span>
        <span className="metric-value highlight">{choice}</span>
        {confidence !== null ? <span className="metric-conf">(Confidence: {confidence}%)</span> : null}
      </div>
    );
  }

  if (type === "score") {
    const score = typeof answer.score === "number" ? answer.score.toFixed(2) : String(answer.score);
    const confidence = typeof answer.confidence === "number" ? (answer.confidence * 100).toFixed(1) : null;
    return (
      <div className="decision-metric">
        <span className="metric-label">Score:</span>
        <span className="metric-value highlight">{score}</span>
        {confidence !== null ? <span className="metric-conf">(Confidence: {confidence}%)</span> : null}
      </div>
    );
  }

  return <pre>{JSON.stringify(answer, null, 2)}</pre>;
}

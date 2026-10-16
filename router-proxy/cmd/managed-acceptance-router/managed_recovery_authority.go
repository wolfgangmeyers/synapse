package main

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"syscall"
	"time"
)

const managedRecoveryEvidenceLimit = 40

type managedRecoveryAuthority struct {
	db             *sql.DB
	providerURL    string
	trialID        string
	token          string
	completionFile string
	routerPID      int
	client         *http.Client
}

type managedRecoveryFence struct {
	PiSessionID        string `json:"pi_session_id"`
	SessionEpoch       int64  `json:"session_epoch"`
	RuntimeGeneration  string `json:"runtime_generation"`
	ProcessIncarnation string `json:"process_incarnation"`
	ProcessPID         int    `json:"process_pid"`
	ProcessStartMarker string `json:"process_start_marker"`
	ManifestRevision   int64  `json:"manifest_revision"`
	ManifestHash       string `json:"manifest_hash"`
	LifecycleState     string `json:"lifecycle_state"`
	ActivityState      string `json:"activity_state"`
	AcceptingPrompts   int    `json:"accepting_prompts"`
	UpdatedAt          string `json:"updated_at"`
	ProjectionVersion  int64  `json:"projection_version"`
	ProcessGroup       int    `json:"process_group"`
}

type managedRecoveryBarrierRequest struct {
	Event               string  `json:"event"`
	TrialID             string  `json:"trial_id"`
	Token               string  `json:"token"`
	AgentID             string  `json:"agent_id"`
	ObservedMonotonicMS float64 `json:"observed_monotonic_ms"`
}

// managedRecoveryCompletionRequest is the exact completion event emitted by
// the installed Pi fixture. The authority derives every process and provider
// field, then creates the receipt identity and timestamp itself.
type managedRecoveryCompletionRequest struct {
	Event       string `json:"event"`
	TrialID     string `json:"trial_id"`
	Token       string `json:"token"`
	PiSessionID string `json:"pi_session_id"`
}

type managedRecoveryCompletionReceipt struct {
	Event               string
	ReceiptID           string
	TrialID             string
	Token               string
	AgentID             string
	ProviderRequestSeq  int64
	PiSessionID         string
	SessionEpoch        int64
	ProcessPID          int
	ProcessStartMarker  string
	ProcessIncarnation  string
	RuntimeGeneration   string
	ManifestRevision    int64
	ManifestHash        string
	ObservedMonotonicMS float64
	CreatedAt           string
}

// managedRecoveryCompletionEvidence is the validator-facing receipt. The
// authority keeps the rest of the process fence private in SQLite and uses it
// when re-reading the evidence.
type managedRecoveryCompletionEvidence struct {
	AgentID             string  `json:"agent_id"`
	Event               string  `json:"event"`
	ObservedMonotonicMS float64 `json:"observed_monotonic_ms"`
	ProcessIncarnation  string  `json:"process_incarnation"`
	ProviderRequestSeq  int64   `json:"provider_request_seq"`
	ReceiptID           string  `json:"receipt_id"`
	RuntimeGeneration   string  `json:"runtime_generation"`
	Token               string  `json:"token"`
	TrialID             string  `json:"trial_id"`
}

func (r managedRecoveryCompletionReceipt) publicEvidence() managedRecoveryCompletionEvidence {
	return managedRecoveryCompletionEvidence{
		AgentID: r.AgentID, Event: r.Event, ObservedMonotonicMS: r.ObservedMonotonicMS,
		ProcessIncarnation: r.ProcessIncarnation, ProviderRequestSeq: r.ProviderRequestSeq,
		ReceiptID: r.ReceiptID, RuntimeGeneration: r.RuntimeGeneration, Token: r.Token,
		TrialID: r.TrialID,
	}
}

type managedRecoveryProviderState struct {
	Token              string `json:"token"`
	ProviderRequestSeq int64  `json:"provider_request_seq"`
	Active             bool   `json:"active"`
	Completed          bool   `json:"completed"`
}

const managedRecoveryCompletionReceiptSchema = `CREATE TABLE IF NOT EXISTS acceptance_managed_recovery_completion_receipts (
	trial_id TEXT PRIMARY KEY CHECK(length(trial_id) > 0),
	token TEXT NOT NULL CHECK(length(token) > 0),
	provider_request_seq INTEGER NOT NULL CHECK(provider_request_seq > 0),
	agent_id TEXT NOT NULL CHECK(length(agent_id) > 0),
	pi_session_id TEXT NOT NULL CHECK(length(pi_session_id) > 0),
	session_epoch INTEGER NOT NULL CHECK(session_epoch > 0),
	process_pid INTEGER NOT NULL CHECK(process_pid > 1),
	process_start_marker TEXT NOT NULL CHECK(length(process_start_marker) > 0),
	process_incarnation TEXT NOT NULL CHECK(length(process_incarnation) > 0),
	runtime_generation TEXT NOT NULL CHECK(length(runtime_generation) > 0),
	manifest_revision INTEGER NOT NULL CHECK(manifest_revision > 0),
	manifest_hash TEXT NOT NULL CHECK(length(manifest_hash) > 0),
	observed_monotonic_ms REAL NOT NULL CHECK(observed_monotonic_ms > 0),
	created_at TEXT NOT NULL CHECK(length(created_at) > 0),
	receipt_id TEXT NOT NULL UNIQUE CHECK(length(receipt_id) > 0)
) STRICT, WITHOUT ROWID`

func newManagedRecoveryAuthority(database, providerURL, trialID, token, completionFile string, routerPID int) (*managedRecoveryAuthority, error) {
	if trialID == "" {
		return nil, nil
	}
	if token == "" {
		return nil, errors.New("managed recovery token is required")
	}
	db, err := sql.Open("sqlite", database)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	barrierSchema := `CREATE TABLE IF NOT EXISTS acceptance_managed_recovery_evidence (
		trial_id TEXT NOT NULL,
		kind TEXT NOT NULL CHECK(kind IN ('barrier','completion')),
		authority_json TEXT NOT NULL,
		fence_json TEXT NOT NULL,
		provider_request_seq INTEGER NOT NULL CHECK(provider_request_seq > 0),
		monotonic_ns INTEGER NOT NULL CHECK(monotonic_ns > 0),
		created_at TEXT NOT NULL,
		PRIMARY KEY(trial_id, kind)
	)`
	if _, err := db.Exec(barrierSchema); err != nil {
		_ = db.Close()
		return nil, err
	}
	if _, err := db.Exec(managedRecoveryCompletionReceiptSchema); err != nil {
		_ = db.Close()
		return nil, err
	}
	return &managedRecoveryAuthority{db: db, providerURL: providerURL, trialID: trialID, token: token, completionFile: completionFile, routerPID: routerPID, client: &http.Client{Timeout: 2 * time.Second}}, nil
}

func (a *managedRecoveryAuthority) Close() error {
	if a == nil || a.db == nil {
		return nil
	}
	return a.db.Close()
}

func (a *managedRecoveryAuthority) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	if a == nil || r.URL.Path != "/v1/acceptance/managed-recovery" {
		http.NotFound(w, r)
		return
	}
	var value any
	var err error
	switch r.Method {
	case http.MethodPost:
		if r.URL.Query().Get("kind") == "barrier" {
			value, err = a.persistBarrier(r.Context(), r)
		} else {
			err = errors.New("managed recovery evidence kind is invalid")
		}
	case http.MethodGet:
		value, err = a.readEvidence(r.Context(), r.URL.Query().Get("kind"), r.URL.Query().Get("trial_id"))
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	if err != nil {
		w.WriteHeader(http.StatusConflict)
		_ = json.NewEncoder(w).Encode(map[string]any{"error": err.Error()})
		return
	}
	_ = json.NewEncoder(w).Encode(value)
}

func decodeClosedJSON(r *http.Request, target any) error {
	decoder := json.NewDecoder(http.MaxBytesReader(nil, r.Body, 16*1024))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); err == nil {
		return errors.New("managed recovery request has trailing JSON")
	} else if !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) && err.Error() != "EOF" {
		return err
	}
	return nil
}

func (a *managedRecoveryAuthority) providerState(ctx context.Context, token string) (managedRecoveryProviderState, error) {
	base, err := url.Parse(a.providerURL)
	if err != nil {
		return managedRecoveryProviderState{}, err
	}
	base.Path = "/managed-recovery/status"
	base.RawQuery = url.Values{"token": []string{token}}.Encode()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, base.String(), nil)
	if err != nil {
		return managedRecoveryProviderState{}, err
	}
	response, err := a.client.Do(request)
	if err != nil {
		return managedRecoveryProviderState{}, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return managedRecoveryProviderState{}, fmt.Errorf("managed recovery provider returned %d", response.StatusCode)
	}
	var state managedRecoveryProviderState
	decoder := json.NewDecoder(http.MaxBytesReader(nil, response.Body, 16*1024))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&state); err != nil || state.Token != token || state.ProviderRequestSeq < 1 {
		return managedRecoveryProviderState{}, errors.New("managed recovery provider state is invalid")
	}
	return state, nil
}

type recoveryRowQueryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func (a *managedRecoveryAuthority) currentFence(ctx context.Context, queryer recoveryRowQueryer, agentID, sessionID string) (string, managedRecoveryFence, error) {
	query := `SELECT agent_id,pi_session_id,session_epoch,runtime_generation,process_incarnation,process_pid,process_start_marker,manifest_revision,manifest_hash,lifecycle_state,activity_state,accepting_prompts,updated_at,(SELECT projection_version FROM managed_settings WHERE singleton_id=1) FROM managed_agents WHERE `
	argument := agentID
	if sessionID != "" {
		query += "pi_session_id=?"
		argument = sessionID
	} else {
		query += "agent_id=?"
	}
	var id string
	var fence managedRecoveryFence
	err := queryer.QueryRowContext(ctx, query, argument).Scan(
		&id, &fence.PiSessionID, &fence.SessionEpoch, &fence.RuntimeGeneration,
		&fence.ProcessIncarnation, &fence.ProcessPID, &fence.ProcessStartMarker,
		&fence.ManifestRevision, &fence.ManifestHash, &fence.LifecycleState,
		&fence.ActivityState, &fence.AcceptingPrompts, &fence.UpdatedAt,
		&fence.ProjectionVersion,
	)
	if err != nil {
		return "", managedRecoveryFence{}, err
	}
	group, err := syscall.Getpgid(fence.ProcessPID)
	if err != nil {
		return "", managedRecoveryFence{}, errors.New("managed recovery process group is unavailable")
	}
	fence.ProcessGroup = group
	return id, fence, nil
}

func monotonicNanoseconds() int64 { return time.Since(time.Unix(0, 0)).Nanoseconds() }

func authorityID(prefix string) (string, error) {
	value := make([]byte, 16)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	return prefix + hex.EncodeToString(value), nil
}

func (a *managedRecoveryAuthority) persistBarrier(ctx context.Context, r *http.Request) (map[string]any, error) {
	var request managedRecoveryBarrierRequest
	if err := decodeClosedJSON(r, &request); err != nil {
		return nil, err
	}
	if request.Event != "managed_recovery_in_progress" || request.TrialID != a.trialID || request.Token != a.token || request.AgentID == "" || request.ObservedMonotonicMS <= 0 {
		return nil, errors.New("managed recovery barrier request is invalid")
	}
	provider, err := a.providerState(ctx, request.Token)
	if err != nil || !provider.Active || provider.Completed {
		return nil, errors.New("managed recovery provider request is not active")
	}
	_, fence, err := a.currentFence(ctx, a.db, request.AgentID, "")
	if err != nil {
		return nil, err
	}
	id, err := authorityID("barrier_")
	if err != nil {
		return nil, err
	}
	authority := map[string]any{
		"event": request.Event, "acknowledgement_id": id, "trial_id": request.TrialID, "token": request.Token,
		"agent_id": request.AgentID, "provider_request_seq": provider.ProviderRequestSeq, "acknowledged": true,
		"request_active": true, "router_pid": a.routerPID, "observed_monotonic_ms": request.ObservedMonotonicMS, "fence": fence,
	}
	return a.insertBarrier(ctx, request.TrialID, authority, fence, provider.ProviderRequestSeq, monotonicNanoseconds())
}

func completionFenceMatches(receipt managedRecoveryCompletionReceipt, fence managedRecoveryFence) bool {
	return receipt.PiSessionID == fence.PiSessionID &&
		receipt.SessionEpoch == fence.SessionEpoch &&
		receipt.ProcessPID == fence.ProcessPID &&
		receipt.ProcessStartMarker == fence.ProcessStartMarker &&
		receipt.ProcessIncarnation == fence.ProcessIncarnation &&
		receipt.RuntimeGeneration == fence.RuntimeGeneration &&
		receipt.ManifestRevision == fence.ManifestRevision &&
		receipt.ManifestHash == fence.ManifestHash
}

func currentCompletionReceiptState(fence managedRecoveryFence) bool {
	return fence.LifecycleState == "running" &&
		(fence.ActivityState == "active" || fence.ActivityState == "idle") &&
		fence.AcceptingPrompts == 1
}

func (a *managedRecoveryAuthority) persistCompletion(ctx context.Context, request managedRecoveryCompletionRequest) (managedRecoveryCompletionReceipt, error) {
	if request.Event != "managed_recovery_complete" || request.TrialID != a.trialID || request.Token == "" || request.PiSessionID == "" {
		return managedRecoveryCompletionReceipt{}, errors.New("managed recovery completion request is invalid")
	}
	provider, err := a.providerState(ctx, request.Token)
	if err != nil || provider.Active || !provider.Completed {
		return managedRecoveryCompletionReceipt{}, errors.New("managed recovery provider completion is invalid")
	}

	tx, err := a.db.BeginTx(ctx, nil)
	if err != nil {
		return managedRecoveryCompletionReceipt{}, err
	}
	defer tx.Rollback()

	agentID, fence, err := a.currentFence(ctx, tx, "", request.PiSessionID)
	if err != nil || fence.PiSessionID != request.PiSessionID {
		return managedRecoveryCompletionReceipt{}, errors.New("managed recovery completion fence is stale")
	}
	if !currentCompletionReceiptState(fence) {
		return managedRecoveryCompletionReceipt{}, errors.New("managed recovery completion state is not running, accepting prompts, and active or idle")
	}
	barrier, barrierMono, err := storedBarrier(ctx, tx, request.TrialID)
	if err != nil {
		return managedRecoveryCompletionReceipt{}, errors.New("managed recovery barrier is unavailable")
	}
	barrierAgentID, _ := barrier["agent_id"].(string)
	barrierToken, _ := barrier["token"].(string)
	if barrierAgentID != agentID || barrierToken != request.Token {
		return managedRecoveryCompletionReceipt{}, errors.New("managed recovery completion barrier is stale")
	}
	baseline, ok := barrier["observed_monotonic_ms"].(float64)
	if !ok {
		return managedRecoveryCompletionReceipt{}, errors.New("managed recovery barrier clock is invalid")
	}
	nowMono := monotonicNanoseconds()
	observed := baseline + float64(nowMono-barrierMono)/float64(time.Millisecond)
	if existing, readErr := readCompletionReceipt(ctx, tx, request.TrialID); readErr == nil {
		if existing.Token != request.Token || existing.AgentID != agentID || existing.ProviderRequestSeq != provider.ProviderRequestSeq || !completionFenceMatches(existing, fence) {
			return managedRecoveryCompletionReceipt{}, errors.New("managed recovery completion receipt already exists with different evidence")
		}
		if err := tx.Commit(); err != nil {
			return managedRecoveryCompletionReceipt{}, err
		}
		return existing, nil
	} else if !errors.Is(readErr, sql.ErrNoRows) {
		return managedRecoveryCompletionReceipt{}, readErr
	}

	id, err := authorityID("receipt_")
	if err != nil {
		return managedRecoveryCompletionReceipt{}, err
	}
	receipt := managedRecoveryCompletionReceipt{
		Event: "managed_recovery_completion_persisted", ReceiptID: id,
		TrialID: request.TrialID, Token: request.Token, AgentID: agentID,
		ProviderRequestSeq: provider.ProviderRequestSeq, PiSessionID: fence.PiSessionID,
		SessionEpoch: fence.SessionEpoch, ProcessPID: fence.ProcessPID,
		ProcessStartMarker: fence.ProcessStartMarker, ProcessIncarnation: fence.ProcessIncarnation,
		RuntimeGeneration: fence.RuntimeGeneration, ManifestRevision: fence.ManifestRevision,
		ManifestHash: fence.ManifestHash, ObservedMonotonicMS: observed,
		CreatedAt: time.Now().UTC().Format(time.RFC3339Nano),
	}
	if err := insertCompletionReceipt(ctx, tx, receipt); err != nil {
		return managedRecoveryCompletionReceipt{}, err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM acceptance_managed_recovery_completion_receipts WHERE trial_id IN (SELECT trial_id FROM acceptance_managed_recovery_completion_receipts ORDER BY created_at DESC, trial_id DESC LIMIT -1 OFFSET ?)`, managedRecoveryEvidenceLimit); err != nil {
		return managedRecoveryCompletionReceipt{}, err
	}
	persisted, err := readCompletionReceipt(ctx, tx, request.TrialID)
	if err != nil {
		return managedRecoveryCompletionReceipt{}, err
	}
	if err := tx.Commit(); err != nil {
		return managedRecoveryCompletionReceipt{}, err
	}
	return persisted, nil
}

func insertCompletionReceipt(ctx context.Context, tx *sql.Tx, receipt managedRecoveryCompletionReceipt) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO acceptance_managed_recovery_completion_receipts(
		trial_id,token,provider_request_seq,agent_id,pi_session_id,session_epoch,
		process_pid,process_start_marker,process_incarnation,runtime_generation,
		manifest_revision,manifest_hash,observed_monotonic_ms,created_at,receipt_id
	) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		receipt.TrialID, receipt.Token, receipt.ProviderRequestSeq, receipt.AgentID,
		receipt.PiSessionID, receipt.SessionEpoch, receipt.ProcessPID,
		receipt.ProcessStartMarker, receipt.ProcessIncarnation, receipt.RuntimeGeneration,
		receipt.ManifestRevision, receipt.ManifestHash, receipt.ObservedMonotonicMS,
		receipt.CreatedAt, receipt.ReceiptID,
	)
	return err
}

func readCompletionReceipt(ctx context.Context, queryer recoveryRowQueryer, trialID string) (managedRecoveryCompletionReceipt, error) {
	receipt := managedRecoveryCompletionReceipt{Event: "managed_recovery_completion_persisted"}
	err := queryer.QueryRowContext(ctx, `SELECT
		receipt_id,trial_id,token,agent_id,provider_request_seq,pi_session_id,
		session_epoch,process_pid,process_start_marker,process_incarnation,
		runtime_generation,manifest_revision,manifest_hash,observed_monotonic_ms,created_at
		FROM acceptance_managed_recovery_completion_receipts WHERE trial_id=?`, trialID).Scan(
		&receipt.ReceiptID, &receipt.TrialID, &receipt.Token, &receipt.AgentID,
		&receipt.ProviderRequestSeq, &receipt.PiSessionID, &receipt.SessionEpoch,
		&receipt.ProcessPID, &receipt.ProcessStartMarker, &receipt.ProcessIncarnation,
		&receipt.RuntimeGeneration, &receipt.ManifestRevision, &receipt.ManifestHash,
		&receipt.ObservedMonotonicMS, &receipt.CreatedAt,
	)
	return receipt, err
}

func (a *managedRecoveryAuthority) insertBarrier(ctx context.Context, trialID string, authority any, fence managedRecoveryFence, sequence, monotonicNS int64) (map[string]any, error) {
	authorityJSON, err := json.Marshal(authority)
	if err != nil {
		return nil, err
	}
	fenceJSON, err := json.Marshal(fence)
	if err != nil {
		return nil, err
	}
	_, err = a.db.ExecContext(ctx, `INSERT INTO acceptance_managed_recovery_evidence(trial_id,kind,authority_json,fence_json,provider_request_seq,monotonic_ns,created_at) VALUES(?,'barrier',?,?,?,?,?)`, trialID, string(authorityJSON), string(fenceJSON), sequence, monotonicNS, time.Now().UTC().Format(time.RFC3339Nano))
	if err != nil {
		stored, _, readErr := storedBarrier(ctx, a.db, trialID)
		if readErr == nil && string(authorityJSON) == mustJSON(stored) {
			return stored, nil
		}
		return nil, errors.New("managed recovery authority already exists with different evidence")
	}
	_, _ = a.db.ExecContext(ctx, `DELETE FROM acceptance_managed_recovery_evidence WHERE rowid IN (SELECT rowid FROM acceptance_managed_recovery_evidence ORDER BY created_at DESC, rowid DESC LIMIT -1 OFFSET ?)`, managedRecoveryEvidenceLimit)
	var result map[string]any
	if err := json.Unmarshal(authorityJSON, &result); err != nil {
		return nil, err
	}
	return result, nil
}

func mustJSON(value any) string {
	encoded, _ := json.Marshal(value)
	return string(encoded)
}

func storedBarrier(ctx context.Context, queryer recoveryRowQueryer, trialID string) (map[string]any, int64, error) {
	var encoded string
	var monotonicNS int64
	if err := queryer.QueryRowContext(ctx, `SELECT authority_json,monotonic_ns FROM acceptance_managed_recovery_evidence WHERE trial_id=? AND kind='barrier'`, trialID).Scan(&encoded, &monotonicNS); err != nil {
		return nil, 0, err
	}
	var result map[string]any
	if err := json.Unmarshal([]byte(encoded), &result); err != nil {
		return nil, 0, err
	}
	return result, monotonicNS, nil
}

func readManagedRecoveryCompletionFile(path string) (managedRecoveryCompletionRequest, error) {
	const maximumBytes = 16 * 1024
	info, err := os.Lstat(path)
	if err != nil {
		return managedRecoveryCompletionRequest{}, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || info.Size() < 1 || info.Size() > maximumBytes || info.Mode().Perm()&0077 != 0 {
		return managedRecoveryCompletionRequest{}, errors.New("managed recovery completion signal file is invalid")
	}
	file, err := os.Open(path)
	if err != nil {
		return managedRecoveryCompletionRequest{}, err
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !opened.Mode().IsRegular() || !os.SameFile(info, opened) {
		return managedRecoveryCompletionRequest{}, errors.New("managed recovery completion signal file is invalid")
	}
	encoded, err := io.ReadAll(io.LimitReader(file, maximumBytes+1))
	if err != nil || len(encoded) > maximumBytes {
		return managedRecoveryCompletionRequest{}, errors.New("managed recovery completion signal file is invalid")
	}
	var request managedRecoveryCompletionRequest
	decoder := json.NewDecoder(strings.NewReader(string(encoded)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		return managedRecoveryCompletionRequest{}, errors.New("managed recovery completion signal file is invalid")
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return managedRecoveryCompletionRequest{}, errors.New("managed recovery completion signal file is invalid")
	}
	return request, nil
}

func (a *managedRecoveryAuthority) readEvidence(ctx context.Context, kind, trialID string) (any, error) {
	if trialID != a.trialID || (kind != "barrier" && kind != "completion") {
		return nil, errors.New("managed recovery evidence query is invalid")
	}
	if kind == "completion" {
		receipt, err := readCompletionReceipt(ctx, a.db, trialID)
		if errors.Is(err, sql.ErrNoRows) {
			request, readErr := readManagedRecoveryCompletionFile(a.completionFile)
			if readErr != nil {
				return nil, errors.New("managed recovery completion signal is unavailable or invalid")
			}
			receipt, err = a.persistCompletion(ctx, request)
		}
		if err != nil {
			return nil, err
		}
		provider, err := a.providerState(ctx, receipt.Token)
		if err != nil || provider.ProviderRequestSeq != receipt.ProviderRequestSeq || provider.Active || !provider.Completed {
			return nil, errors.New("managed recovery provider evidence is no longer current")
		}
		agentID, fence, err := a.currentFence(ctx, a.db, "", receipt.PiSessionID)
		if err != nil || agentID != receipt.AgentID || !completionFenceMatches(receipt, fence) || !currentCompletionReceiptState(fence) {
			return nil, errors.New("managed recovery completion fence is stale")
		}
		return receipt.publicEvidence(), nil
	}

	value, _, err := storedBarrier(ctx, a.db, trialID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, errors.New("managed recovery evidence is not persisted")
		}
		return nil, err
	}
	sequence, _ := value["provider_request_seq"].(float64)
	token, _ := value["token"].(string)
	provider, err := a.providerState(ctx, token)
	if err != nil || provider.ProviderRequestSeq != int64(sequence) || !provider.Active || provider.Completed {
		return nil, errors.New("managed recovery provider evidence is no longer current")
	}
	agentID, _ := value["agent_id"].(string)
	_, fence, err := a.currentFence(ctx, a.db, agentID, "")
	if err != nil {
		return nil, err
	}
	persistedFenceJSON, err := json.Marshal(value["fence"])
	if err != nil {
		return nil, errors.New("managed recovery barrier fence is invalid")
	}
	var persistedFence managedRecoveryFence
	if err := json.Unmarshal(persistedFenceJSON, &persistedFence); err != nil {
		return nil, errors.New("managed recovery barrier fence is invalid")
	}
	routerPID, routerPIDOK := value["router_pid"].(float64)
	if persistedFence != fence || !routerPIDOK || int(routerPID) != a.routerPID {
		return nil, errors.New("managed recovery barrier fence is stale")
	}
	return value, nil
}

func acceptanceTrialID(environment string) string {
	if strings.TrimSpace(environment) == "" {
		return ""
	}
	return environment
}

func parseProviderSequence(value string) (int64, error) { return strconv.ParseInt(value, 10, 64) }

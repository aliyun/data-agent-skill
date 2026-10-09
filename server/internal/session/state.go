package session

import (
	"encoding/json"
	"log"
	"maps"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Status represents the current lifecycle status of a session.
type Status string

const (
	StatusRunning      Status = "running"
	StatusWaitingInput Status = "waiting_input"
	StatusCompleted    Status = "completed"
	StatusError        Status = "error"
	StatusCanceled     Status = "canceled"
)

// Confirmation records a single auto-confirmation or user confirmation event.
type Confirmation struct {
	Type          string    `json:"type"`
	AutoConfirmed bool      `json:"auto_confirmed"`
	At            time.Time `json:"at"`
}

type SendStatus string

const (
	SendPending      SendStatus = "pending"
	SendSending      SendStatus = "sending"
	SendAcknowledged SendStatus = "acknowledged"
	SendFailed       SendStatus = "failed"
	SendUnknown      SendStatus = "unknown"
)

type ConfirmationRequest struct {
	Key     string     `json:"key"`
	Kind    string     `json:"kind"`
	Content string     `json:"content"`
	Status  SendStatus `json:"status"`
	Stable  bool       `json:"stable"`
	Ready   bool       `json:"ready,omitempty"`
}

// State holds the mutable, thread-safe status of a single Data Agent session.
// All field access must go through the getter/setter methods which acquire the
// internal RWMutex.
type State struct {
	mu        sync.RWMutex
	persistMu sync.Mutex

	// changed is a channel that is closed and replaced each time meaningful
	// state changes. Callers waiting for progress hold a reference to the
	// current channel and block on it; notify() closes it to wake them up.
	notifyMu sync.Mutex
	changed  chan struct{}

	SessionID            string                         `json:"session_id"`
	AgentID              string                         `json:"agent_id"`
	Status               Status                         `json:"status"`
	Mode                 string                         `json:"mode"`
	AutoConfirm          bool                           `json:"auto_confirm"`
	CurrentStep          int                            `json:"current_step"`
	TotalSteps           int                            `json:"total_steps"`
	StepName             string                         `json:"step_name"`
	WaitingFor           string                         `json:"waiting_for,omitempty"`
	WaitingDetail        string                         `json:"waiting_detail,omitempty"`
	Checkpoint           int                            `json:"checkpoint"`
	Requests             map[string]ConfirmationRequest `json:"requests,omitempty"`
	PendingAsk           string                         `json:"pending_ask,omitempty"`
	AppliedEvents        map[string]bool                `json:"applied_events,omitempty"`
	TurnKey              string                         `json:"turn_key,omitempty"`
	PendingLLM           string                         `json:"pending_llm,omitempty"`
	PendingToolOutput    string                         `json:"pending_tool_output,omitempty"`
	HasTurnConclusion    bool                           `json:"has_turn_conclusion,omitempty"`
	AwaitingTurn         bool                           `json:"awaiting_turn,omitempty"`
	MessageStatus        SendStatus                     `json:"message_status,omitempty"`
	SendGeneration       uint64                         `json:"send_generation,omitempty"`
	UpdatedAt            time.Time                      `json:"updated_at"`
	CreatedAt            time.Time                      `json:"created_at"`
	Confirmations        []Confirmation                 `json:"confirmations"`
	Conclusions          []string                       `json:"conclusions,omitempty"`
	Artifacts            []string                       `json:"artifacts,omitempty"`
	NextImageSeq         int                            `json:"next_image_seq,omitempty"`
	ErrorMessage         string                         `json:"error_message,omitempty"`
	RecommendedQuestions []string                       `json:"recommended_questions,omitempty"`
	WorkspaceID          string                         `json:"workspace_id,omitempty"`
	PollSeq              int                            `json:"-"` // not persisted; auto-incremented per status call
	pollCheckpoint       int                            // checkpoint seen at the last poll; progress resets PollSeq
	conclusionIdx        map[string]int                 // dedup key → Conclusions index (not persisted)
}

// ---------- Change notification ----------

// notify closes the current changed channel and installs a fresh one.
// Callers blocking in WaitForChange or WaitForResult wake up immediately.
// Must NOT be called with s.mu held (uses its own notifyMu).
func (s *State) notify() {
	s.notifyMu.Lock()
	old := s.changed
	s.changed = make(chan struct{})
	s.notifyMu.Unlock()
	if old != nil {
		close(old)
	}
}

// Changed returns the current notification channel. It is closed by notify()
// the next time state changes. Callers should snapshot the channel reference
// before reading state, then select on it after finding no progress.
func (s *State) Changed() <-chan struct{} {
	s.notifyMu.Lock()
	if s.changed == nil {
		s.changed = make(chan struct{})
	}
	ch := s.changed
	s.notifyMu.Unlock()
	return ch
}

// ---------- Thread-safe getters ----------

// IncrPollSeq increments and returns the poll counter behind the anti-loop
// warning. The counter resets whenever the checkpoint has advanced since the
// previous poll: polling that observes progress is legitimate — only repeated
// polls with no progress should escalate to a warning.
func (s *State) IncrPollSeq() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.Checkpoint != s.pollCheckpoint {
		s.pollCheckpoint = s.Checkpoint
		s.PollSeq = 0
	}
	s.PollSeq++
	return s.PollSeq
}

func (s *State) GetWorkspaceID() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.WorkspaceID
}

func (s *State) GetSessionID() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.SessionID
}

func (s *State) GetAgentID() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.AgentID
}

func (s *State) GetStatus() Status {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.Status
}

func (s *State) GetMode() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.Mode
}

func (s *State) GetAutoConfirm() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.AutoConfirm
}

func (s *State) GetCurrentStep() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.CurrentStep
}

func (s *State) GetTotalSteps() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.TotalSteps
}

func (s *State) GetStepName() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.StepName
}

func (s *State) GetWaitingFor() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.WaitingFor
}

func (s *State) GetWaitingDetail() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.WaitingDetail
}

func (s *State) GetCheckpoint() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.Checkpoint
}

func (s *State) RegisterAsk(request ConfirmationRequest) (ConfirmationRequest, bool) {
	s.mu.Lock()
	if existing, ok := s.Requests[request.Key]; ok {
		s.mu.Unlock()
		return existing, false
	}
	if s.Requests == nil {
		s.Requests = make(map[string]ConfirmationRequest)
	}
	request.Status = SendPending
	s.Requests[request.Key] = request
	s.PendingAsk = request.Key
	s.Status = StatusWaitingInput
	s.WaitingFor = request.Kind
	s.WaitingDetail = request.Content
	s.UpdatedAt = time.Now()
	s.mu.Unlock()
	s.notify()
	return request, true
}

func (s *State) MarkReportReady(key string) {
	s.mu.Lock()
	request := s.Requests[key]
	request.Ready = true
	s.Requests[key] = request
	s.UpdatedAt = time.Now()
	s.mu.Unlock()
	s.notify()
}

func (s *State) SetSendStatus(key string, status SendStatus, auto bool) {
	s.mu.Lock()
	if key != "" {
		request := s.Requests[key]
		request.Status = status
		s.Requests[key] = request
		if status == SendSending && request.Kind == "ask_report_render" {
			s.AwaitingTurn = true
		}
		if status == SendAcknowledged {
			s.Confirmations = append(s.Confirmations, Confirmation{Type: request.Kind, AutoConfirmed: auto, At: time.Now()})
			if s.PendingAsk == key {
				s.PendingAsk = ""
				s.WaitingFor, s.WaitingDetail = "", ""
				s.Status = StatusRunning
			}
		} else if s.PendingAsk == key {
			s.Status = StatusWaitingInput
			s.WaitingFor = request.Kind
			s.WaitingDetail = request.Content
			if status == SendUnknown {
				s.WaitingDetail += "\nMessage delivery is unknown; do not resend until remote state is verified."
			}
		}
	} else {
		s.MessageStatus = status
		if status == SendSending {
			s.AwaitingTurn = s.Status == StatusCompleted || s.Status == StatusError || s.Status == StatusCanceled
		}
		if status == SendAcknowledged {
			s.Status = StatusRunning
			s.WaitingFor, s.WaitingDetail, s.ErrorMessage = "", "", ""
		}
		if status == SendUnknown {
			s.Status = StatusWaitingInput
			s.WaitingFor = "message_delivery"
			s.WaitingDetail = "Message delivery is unknown; do not resend until remote state is verified."
		}
	}
	if status == SendSending {
		s.SendGeneration++
	}
	s.UpdatedAt = time.Now()
	s.mu.Unlock()
	s.notify()
}

func (s *State) EventApplied(key string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.AppliedEvents[key]
}

func (s *State) MarkEventApplied(key string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.AppliedEvents == nil {
		s.AppliedEvents = make(map[string]bool)
	}
	s.AppliedEvents[key] = true
}

func (s *State) AppendLLMFallback(text string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.PendingLLM += text
}

func (s *State) AppendToolOutput(text string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.PendingToolOutput != "" {
		s.PendingToolOutput += "\n\n"
	}
	s.PendingToolOutput += text
}

func (s *State) StartTurn(key string) {
	s.mu.Lock()
	s.TurnKey = key
	s.PendingLLM = ""
	s.PendingToolOutput = ""
	s.HasTurnConclusion = false
	s.AwaitingTurn = false
	s.PendingAsk = ""
	s.WaitingFor, s.WaitingDetail = "", ""
	s.Status = StatusRunning
	s.MessageStatus = ""
	s.UpdatedAt = time.Now()
	s.mu.Unlock()
	s.notify()
}

func (s *State) GetUpdatedAt() time.Time {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.UpdatedAt
}

func (s *State) GetCreatedAt() time.Time {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.CreatedAt
}

func (s *State) GetConfirmations() []Confirmation {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]Confirmation, len(s.Confirmations))
	copy(out, s.Confirmations)
	return out
}

func (s *State) GetConclusions() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]string, len(s.Conclusions))
	copy(out, s.Conclusions)
	return out
}

func (s *State) GetArtifacts() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]string, len(s.Artifacts))
	copy(out, s.Artifacts)
	return out
}

func (s *State) GetErrorMessage() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.ErrorMessage
}

// ---------- Thread-safe setters ----------

func (s *State) SetStatus(status Status) {
	s.mu.Lock()
	s.Status = status
	s.UpdatedAt = time.Now()
	s.mu.Unlock()
	s.notify()
}

func (s *State) SetCheckpoint(cp int) {
	s.mu.Lock()
	s.Checkpoint = cp
	s.UpdatedAt = time.Now()
	s.mu.Unlock()
	s.notify()
}

func (s *State) SetStepProgress(current, total int, name string) {
	s.mu.Lock()
	s.CurrentStep = current
	s.TotalSteps = total
	s.StepName = name
	s.UpdatedAt = time.Now()
	s.mu.Unlock()
	s.notify()
}

func (s *State) SetWaiting(waitingFor, detail string) {
	s.mu.Lock()
	s.Status = StatusWaitingInput
	s.WaitingFor = waitingFor
	s.WaitingDetail = detail
	s.UpdatedAt = time.Now()
	s.mu.Unlock()
	s.notify()
}

func (s *State) ClearWaiting() {
	s.mu.Lock()
	s.WaitingFor = ""
	s.WaitingDetail = ""
	s.Status = StatusRunning
	s.UpdatedAt = time.Now()
	s.mu.Unlock()
	s.notify()
}

func (s *State) SetError(msg string) {
	s.mu.Lock()
	s.Status = StatusError
	s.PendingAsk = ""
	s.WaitingFor, s.WaitingDetail = "", ""
	s.MessageStatus = ""
	s.ErrorMessage = msg
	s.UpdatedAt = time.Now()
	s.mu.Unlock()
	s.notify()
}

func (s *State) SetCompleted() {
	s.mu.Lock()
	s.Status = StatusCompleted
	s.PendingAsk = ""
	s.WaitingFor, s.WaitingDetail = "", ""
	s.MessageStatus = ""
	s.UpdatedAt = time.Now()
	s.mu.Unlock()
	s.notify()
}

func (s *State) SetCanceled() {
	s.mu.Lock()
	s.Status = StatusCanceled
	s.PendingAsk = ""
	s.WaitingFor, s.WaitingDetail = "", ""
	s.MessageStatus = ""
	s.UpdatedAt = time.Now()
	s.mu.Unlock()
	s.notify()
}

func (s *State) AddConfirmation(confirmType string, auto bool) {
	s.mu.Lock()
	s.Confirmations = append(s.Confirmations, Confirmation{
		Type:          confirmType,
		AutoConfirmed: auto,
		At:            time.Now(),
	})
	s.UpdatedAt = time.Now()
	s.mu.Unlock()
}

func (s *State) AddConclusion(text string) {
	s.mu.Lock()
	s.HasTurnConclusion = true
	s.Conclusions = append(s.Conclusions, text)
	s.UpdatedAt = time.Now()
	s.mu.Unlock()
	s.notify()
}

// UpsertConclusion appends a conclusion or, when the same key was seen
// before, replaces the earlier copy in place. The backend re-emits mission
// objective conclusions as a run progresses; replacing keeps the latest text
// without piling up near-duplicates.
func (s *State) UpsertConclusion(key, text string) {
	if key == "" {
		s.AddConclusion(text)
		return
	}
	s.mu.Lock()
	s.HasTurnConclusion = true
	if s.conclusionIdx == nil {
		s.conclusionIdx = make(map[string]int)
	}
	if i, ok := s.conclusionIdx[key]; ok && i < len(s.Conclusions) {
		s.Conclusions[i] = text
	} else {
		s.conclusionIdx[key] = len(s.Conclusions)
		s.Conclusions = append(s.Conclusions, text)
	}
	s.UpdatedAt = time.Now()
	s.mu.Unlock()
	s.notify()
}

func (s *State) AddArtifact(artifact string) {
	s.mu.Lock()
	s.Artifacts = append(s.Artifacts, artifact)
	s.UpdatedAt = time.Now()
	s.mu.Unlock()
	s.notify()
}

func (s *State) SetRecommendedQuestions(questions []string) {
	s.mu.Lock()
	s.RecommendedQuestions = questions
	s.UpdatedAt = time.Now()
	s.mu.Unlock()
}

// AllocImageSeq atomically allocates a contiguous block of image sequence numbers.
// It returns the starting sequence number. The caller gets seq, seq+1, ..., seq+count-1.
func (s *State) AllocImageSeq(count int) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	start := s.NextImageSeq
	s.NextImageSeq += count
	s.UpdatedAt = time.Now()
	return start
}

// ---------- Snapshot ----------

// StateSnapshot is a plain data struct (no mutex) used for serialization and
// returning state to callers. It mirrors all public fields of State.
type StateSnapshot struct {
	SessionID            string                         `json:"session_id"`
	AgentID              string                         `json:"agent_id"`
	Status               Status                         `json:"status"`
	Mode                 string                         `json:"mode"`
	AutoConfirm          bool                           `json:"auto_confirm"`
	CurrentStep          int                            `json:"current_step"`
	TotalSteps           int                            `json:"total_steps"`
	StepName             string                         `json:"step_name"`
	WaitingFor           string                         `json:"waiting_for,omitempty"`
	WaitingDetail        string                         `json:"waiting_detail,omitempty"`
	Checkpoint           int                            `json:"checkpoint"`
	Requests             map[string]ConfirmationRequest `json:"requests,omitempty"`
	PendingAsk           string                         `json:"pending_ask,omitempty"`
	AppliedEvents        map[string]bool                `json:"applied_events,omitempty"`
	TurnKey              string                         `json:"turn_key,omitempty"`
	PendingLLM           string                         `json:"pending_llm,omitempty"`
	PendingToolOutput    string                         `json:"pending_tool_output,omitempty"`
	HasTurnConclusion    bool                           `json:"has_turn_conclusion,omitempty"`
	AwaitingTurn         bool                           `json:"awaiting_turn,omitempty"`
	MessageStatus        SendStatus                     `json:"message_status,omitempty"`
	SendGeneration       uint64                         `json:"send_generation,omitempty"`
	UpdatedAt            time.Time                      `json:"updated_at"`
	CreatedAt            time.Time                      `json:"created_at"`
	Confirmations        []Confirmation                 `json:"confirmations"`
	Conclusions          []string                       `json:"conclusions,omitempty"`
	Artifacts            []string                       `json:"artifacts,omitempty"`
	NextImageSeq         int                            `json:"next_image_seq,omitempty"`
	ErrorMessage         string                         `json:"error_message,omitempty"`
	RecommendedQuestions []string                       `json:"recommended_questions,omitempty"`
	WorkspaceID          string                         `json:"workspace_id,omitempty"`
}

// Snapshot returns a deep copy of the state as a plain struct suitable for
// serialization or returning to callers without holding the lock.
func (s *State) Snapshot() StateSnapshot {
	s.mu.RLock()
	defer s.mu.RUnlock()

	snap := StateSnapshot{
		SessionID:         s.SessionID,
		AgentID:           s.AgentID,
		Status:            s.Status,
		Mode:              s.Mode,
		AutoConfirm:       s.AutoConfirm,
		CurrentStep:       s.CurrentStep,
		TotalSteps:        s.TotalSteps,
		StepName:          s.StepName,
		WaitingFor:        s.WaitingFor,
		WaitingDetail:     s.WaitingDetail,
		Checkpoint:        s.Checkpoint,
		Requests:          maps.Clone(s.Requests),
		PendingAsk:        s.PendingAsk,
		AppliedEvents:     maps.Clone(s.AppliedEvents),
		TurnKey:           s.TurnKey,
		PendingLLM:        s.PendingLLM,
		PendingToolOutput: s.PendingToolOutput,
		HasTurnConclusion: s.HasTurnConclusion,
		AwaitingTurn:      s.AwaitingTurn,
		MessageStatus:     s.MessageStatus,
		SendGeneration:    s.SendGeneration,
		UpdatedAt:         s.UpdatedAt,
		CreatedAt:         s.CreatedAt,
		ErrorMessage:      s.ErrorMessage,
	}

	if len(s.Confirmations) > 0 {
		snap.Confirmations = make([]Confirmation, len(s.Confirmations))
		copy(snap.Confirmations, s.Confirmations)
	}
	if len(s.Conclusions) > 0 {
		snap.Conclusions = make([]string, len(s.Conclusions))
		copy(snap.Conclusions, s.Conclusions)
	}
	if len(s.Artifacts) > 0 {
		snap.Artifacts = make([]string, len(s.Artifacts))
		copy(snap.Artifacts, s.Artifacts)
	}
	if len(s.RecommendedQuestions) > 0 {
		snap.RecommendedQuestions = make([]string, len(s.RecommendedQuestions))
		copy(snap.RecommendedQuestions, s.RecommendedQuestions)
	}
	snap.NextImageSeq = s.NextImageSeq
	snap.WorkspaceID = s.WorkspaceID

	return snap
}

// ---------- Persistence ----------

func (s *State) Persist(dir string) (err error) {
	// Serialize snapshot creation as well as writes so an older snapshot cannot win.
	s.persistMu.Lock()
	defer s.persistMu.Unlock()
	snap := s.Snapshot()
	defer func() {
		if err != nil {
			log.Printf("[session:%s] persist failed: %v", snap.SessionID, err)
		}
	}()

	sessionDir := filepath.Join(dir, snap.SessionID)
	if err = os.MkdirAll(sessionDir, 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(snap, "", "  ")
	if err != nil {
		return err
	}
	file, err := os.CreateTemp(sessionDir, ".status-*")
	if err != nil {
		return err
	}
	tmp := file.Name()
	defer os.Remove(tmp)
	if _, err = file.Write(data); err != nil {
		file.Close()
		return err
	}
	if err = file.Sync(); err != nil {
		file.Close()
		return err
	}
	if err = file.Close(); err != nil {
		return err
	}
	if err = os.Rename(tmp, filepath.Join(sessionDir, "status.json")); err != nil {
		return err
	}
	directory, err := os.Open(sessionDir)
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}

// stateFromSnapshot reconstructs a mutable State from a persisted snapshot.
func stateFromSnapshot(snap *StateSnapshot) *State {
	s := &State{
		SessionID:            snap.SessionID,
		AgentID:              snap.AgentID,
		Status:               snap.Status,
		Mode:                 snap.Mode,
		AutoConfirm:          snap.AutoConfirm,
		CurrentStep:          snap.CurrentStep,
		TotalSteps:           snap.TotalSteps,
		StepName:             snap.StepName,
		WaitingFor:           snap.WaitingFor,
		WaitingDetail:        snap.WaitingDetail,
		Checkpoint:           snap.Checkpoint,
		Requests:             maps.Clone(snap.Requests),
		PendingAsk:           snap.PendingAsk,
		AppliedEvents:        maps.Clone(snap.AppliedEvents),
		TurnKey:              snap.TurnKey,
		PendingLLM:           snap.PendingLLM,
		PendingToolOutput:    snap.PendingToolOutput,
		HasTurnConclusion:    snap.HasTurnConclusion,
		AwaitingTurn:         snap.AwaitingTurn,
		MessageStatus:        snap.MessageStatus,
		SendGeneration:       snap.SendGeneration,
		UpdatedAt:            snap.UpdatedAt,
		CreatedAt:            snap.CreatedAt,
		Confirmations:        append([]Confirmation{}, snap.Confirmations...),
		Conclusions:          append([]string{}, snap.Conclusions...),
		Artifacts:            append([]string{}, snap.Artifacts...),
		NextImageSeq:         snap.NextImageSeq,
		ErrorMessage:         snap.ErrorMessage,
		RecommendedQuestions: append([]string{}, snap.RecommendedQuestions...),
		WorkspaceID:          snap.WorkspaceID,
		changed:              make(chan struct{}),
	}
	for key, request := range s.Requests {
		if request.Status == SendSending {
			request.Status = SendUnknown
			s.Requests[key] = request
			if s.PendingAsk == key {
				s.Status = StatusWaitingInput
				s.WaitingFor = request.Kind
				s.WaitingDetail = request.Content + "\nMessage delivery is unknown; verify remote state before retrying."
			}
		}
	}
	if s.MessageStatus == SendSending {
		s.MessageStatus = SendUnknown
		s.Status = StatusWaitingInput
		s.WaitingFor = "message_delivery"
		s.WaitingDetail = "Message delivery is unknown; verify remote state before retrying."
	}
	return s
}

// LoadState reads a persisted state from {dir}/{sessionID}/status.json.
// Returns nil if the file does not exist or cannot be parsed.
func LoadState(dir, sessionID string) *StateSnapshot {
	path := filepath.Join(dir, sessionID, "status.json")

	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}

	var snap StateSnapshot
	if err := json.Unmarshal(data, &snap); err != nil {
		return nil
	}

	return &snap
}

package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

const (
	SetupStatusPending   = "pending"
	SetupStatusSkipped   = "skipped"
	SetupStatusCompleted = "completed"

	SetupStepPending = "pending"
	SetupStepDone    = "done"
	SetupStepSkipped = "skipped"
)

// SetupWizardState tracks the lifecycle and per-step progress of the
// post-login first-run setup wizard. It never stores secrets.
type SetupWizardState struct {
	Status          string         `json:"status"`
	CurrentStep     int            `json:"currentStep"`
	StepStates      map[int]string `json:"stepStates"`
	BannerDismissed bool           `json:"bannerDismissed"`
	UpdatedAt       string         `json:"updatedAt,omitempty"`
}

func defaultSetupWizardState() SetupWizardState {
	return SetupWizardState{
		Status:      SetupStatusPending,
		CurrentStep: 1,
		StepStates: map[int]string{
			1: SetupStepPending,
			2: SetupStepPending,
			3: SetupStepPending,
			4: SetupStepPending,
			5: SetupStepPending,
		},
	}
}

func (v SetupWizardState) IsPending() bool {
	return v.Status == SetupStatusPending
}

func (v SetupWizardState) ShowResumeBanner() bool {
	return v.Status == SetupStatusSkipped && !v.BannerDismissed
}

// CompletedConfigSteps counts completed configuration steps among Steps 1–4.
func (v SetupWizardState) CompletedConfigSteps() int {
	n := 0
	for s := 1; s <= 4; s++ {
		if v.StepStates[s] == SetupStepDone {
			n++
		}
	}
	return n
}

func normalizeSetupStatus(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case SetupStatusSkipped:
		return SetupStatusSkipped
	case SetupStatusCompleted:
		return SetupStatusCompleted
	default:
		return SetupStatusPending
	}
}

func normalizeSetupStepState(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case SetupStepDone:
		return SetupStepDone
	case SetupStepSkipped:
		return SetupStepSkipped
	default:
		return SetupStepPending
	}
}

func clampSetupStep(step int) int {
	if step < 1 {
		return 1
	}
	if step > 5 {
		return 5
	}
	return step
}

func parseStepStatesJSON(raw string) map[int]string {
	out := defaultSetupWizardState().StepStates
	if strings.TrimSpace(raw) == "" {
		return out
	}
	var parsed map[string]string
	if err := json.Unmarshal([]byte(raw), &parsed); err != nil {
		return out
	}
	for k, val := range parsed {
		idx, err := strconv.Atoi(k)
		if err != nil || idx < 1 || idx > 5 {
			continue
		}
		out[idx] = normalizeSetupStepState(val)
	}
	return out
}

func marshalStepStatesJSON(states map[int]string) (string, error) {
	rawMap := make(map[string]string, 5)
	for s := 1; s <= 5; s++ {
		rawMap[strconv.Itoa(s)] = normalizeSetupStepState(states[s])
	}
	b, err := json.Marshal(rawMap)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

func (s *Store) SetupWizardState(ctx context.Context) (SetupWizardState, error) {
	v := defaultSetupWizardState()
	var (
		rawStates string
		dismissed int
	)
	err := s.db.QueryRowContext(ctx, `SELECT status, current_step, step_states_json, banner_dismissed, updated_at FROM setup_wizard_state WHERE id = 1`).
		Scan(&v.Status, &v.CurrentStep, &rawStates, &dismissed, &v.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return v, nil
	}
	if err != nil {
		return v, err
	}
	v.Status = normalizeSetupStatus(v.Status)
	v.CurrentStep = clampSetupStep(v.CurrentStep)
	v.StepStates = parseStepStatesJSON(rawStates)
	v.BannerDismissed = dismissed != 0
	return v, nil
}

func (s *Store) saveSetupWizardStateTx(ctx context.Context, tx *sql.Tx, v SetupWizardState) error {
	rawStates, err := marshalStepStatesJSON(v.StepStates)
	if err != nil {
		return fmt.Errorf("marshal setup step states: %w", err)
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO setup_wizard_state(id, status, current_step, step_states_json, banner_dismissed, updated_at)
		VALUES(1, ?, ?, ?, ?, strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
		ON CONFLICT(id) DO UPDATE SET
			status = excluded.status,
			current_step = excluded.current_step,
			step_states_json = excluded.step_states_json,
			banner_dismissed = excluded.banner_dismissed,
			updated_at = excluded.updated_at`,
		normalizeSetupStatus(v.Status), clampSetupStep(v.CurrentStep), rawStates, boolInt(v.BannerDismissed))
	return err
}

// SaveSetupWizardStep records the outcome of a single wizard step (done or
// skipped) and updates current_step without changing an already completed or
// skipped top-level status.
func (s *Store) SaveSetupWizardStep(ctx context.Context, step int, stepState string, nextStep int) (SetupWizardState, error) {
	if step < 1 || step > 5 {
		return SetupWizardState{}, fmt.Errorf("invalid setup step %d", step)
	}
	tx, err := s.beginWrite(ctx)
	if err != nil {
		return SetupWizardState{}, err
	}
	defer tx.Rollback()

	cur, err := s.setupWizardStateTx(ctx, tx)
	if err != nil {
		return SetupWizardState{}, err
	}
	cur.StepStates[step] = normalizeSetupStepState(stepState)
	if nextStep > 0 {
		cur.CurrentStep = clampSetupStep(nextStep)
	}
	if err := s.saveSetupWizardStateTx(ctx, tx, cur); err != nil {
		return SetupWizardState{}, err
	}
	if err := tx.Commit(); err != nil {
		return SetupWizardState{}, err
	}
	return s.SetupWizardState(ctx)
}

// CompleteSetupWizard marks the wizard completed and dismisses any skip banner.
func (s *Store) CompleteSetupWizard(ctx context.Context) (SetupWizardState, error) {
	tx, err := s.beginWrite(ctx)
	if err != nil {
		return SetupWizardState{}, err
	}
	defer tx.Rollback()

	cur, err := s.setupWizardStateTx(ctx, tx)
	if err != nil {
		return SetupWizardState{}, err
	}
	cur.Status = SetupStatusCompleted
	cur.CurrentStep = 5
	cur.StepStates[5] = SetupStepDone
	cur.BannerDismissed = true
	if err := s.saveSetupWizardStateTx(ctx, tx, cur); err != nil {
		return SetupWizardState{}, err
	}
	if err := tx.Commit(); err != nil {
		return SetupWizardState{}, err
	}
	return s.SetupWizardState(ctx)
}

// SkipSetupWizard transitions a pending wizard into skipped state so the user
// enters the library and sees a dismissible resume banner on /admin. If the
// wizard is already completed, its completed status is preserved.
func (s *Store) SkipSetupWizard(ctx context.Context) (SetupWizardState, error) {
	tx, err := s.beginWrite(ctx)
	if err != nil {
		return SetupWizardState{}, err
	}
	defer tx.Rollback()

	cur, err := s.setupWizardStateTx(ctx, tx)
	if err != nil {
		return SetupWizardState{}, err
	}
	if cur.Status != SetupStatusCompleted {
		cur.Status = SetupStatusSkipped
		cur.BannerDismissed = false
	}
	if err := s.saveSetupWizardStateTx(ctx, tx, cur); err != nil {
		return SetupWizardState{}, err
	}
	if err := tx.Commit(); err != nil {
		return SetupWizardState{}, err
	}
	return s.SetupWizardState(ctx)
}

// DismissSetupWizardBanner permanently hides the skipped-setup banner on /admin.
func (s *Store) DismissSetupWizardBanner(ctx context.Context) error {
	tx, err := s.beginWrite(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	cur, err := s.setupWizardStateTx(ctx, tx)
	if err != nil {
		return err
	}
	cur.BannerDismissed = true
	if err := s.saveSetupWizardStateTx(ctx, tx, cur); err != nil {
		return err
	}
	return tx.Commit()
}

// MarkSetupWizardCompleted marks setup completed in-place (used by test
// fixtures and explicit state transitions).
func (s *Store) MarkSetupWizardCompleted(ctx context.Context) error {
	_, err := s.CompleteSetupWizard(ctx)
	return err
}

func (s *Store) setupWizardStateTx(ctx context.Context, tx *sql.Tx) (SetupWizardState, error) {
	v := defaultSetupWizardState()
	var (
		rawStates string
		dismissed int
	)
	err := tx.QueryRowContext(ctx, `SELECT status, current_step, step_states_json, banner_dismissed, updated_at FROM setup_wizard_state WHERE id = 1`).
		Scan(&v.Status, &v.CurrentStep, &rawStates, &dismissed, &v.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return v, nil
	}
	if err != nil {
		return v, err
	}
	v.Status = normalizeSetupStatus(v.Status)
	v.CurrentStep = clampSetupStep(v.CurrentStep)
	v.StepStates = parseStepStatesJSON(rawStates)
	v.BannerDismissed = dismissed != 0
	return v, nil
}

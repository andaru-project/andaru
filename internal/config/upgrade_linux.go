package config

import (
	"context"
	"fmt"
	"time"

	"github.com/andaru-project/andaru/internal/authz"
)

// UpgradeView is what finishing an upgrade asks: the head the questions were worked out from, and every question
// met with the choices given so far. A question an earlier choice raised comes after it, unanswered.
type UpgradeView struct {
	SavedVersion Version    `json:"savedVersion"`
	Questions    []Question `json:"questions"`
}

// ErrUpgradePending refuses what would make a provisional choice permanent while the head waits for decisions.
var ErrUpgradePending error = &Fault{Code: "SETTINGS_UPGRADE_PENDING", Details: []FieldError{{Message: "The settings wait for decisions after an upgrade. Answer them first."}}}

// UpgradePreview works out the head's questions with the choices given so far (settings safe mode). With none
// given, it says whether the settings wait for decisions at all.
func (s *Store) UpgradePreview(ctx context.Context, answers map[string]string) (UpgradeView, error) {
	var view UpgradeView
	err := s.read(ctx, "", nil, func(head savedState, _ VersionInfo) error {
		_, _, _, questions, err := decodeSavedWith(head.storedBody, answers)
		view = UpgradeView{SavedVersion: head.Version, Questions: questions}
		return err
	})
	if view.Questions == nil {
		view.Questions = []Question{}
	}
	return view, err
}

// Upgrade answers every question of the head, expected, and saves the result as a new version of the current
// schema; the actor answered and this Andaru is its writer. The objects the answers change are recorded as changed.
func (s *Store) Upgrade(ctx context.Context, actor authz.Actor, expected Version, answers map[string]string) (SaveResult, error) {
	if expected == "" {
		return SaveResult{}, fault("PRECONDITION_REQUIRED", "/expectedSavedVersion", "Expected saved version is required.")
	}
	if _, err := ParseVersion(string(expected)); err != nil {
		return SaveResult{}, fault("INVALID_REQUEST", "/expectedSavedVersion", "Invalid saved version.")
	}
	// The objects the answers create or delete need their versions, so the answered document is read first.
	var requested []objectKey
	if err := s.read(ctx, "", nil, func(head savedState, _ VersionInfo) error {
		answered, _, _, _, err := decodeSavedWith(head.storedBody, answers)
		requested = restoreRequests(decodedBackup{configuration: &answered})
		return err
	}); err != nil {
		return SaveResult{}, err
	}
	p, current, next, err := s.startSave(ctx, actor, requested)
	if err != nil {
		return SaveResult{}, err
	}
	defer p.Close()
	if current.Version != expected {
		return SaveResult{}, fault("PRECONDITION_FAILED", "/expectedSavedVersion", "The saved configuration changed since its questions were read.")
	}
	if len(current.questions) == 0 {
		return SaveResult{}, fault("PRECONDITION_FAILED", "/expectedSavedVersion", "The saved configuration waits for no decision.")
	}
	answered, _, _, questions, err := decodeSavedWith(current.storedBody, answers)
	if err != nil {
		return SaveResult{}, err
	}
	var missing []FieldError
	for _, q := range questions {
		if !q.Answered {
			missing = append(missing, FieldError{Path: "/choices/" + q.ID, Message: fmt.Sprintf("Expected a choice for question %q.", q.ID)})
		}
	}
	if len(missing) > 0 {
		return SaveResult{}, &Fault{Code: "VALIDATION_FAILED", Details: missing}
	}
	var preview RestorePreview
	if p.prepared, preview, err = prepareRestoration(&current, decodedBackup{configuration: &answered, ownSecrets: true}, next, time.Now().UTC()); err != nil {
		return SaveResult{}, err
	}
	if len(preview.BlockingSecrets) != 0 {
		return SaveResult{}, fault("VALIDATION_FAILED", "/choices", "The answered settings name a secret the store does not hold.")
	}
	saved, err := p.publish(ctx)
	if err == nil {
		err = p.Close()
	}
	return saved, err
}

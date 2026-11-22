package engine

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/andaru-project/andaru/internal/audit"
	"github.com/andaru-project/andaru/internal/authz"
	"github.com/andaru-project/andaru/internal/config"
	"github.com/andaru-project/andaru/internal/generation"
	"github.com/andaru-project/andaru/internal/identity"
	"github.com/andaru-project/andaru/internal/jsonprofile"
	"github.com/andaru-project/andaru/internal/securefs"
)

var (
	errRestoreOperationConflict = errors.New("restore operation ID was reused with different content")
	errRestoreStateConflict     = errors.New("restore state no longer matches the recorded old or new outcome")
	restoreOperationPattern     = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$`)
	restoreBackupIDPattern      = regexp.MustCompile(`^[0-9a-f]{32}$`)
)

type coordinatedIdentityStore interface {
	identity.Store
	identity.BackupStore
	RestoreMatches(context.Context, identity.BackupDocuments) (bool, error)
}

type coordinatedRestoreRequest struct {
	OperationID              string
	Fingerprint              []byte
	Actor                    authz.Actor
	Source                   string
	Target                   string
	ExpectedSavedVersion     config.Version
	ExpectedIdentityRevision int64
	Configuration            config.RestoreDocuments
	Identity                 identity.RestoreDocuments
	RootAdministrator        *identity.RootAdministrator
}

type coordinatedRestoreResult struct {
	SavedVersion      config.Version `json:"savedVersion,omitempty"`
	IdentityRevision  int64          `json:"identityRevision,omitempty"`
	UnresolvedSecrets []string       `json:"unresolvedSecrets"`
	Replayed          bool           `json:"replayed"`
}

type restoreJournal struct {
	SchemaVersion            int                       `json:"schemaVersion"`
	OperationID              string                    `json:"operationId"`
	RequestHash              string                    `json:"requestHash"`
	State                    string                    `json:"state"`
	Actor                    authz.Actor               `json:"actor"`
	Source                   string                    `json:"source"`
	Target                   string                    `json:"target"`
	ExpectedSavedVersion     config.Version            `json:"expectedSavedVersion,omitempty"`
	TargetSavedVersion       config.Version            `json:"targetSavedVersion,omitempty"`
	Configuration            config.RestoreDocuments   `json:"configuration,omitempty"`
	ExpectedIdentityRevision int64                     `json:"expectedIdentityRevision,omitempty"`
	TargetIdentityRevision   int64                     `json:"targetIdentityRevision,omitempty"`
	Identity                 identity.RestoreDocuments `json:"identity,omitempty"`
	Result                   coordinatedRestoreResult  `json:"result"`
}

type restoreCoordinator struct {
	configuration *config.Store
	identities    coordinatedIdentityStore
	security      *securityCoordinator
	stateDir      string
	crash         func(string) error
}

func newRestoreCoordinator(configuration *config.Store, identities coordinatedIdentityStore, security *securityCoordinator, stateDir string) *restoreCoordinator {
	return &restoreCoordinator{configuration: configuration, identities: identities, security: security, stateDir: stateDir}
}

func (c *restoreCoordinator) Execute(ctx context.Context, request coordinatedRestoreRequest) (coordinatedRestoreResult, error) {
	if !restoreOperationPattern.MatchString(request.OperationID) || !validRestoreActor(request.Actor) || !validSource(request.Source) || !restoreBackupIDPattern.MatchString(request.Target) || (request.Configuration.Configuration == nil && request.Configuration.ConfigurationSecrets == nil && request.Identity.Users == nil && request.Identity.UserCredentials == nil) {
		return coordinatedRestoreResult{}, identity.ErrInvalid
	}
	requestHash, err := c.identities.HashRequest(ctx, request.Fingerprint)
	if err != nil {
		return coordinatedRestoreResult{}, err
	}
	if _, err = c.security.audits.Begin(ctx, audit.Intent{OperationID: request.OperationID, Actor: request.Actor, Source: request.Source, Action: audit.ActionBackupRestored, Target: request.Target, RequestHash: requestHash, Category: audit.CategoryProtected}); err != nil {
		return coordinatedRestoreResult{}, err
	}
	var result coordinatedRestoreResult
	err = generation.Observe(ctx, generation.Dir(c.stateDir), func(generation.Observation) error {
		return c.security.withAuthorizationLock(ctx, func() error {
			var executeErr error
			result, executeErr = c.executeLocked(ctx, request, requestHash)
			return executeErr
		})
	})
	if err != nil {
		if _, found, journalErr := c.readJournal(request.OperationID); journalErr == nil && !found {
			err = errors.Join(err, c.security.audits.Finish(ctx, audit.Outcome{OperationID: request.OperationID, Result: audit.ResultNotCommitted}))
		}
		return result, err
	}
	_ = c.security.audits.Finish(ctx, audit.Outcome{OperationID: request.OperationID, Result: audit.ResultSuccess})
	return result, nil
}

func (c *restoreCoordinator) executeLocked(ctx context.Context, request coordinatedRestoreRequest, requestHash string) (result coordinatedRestoreResult, err error) {
	existing, found, err := c.readJournal(request.OperationID)
	if err != nil {
		return result, err
	}
	if found {
		if existing.RequestHash != requestHash {
			return result, errRestoreOperationConflict
		}
		if existing.State == "committed" {
			existing.Result.Replayed = true
			return existing.Result, nil
		}
		if err = c.reconcileLocked(ctx, &existing); err != nil {
			return result, err
		}
		existing.Result.Replayed = true
		return existing.Result, nil
	}

	var preparedConfig *config.PreparedRestore
	if request.Configuration.Configuration != nil || request.Configuration.ConfigurationSecrets != nil {
		preparedConfig, err = c.configuration.PrepareRestore(ctx, request.Actor, request.ExpectedSavedVersion, request.Configuration)
		if err != nil {
			return result, err
		}
		defer func() { err = errors.Join(err, preparedConfig.Close()) }()
	}
	var preparedIdentity *identity.PreparedRestore
	if request.Identity.Users != nil || request.Identity.UserCredentials != nil {
		preparedIdentity, err = c.identities.PrepareRestore(ctx, request.ExpectedIdentityRevision, request.Identity, request.RootAdministrator)
		if err != nil {
			return result, err
		}
		defer func() { err = errors.Join(err, preparedIdentity.Close()) }()
	}

	journal := restoreJournal{SchemaVersion: 1, OperationID: request.OperationID, RequestHash: requestHash, State: "prepared", Actor: request.Actor, Source: request.Source, Target: request.Target, ExpectedSavedVersion: request.ExpectedSavedVersion, ExpectedIdentityRevision: request.ExpectedIdentityRevision}
	if preparedConfig != nil {
		journal.Configuration = preparedConfig.TargetDocuments()
		journal.TargetSavedVersion = preparedConfig.TargetVersion()
		journal.Result.SavedVersion = journal.TargetSavedVersion
		journal.Result.UnresolvedSecrets = slices.Clone(preparedConfig.Preview().UnresolvedSecrets)
	}
	if preparedIdentity != nil {
		target := preparedIdentity.TargetDocuments()
		journal.Identity = identity.RestoreDocuments{Users: target.Users, UserCredentials: target.UserCredentials}
		journal.TargetIdentityRevision = target.IdentityRevision
		journal.Result.IdentityRevision = target.IdentityRevision
	}
	if journal.Result.UnresolvedSecrets == nil {
		journal.Result.UnresolvedSecrets = []string{}
	}
	if err = c.writeJournal(journal); err != nil {
		return result, err
	}
	if err = c.atCrashPoint("after-journal"); err != nil {
		return result, err
	}
	if preparedConfig != nil {
		if err = c.atCrashPoint("before-config-publication"); err != nil {
			return result, err
		}
		published, publishErr := preparedConfig.Publish(ctx)
		if publishErr != nil {
			return result, publishErr
		}
		if published.SavedVersion != journal.TargetSavedVersion {
			return result, errRestoreStateConflict
		}
		if err = c.atCrashPoint("after-config-publication"); err != nil {
			return result, err
		}
	}
	if preparedIdentity != nil {
		if err = c.atCrashPoint("before-identity-publication"); err != nil {
			return result, err
		}
		published, publishErr := preparedIdentity.Publish(ctx)
		if publishErr != nil {
			return result, publishErr
		}
		if published.IdentityRevision != journal.TargetIdentityRevision {
			return result, errRestoreStateConflict
		}
		if err = c.atCrashPoint("after-identity-publication"); err != nil {
			return result, err
		}
	}
	journal.State = "committed"
	if err = c.writeJournal(journal); err != nil {
		return result, err
	}
	return journal.Result, nil
}

func (c *restoreCoordinator) Reconcile(ctx context.Context) error {
	journals, err := c.readJournals()
	if err != nil {
		return err
	}
	for i := range journals {
		journal := &journals[i]
		if journal.State == "committed" {
			continue
		}
		if err = generation.Observe(ctx, generation.Dir(c.stateDir), func(generation.Observation) error {
			return c.security.withAuthorizationLock(ctx, func() error { return c.reconcileLocked(ctx, journal) })
		}); err != nil {
			return fmt.Errorf("reconcile restore operation %s: %w", journal.OperationID, err)
		}
	}
	if err = c.security.audits.Reconcile(ctx, restoreAuditSource{coordinator: c, next: c.security}); err != nil && !errors.Is(err, audit.ErrReconciliationComplete) {
		return fmt.Errorf("reconcile restore audit: %w", err)
	}
	return nil
}

type restoreAuditSource struct {
	coordinator *restoreCoordinator
	next        audit.OperationSource
}

func (s restoreAuditSource) LookupOperation(ctx context.Context, operationID string) (audit.OperationRecord, bool, error) {
	journal, found, err := s.coordinator.readJournal(operationID)
	if err != nil || !found || journal.State != "committed" {
		if err != nil {
			return audit.OperationRecord{}, false, err
		}
		return s.next.LookupOperation(ctx, operationID)
	}
	return audit.OperationRecord{
		OperationID: journal.OperationID, RequestHash: journal.RequestHash, Actor: journal.Actor, Source: journal.Source,
		Action: audit.ActionBackupRestored, Target: journal.Target, Category: audit.CategoryProtected,
		Outcome: audit.Outcome{OperationID: journal.OperationID, Result: audit.ResultSuccess},
	}, true, nil
}

func (c *restoreCoordinator) reconcileLocked(ctx context.Context, journal *restoreJournal) error {
	if journal.State != "prepared" {
		return errRestoreStateConflict
	}
	// A restore a newer Andaru committed, and a downgrade then set aside with every other newer version, is done.
	setAside := false
	if journal.TargetSavedVersion != "" {
		var err error
		if setAside, err = c.configuration.Newer(ctx, config.Version(journal.TargetSavedVersion)); err != nil {
			return err
		}
	}
	if journal.TargetSavedVersion != "" && !setAside {
		matches, err := c.configuration.RestoreMatches(ctx, journal.TargetSavedVersion, journal.Configuration)
		if err != nil {
			return err
		}
		if !matches {
			current, err := c.configuration.Export(ctx, true)
			if err != nil {
				return err
			}
			if current.SavedVersion != journal.ExpectedSavedVersion {
				return errRestoreStateConflict
			}
			prepared, err := c.configuration.PrepareRestore(ctx, journal.Actor, journal.ExpectedSavedVersion, journal.Configuration)
			if err != nil {
				return err
			}
			published, publishErr := prepared.Publish(ctx)
			closeErr := prepared.Close()
			if publishErr != nil || closeErr != nil {
				return errors.Join(publishErr, closeErr)
			}
			if published.SavedVersion != journal.TargetSavedVersion {
				return errRestoreStateConflict
			}
		}
	}
	if journal.TargetIdentityRevision != 0 {
		target := identity.BackupDocuments{IdentityRevision: journal.TargetIdentityRevision, Users: journal.Identity.Users, UserCredentials: journal.Identity.UserCredentials}
		matches, err := c.identities.RestoreMatches(ctx, target)
		if err != nil {
			return err
		}
		if !matches {
			current, err := c.identities.Revision(ctx)
			if err != nil {
				return err
			}
			if current != journal.ExpectedIdentityRevision {
				return errRestoreStateConflict
			}
			prepared, err := c.identities.PrepareRestore(ctx, current, journal.Identity, nil)
			if err != nil {
				return err
			}
			published, publishErr := prepared.Publish(ctx)
			closeErr := prepared.Close()
			if publishErr != nil || closeErr != nil {
				return errors.Join(publishErr, closeErr)
			}
			if published.IdentityRevision != journal.TargetIdentityRevision {
				return errRestoreStateConflict
			}
		}
	}
	journal.State = "committed"
	return c.writeJournal(*journal)
}

func (c *restoreCoordinator) atCrashPoint(point string) error {
	if c.crash == nil {
		return nil
	}
	return c.crash(point)
}

func (c *restoreCoordinator) journalDir() string {
	return filepath.Join(c.stateDir, "restore-operations")
}
func (c *restoreCoordinator) journalPath(operationID string) string {
	return filepath.Join(c.journalDir(), operationID+".json")
}

func (c *restoreCoordinator) ensureJournalDir() error {
	dir := c.journalDir()
	if err := os.Mkdir(dir, 0o700); err != nil && !errors.Is(err, fs.ErrExist) {
		return err
	}
	info, err := os.Lstat(dir)
	if err != nil || !info.IsDir() || info.Mode().Perm() != 0o700 || !securefs.RootOwned(info) {
		return errors.New("restore journal directory must be root-owned 0700")
	}
	return nil
}

func validRestoreActor(actor authz.Actor) bool {
	_, err := actor.CanonicalLabel()
	return err == nil
}

func (c *restoreCoordinator) writeJournal(journal restoreJournal) error {
	if !restoreOperationPattern.MatchString(journal.OperationID) || !validRestoreActor(journal.Actor) || !validSource(journal.Source) || !restoreBackupIDPattern.MatchString(journal.Target) || journal.SchemaVersion != 1 || (journal.State != "prepared" && journal.State != "committed") || journal.RequestHash == "" {
		return errRestoreStateConflict
	}
	if err := c.ensureJournalDir(); err != nil {
		return err
	}
	raw, err := json.Marshal(journal)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(c.journalDir(), ".restore-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if err = tmp.Chmod(0o600); err == nil {
		_, err = tmp.Write(raw)
	}
	if err == nil {
		err = tmp.Sync()
	}
	if err == nil {
		err = tmp.Close()
	} else {
		_ = tmp.Close()
	}
	if err != nil {
		return err
	}
	if err = os.Rename(tmpPath, c.journalPath(journal.OperationID)); err != nil {
		return err
	}
	dir, err := os.Open(c.journalDir())
	if err != nil {
		return err
	}
	return errors.Join(dir.Sync(), dir.Close())
}

func (c *restoreCoordinator) readJournal(operationID string) (restoreJournal, bool, error) {
	if !restoreOperationPattern.MatchString(operationID) {
		return restoreJournal{}, false, identity.ErrInvalid
	}
	path := c.journalPath(operationID)
	info, err := os.Lstat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return restoreJournal{}, false, nil
	}
	if err != nil {
		return restoreJournal{}, false, err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 || !securefs.RootOwned(info) {
		return restoreJournal{}, false, errRestoreStateConflict
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return restoreJournal{}, false, err
	}
	journal, err := decodeRestoreJournal(raw)
	return journal, err == nil, err
}

func (c *restoreCoordinator) readJournals() ([]restoreJournal, error) {
	if err := c.ensureJournalDir(); err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(c.journalDir())
	if err != nil {
		return nil, err
	}
	journals := make([]restoreJournal, 0, len(entries))
	removedTemporary := false
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".restore-") {
			info, infoErr := entry.Info()
			if infoErr != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 || !securefs.RootOwned(info) {
				return nil, errors.Join(infoErr, errRestoreStateConflict)
			}
			if err = os.Remove(filepath.Join(c.journalDir(), entry.Name())); err != nil {
				return nil, err
			}
			removedTemporary = true
			continue
		}
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			return nil, errors.New("unexpected restore journal entry")
		}
		operationID := strings.TrimSuffix(entry.Name(), ".json")
		journal, found, err := c.readJournal(operationID)
		if err != nil || !found {
			return nil, errors.Join(err, errRestoreStateConflict)
		}
		journals = append(journals, journal)
	}
	if removedTemporary {
		dir, openErr := os.Open(c.journalDir())
		if openErr != nil {
			return nil, openErr
		}
		if err = errors.Join(dir.Sync(), dir.Close()); err != nil {
			return nil, err
		}
	}
	slices.SortFunc(journals, func(a, b restoreJournal) int { return strings.Compare(a.OperationID, b.OperationID) })
	return journals, nil
}

func decodeRestoreJournal(raw []byte) (restoreJournal, error) {
	if !jsonprofile.Valid(raw, true) {
		return restoreJournal{}, errRestoreStateConflict
	}
	var journal restoreJournal
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&journal) != nil || decoder.Decode(&struct{}{}) != io.EOF {
		return restoreJournal{}, errRestoreStateConflict
	}
	if journal.SchemaVersion != 1 || !restoreOperationPattern.MatchString(journal.OperationID) || !validRestoreActor(journal.Actor) || !validSource(journal.Source) || !restoreBackupIDPattern.MatchString(journal.Target) || (journal.State != "prepared" && journal.State != "committed") || journal.RequestHash == "" {
		return restoreJournal{}, errRestoreStateConflict
	}
	return journal, nil
}

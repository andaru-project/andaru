package audit

import (
	"context"
	"errors"
	"testing"
)

func TestFactoryResetEvent(t *testing.T) {
	s, _ := openTestStore(t)
	ctx := context.Background()
	for _, reason := range []string{ReasonFactoryResetBackupWritten, ReasonFactoryResetBackupSkipped} {
		event := testEvent(ActionRecoveryCompleted, CategoryProtected, ResultRecovered)
		event.Metadata = map[string]string{MetadataReasonCode: reason}
		if err := s.AppendSafety(ctx, event); err != nil {
			t.Fatalf("%s: %v", reason, err)
		}
	}
	event := testEvent(ActionRecoveryCompleted, CategoryProtected, ResultRecovered)
	event.Metadata = map[string]string{MetadataReasonCode: "factory_reset_backup_/root/backup.tar"}
	if err := s.AppendSafety(ctx, event); !errors.Is(err, ErrInvalid) {
		t.Fatalf("an unknown reason was recorded: %v", err)
	}
}

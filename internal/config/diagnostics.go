package config

import (
	"context"
	"errors"
	"log/slog"
	"syscall"
)

// diagnostic journals why storage is unavailable without ever logging cause.Error(),
// which may embed paths, SQL text or configuration/secret values. stage MUST be a
// trusted literal; cause is reduced to a numeric SQLite code, errno and context class.
func diagnostic(stage string, cause error) *Fault {
	attrs := []any{"stage", stage}
	var sqliteErr interface{ Code() int }
	if errors.As(cause, &sqliteErr) {
		attrs = append(attrs, "sqlite_code", sqliteErr.Code())
	}
	var errno syscall.Errno
	if errors.As(cause, &errno) {
		attrs = append(attrs, "errno", int(errno))
	}
	switch {
	case errors.Is(cause, context.DeadlineExceeded):
		attrs = append(attrs, "context", "deadline")
	case errors.Is(cause, context.Canceled):
		attrs = append(attrs, "context", "canceled")
	}
	slog.Error("configuration storage unavailable", attrs...)
	return unavailable()
}

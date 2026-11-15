package backup

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"unicode/utf8"

	"filippo.io/age"
)

const MaxPassphraseBytes = 1024
const scryptWorkFactor = 18

func ValidatePassphrase(passphrase string) error {
	if len(passphrase) == 0 || len(passphrase) > MaxPassphraseBytes || !utf8.ValidString(passphrase) || strings.IndexByte(passphrase, 0) >= 0 {
		return &Error{Code: "PASSPHRASE_INVALID"}
	}
	return nil
}

type contextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r contextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.reader.Read(p)
}

func encrypt(ctx context.Context, raw []byte, passphrase string) ([]byte, error) {
	recipient, err := age.NewScryptRecipient(passphrase)
	if err != nil {
		return nil, &Error{Code: "PASSPHRASE_INVALID"}
	}
	recipient.SetWorkFactor(scryptWorkFactor)
	var buffer bytes.Buffer
	buffer.Grow(len(raw) + 4096)
	writer, err := age.Encrypt(&buffer, recipient)
	if err != nil {
		return nil, &Error{Code: "ENCRYPTION_FAILED"}
	}
	if err = ctx.Err(); err != nil {
		return nil, err
	}
	if n, err := writer.Write(raw); err != nil || n != len(raw) {
		return nil, &Error{Code: "ENCRYPTION_FAILED"}
	}
	if err = writer.Close(); err != nil {
		return nil, &Error{Code: "ENCRYPTION_FAILED"}
	}
	if err = ctx.Err(); err != nil {
		return nil, err
	}
	if buffer.Len() > MaxArchiveSize {
		return nil, &Error{Code: "ARCHIVE_LIMIT"}
	}
	return buffer.Bytes(), nil
}

func decrypt(ctx context.Context, raw []byte, passphrase string) ([]byte, error) {
	if err := ValidatePassphrase(passphrase); err != nil {
		return nil, err
	}
	identity, err := age.NewScryptIdentity(passphrase)
	if err != nil {
		return nil, &Error{Code: "PASSPHRASE_INVALID"}
	}
	// The library's default permits work factors above the appliance's resource budget.
	// https://github.com/FiloSottile/age/blob/v1.3.2/scrypt.go#L151
	identity.SetMaxWorkFactor(scryptWorkFactor)
	reader, err := age.Decrypt(contextReader{ctx: ctx, reader: bytes.NewReader(raw)}, identity)
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if err != nil {
		var mismatch *age.NoIdentityMatchError
		if errors.As(err, &mismatch) {
			return nil, &Error{Code: "DECRYPTION_FAILED"}
		}
		return nil, &Error{Code: "UNSUPPORTED_ENCRYPTION"}
	}
	var buffer bytes.Buffer
	buffer.Grow(min(len(raw), MaxZIPSize+1) + 512)
	_, err = io.Copy(&buffer, io.LimitReader(contextReader{ctx: ctx, reader: reader}, MaxZIPSize+1))
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if buffer.Len() > MaxZIPSize {
		return nil, &Error{Code: "ARCHIVE_LIMIT"}
	}
	if err != nil {
		return nil, &Error{Code: "DECRYPTION_FAILED"}
	}
	return buffer.Bytes(), nil
}

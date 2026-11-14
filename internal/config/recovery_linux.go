package config

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strconv"
)

// VerifyRecoveryState verifies an existing cold state copy, without Open's initialization or additive migrations.
// The caller excludes writers and supplies the copy, never a live engine's state directory.
func VerifyRecoveryState(ctx context.Context, stateDir string) error {
	root := filepath.Join(stateDir, "configuration")
	if err := checkMembers(root, true); err != nil {
		return err
	}
	var dbs [2]*sql.DB
	for i, role := range []struct {
		name string
		app  int
	}{{"config.db", configApp}, {"secrets.db", secretApp}} {
		path := filepath.Join(root, role.name)
		if err := probeDatabase(ctx, path, role.app); err != nil {
			return err
		}
		db, err := openDatabase(ctx, path, "ro", false)
		if err != nil {
			return err
		}
		dbs[i] = db
		defer db.Close()
		rows, err := db.QueryContext(ctx, "PRAGMA integrity_check")
		if err != nil {
			return fmt.Errorf("check recovery database integrity: %w", err)
		}
		count := 0
		for rows.Next() {
			var status string
			if err = rows.Scan(&status); err != nil {
				rows.Close()
				return err
			}
			if status != "ok" {
				rows.Close()
				return errors.New("recovery database failed integrity check")
			}
			count++
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
		if count != 1 {
			return errors.New("recovery database has no integrity result")
		}
	}
	var version int64
	var schema int
	var configBody, configHash, secretHash []byte
	err := dbs[0].QueryRowContext(ctx, `SELECT v.id,v.schema_version,d.body,v.config_sha256,v.secrets_sha256 FROM pointers p JOIN versions v ON v.id=p.version_id JOIN documents d ON d.sha256=v.config_sha256 WHERE p.name='saved'`).Scan(&version, &schema, &configBody, &configHash, &secretHash)
	if err != nil {
		return fmt.Errorf("read recovery saved state: %w", err)
	}
	if version < 0 || schema != 1 || len(configBody) > documentLimit || len(configHash) != sha256.Size || len(secretHash) != sha256.Size {
		return errors.New("invalid recovery saved-state metadata")
	}
	sum := sha256.Sum256(configBody)
	if !bytes.Equal(sum[:], configHash) {
		return errors.New("recovery configuration hash mismatch")
	}
	declaration, err := decodeDeclaration(configBody)
	if err != nil {
		return errors.New("invalid recovery configuration document")
	}
	normalized, err := canonical(declaration)
	if err != nil || !bytes.Equal(normalized, configBody) {
		return errors.New("recovery configuration is not canonical")
	}
	var secretBody []byte
	if err = dbs[1].QueryRowContext(ctx, "SELECT body FROM documents WHERE sha256=?", secretHash).Scan(&secretBody); err != nil {
		return errors.New("recovery secret document is missing")
	}
	if len(secretBody) > documentLimit {
		return errors.New("recovery secret document is oversized")
	}
	sum = sha256.Sum256(secretBody)
	if !bytes.Equal(sum[:], secretHash) {
		return errors.New("recovery secret hash mismatch")
	}
	if _, err = decodeStrictLimit(secretBody, documentLimit); err != nil {
		return errors.New("invalid recovery secret document")
	}
	var secrets secretDocument
	if err = json.Unmarshal(secretBody, &secrets); err != nil {
		return errors.New("invalid recovery secret document")
	}
	if err = validateSecrets(secrets, Version(strconv.FormatInt(version, 10))); err != nil {
		return errors.New("invalid recovery secret version")
	}
	normalized, err = canonical(secrets)
	if err != nil || !bytes.Equal(normalized, secretBody) {
		return errors.New("recovery secret document is not canonical")
	}
	for _, link := range declaration.Links {
		if link.Password != nil {
			if _, ok := secrets.Scopes["core"][link.Password.Secret]; !ok {
				return errors.New("recovery configuration references a missing secret")
			}
		}
	}
	return nil
}

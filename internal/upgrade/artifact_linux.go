package upgrade

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

type artifact struct {
	Path, SHA256 string
	Available    bool
}
type packageMetadata struct {
	packageInfo
	hash string
	size int64
}

// retainPackage accepts only the exact installed package authenticated by the configured APT indexes.
// run must execute downloads in stage; keeping the working directory on exec.Cmd avoids a process-wide chdir.
func retainPackage(ctx context.Context, run commandRunner, info packageInfo, stage string) (artifact, error) {
	if !versionPattern.MatchString(info.Version) || !architecturePattern.MatchString(info.Architecture) {
		return artifact{}, errors.New("invalid installed package identity")
	}
	selected := "andaru:" + info.Architecture + "=" + info.Version
	raw, err := run(ctx, "apt-cache", "show", selected)
	if err != nil {
		var command *commandError
		if errors.As(err, &command) && command.code == 100 {
			return artifact{}, nil
		}
		return artifact{}, fmt.Errorf("read previous package metadata: %w", err)
	}
	metadata, err := recoveryMetadata(raw, info)
	if err != nil {
		return artifact{}, err
	}
	if metadata.hash == "" {
		return artifact{}, nil
	}
	files, err := debFiles(stage)
	if err != nil {
		return artifact{}, err
	}
	if len(files) == 0 {
		if _, err = run(ctx, "apt-get", "download", selected); err != nil {
			return artifact{}, fmt.Errorf("download previous package: %w", err)
		}
		files, err = debFiles(stage)
		if err != nil {
			return artifact{}, err
		}
	}
	if len(files) != 1 {
		return artifact{}, errors.New("expected exactly one previous package artifact")
	}
	path := files[0]
	f, err := os.Open(path)
	if err != nil {
		return artifact{}, err
	}
	stat, err := f.Stat()
	if err != nil || stat.Size() != metadata.size {
		f.Close()
		return artifact{}, errors.New("previous package size does not match APT metadata")
	}
	hash := sha256.New()
	_, readErr := io.Copy(hash, io.LimitReader(f, metadata.size+1))
	closeErr := f.Close()
	if err = errors.Join(readErr, closeErr); err != nil {
		return artifact{}, err
	}
	actual := hex.EncodeToString(hash.Sum(nil))
	if actual != metadata.hash {
		return artifact{}, errors.New("previous package checksum does not match APT metadata")
	}
	control, err := run(ctx, "dpkg-deb", "--field", path)
	if err != nil {
		return artifact{}, fmt.Errorf("inspect previous package: %w", err)
	}
	records, err := controlRecords(control)
	if err != nil {
		return artifact{}, err
	}
	if len(records) != 1 || records[0]["Package"] != "andaru" || records[0]["Version"] != info.Version || records[0]["Architecture"] != info.Architecture {
		return artifact{}, errors.New("previous package identity does not match the installed version and architecture")
	}
	if err = os.Chmod(path, 0600); err != nil {
		return artifact{}, err
	}
	return artifact{Path: path, SHA256: actual, Available: true}, nil
}

func recoveryMetadata(raw []byte, info packageInfo) (packageMetadata, error) {
	records, err := controlRecords(raw)
	if err != nil {
		return packageMetadata{}, err
	}
	var result packageMetadata
	for _, fields := range records {
		if fields["Package"] != "andaru" || fields["Version"] != info.Version || fields["Architecture"] != info.Architecture {
			continue
		}
		if fields["SHA256"] == "" {
			continue
		} // dpkg's local status stanza is not a signed archive artifact.
		h, err := hex.DecodeString(fields["SHA256"])
		if err != nil || len(h) != sha256.Size {
			return result, errors.New("invalid previous package SHA-256")
		}
		size, err := strconv.ParseInt(fields["Size"], 10, 64)
		if err != nil || size <= 0 || size == int64(^uint64(0)>>1) {
			return result, errors.New("invalid previous package size")
		}
		hash := hex.EncodeToString(h)
		if result.hash != "" && (result.hash != hash || result.size != size) {
			return result, errors.New("ambiguous previous package identity across APT sources")
		}
		result = packageMetadata{packageInfo: info, hash: hash, size: size}
	}
	return result, nil
}

func controlRecords(raw []byte) ([]map[string]string, error) {
	if err := textOutput(raw); err != nil {
		return nil, err
	}
	var result []map[string]string
	fields := map[string]string{}
	for _, line := range strings.Split(string(raw), "\n") {
		if line == "" {
			if len(fields) > 0 {
				result = append(result, fields)
				fields = map[string]string{}
			}
			continue
		}
		if line[0] == ' ' || line[0] == '\t' {
			continue
		}
		name, value, ok := strings.Cut(line, ":")
		if !ok || name == "" {
			return nil, errors.New("invalid package control metadata")
		}
		if _, exists := fields[name]; exists {
			return nil, errors.New("duplicate package control field")
		}
		fields[name] = strings.TrimSpace(value)
	}
	if len(fields) > 0 {
		result = append(result, fields)
	}
	return result, nil
}

func debFiles(stage string) ([]string, error) {
	entries, err := os.ReadDir(stage)
	if err != nil {
		return nil, err
	}
	var result []string
	for _, entry := range entries {
		if !strings.HasSuffix(entry.Name(), ".deb") {
			continue
		}
		path := filepath.Join(stage, entry.Name())
		st, err := os.Lstat(path)
		if err != nil {
			return nil, err
		}
		if !st.Mode().IsRegular() {
			return nil, errors.New("unsafe previous package artifact")
		}
		result = append(result, path)
	}
	return result, nil
}

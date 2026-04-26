// Cross-run resume state for Dropbox chunked uploads.
//
// Persists the Dropbox upload session_id and offset after each successful
// UploadSessionAppendV2 so that an interrupted rclone run can pick up where
// it left off. See the --dropbox-resume-uploads flag.

package dropbox

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	rclonefs "github.com/rclone/rclone/fs"
	"github.com/rclone/rclone/fs/config"
)

// resumeCacheSubdir lives under rclone's cache dir.
const resumeCacheSubdir = "dropbox-resume"

// resumeStateVersion identifies the layout of an on-disk state file. Older
// state files without this field deserialize to version 0 and are rejected so
// we don't misinterpret a missing PrefixHashHex as "hash of empty prefix".
const resumeStateVersion = 1

// resumeState is the on-disk record for one in-progress Dropbox upload.
type resumeState struct {
	Version       int       `json:"version"`
	SessionID     string    `json:"session_id"`
	Offset        uint64    `json:"offset"`
	SourceSize    int64     `json:"source_size"`
	SourceModTime time.Time `json:"source_modtime"`
	DestPath      string    `json:"dest_path"`
	// PrefixHashHex is the Dropbox content hash of source bytes 0..Offset.
	// Verified against a freshly-computed prefix hash on resume so content
	// changes that preserve size+mtime are caught before any network I/O.
	PrefixHashHex string `json:"prefix_hash_hex"`
}

// resumeCacheDir returns the directory where resume state files live,
// creating it on first use. An error from this function means resume is
// unavailable for this run; callers should log and proceed without it.
func resumeCacheDir() (string, error) {
	dir := filepath.Join(config.GetCacheDir(), resumeCacheSubdir)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return "", fmt.Errorf("create resume cache dir: %w", err)
	}
	return dir, nil
}

// resumeKey derives a stable filename for a given upload. Different source
// files to the same destination, or the same source to different destinations,
// produce different keys; the same fingerprint is reproducible across runs.
func resumeKey(destPath string, size int64, modTime time.Time) string {
	h := sha256.New()
	// SHA256.Write never returns a non-nil error (documented).
	_, _ = fmt.Fprintf(h, "%s\x00%d\x00%d\n", destPath, size, modTime.UTC().Unix())
	return hex.EncodeToString(h.Sum(nil))
}

// resumeStatePath is the full path to the state JSON for a given key.
func resumeStatePath(key string) (string, error) {
	dir, err := resumeCacheDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, key+".json"), nil
}

// loadResumeState returns (state, true, nil) for a present and parseable
// state, (nil, false, nil) when no state exists, or (nil, false, err) for
// unreadable or corrupt files. Corrupt files are surfaced rather than
// swallowed so we don't silently fall through to a zero-value state that
// might accidentally match a real fingerprint.
func loadResumeState(key string) (*resumeState, bool, error) {
	path, err := resumeStatePath(key)
	if err != nil {
		return nil, false, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, false, nil
		}
		return nil, false, fmt.Errorf("read resume state %s: %w", path, err)
	}
	var s resumeState
	if err := json.Unmarshal(data, &s); err != nil {
		return nil, false, fmt.Errorf("parse resume state %s: %w", path, err)
	}
	if s.Version != resumeStateVersion {
		return nil, false, fmt.Errorf("resume state %s: unsupported version %d (want %d)", path, s.Version, resumeStateVersion)
	}
	return &s, true, nil
}

// saveResumeState writes the state atomically (temp file + rename).
func saveResumeState(key string, s *resumeState) error {
	path, err := resumeStatePath(key)
	if err != nil {
		return err
	}
	data, err := json.Marshal(s)
	if err != nil {
		return fmt.Errorf("marshal resume state: %w", err)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0600); err != nil {
		return fmt.Errorf("write temp resume state %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("rename resume state %s -> %s: %w", tmp, path, err)
	}
	return nil
}

// deleteResumeState removes the state file; missing files are not an error.
func deleteResumeState(key string) error {
	path, err := resumeStatePath(key)
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("remove resume state %s: %w", path, err)
	}
	return nil
}

// fingerprintMatches reports whether the on-disk state still corresponds to
// the given source + destination. Modtimes are compared with time.Equal since
// deserialized times keep their original location.
func fingerprintMatches(state *resumeState, size int64, modTime time.Time, destPath string) bool {
	if state == nil {
		return false
	}
	return state.SourceSize == size &&
		state.SourceModTime.Equal(modTime) &&
		state.DestPath == destPath
}

// sweepResumeCache deletes state files older than maxAge. Called from NewFs
// so orphans from long-abandoned uploads don't pile up. Best-effort: any
// error is logged at debug and ignored.
func sweepResumeCache(maxAge time.Duration) {
	dir := filepath.Join(config.GetCacheDir(), resumeCacheSubdir)
	entries, err := os.ReadDir(dir)
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			rclonefs.Debugf(nil, "dropbox resume cache: sweep read dir %s: %v", dir, err)
		}
		return
	}
	cutoff := time.Now().Add(-maxAge)
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		if info.ModTime().Before(cutoff) {
			p := filepath.Join(dir, e.Name())
			if err := os.Remove(p); err != nil {
				rclonefs.Debugf(nil, "dropbox resume cache: sweep remove %s: %v", p, err)
			}
		}
	}
}

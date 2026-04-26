package dropbox

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/rclone/rclone/fs/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// withTempCacheDir points config.GetCacheDir() at a temp directory for the
// duration of the test so the real user cache isn't polluted. The original
// cache dir is restored on cleanup so subsequent tests aren't left pointing
// at a now-deleted TempDir.
func withTempCacheDir(t *testing.T) string {
	t.Helper()
	prev := config.GetCacheDir()
	dir := t.TempDir()
	require.NoError(t, config.SetCacheDir(dir))
	t.Cleanup(func() {
		_ = config.SetCacheDir(prev)
	})
	return dir
}

func TestResumeStateRoundTrip(t *testing.T) {
	withTempCacheDir(t)

	key := resumeKey("/foo/bar.bin", 123456789, time.Unix(1_700_000_000, 0))
	state := &resumeState{
		Version:       resumeStateVersion,
		SessionID:     "sess-abc",
		Offset:        48 * 1024 * 1024,
		SourceSize:    123456789,
		SourceModTime: time.Unix(1_700_000_000, 0).UTC(),
		DestPath:      "/foo/bar.bin",
		PrefixHashHex: "deadbeef",
	}

	require.NoError(t, saveResumeState(key, state))

	got, ok, err := loadResumeState(key)
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, state.Version, got.Version)
	assert.Equal(t, state.SessionID, got.SessionID)
	assert.Equal(t, state.Offset, got.Offset)
	assert.Equal(t, state.SourceSize, got.SourceSize)
	assert.True(t, state.SourceModTime.Equal(got.SourceModTime))
	assert.Equal(t, state.DestPath, got.DestPath)
	assert.Equal(t, state.PrefixHashHex, got.PrefixHashHex)

	// Atomic save must not leave the temp file behind.
	dir, err := resumeCacheDir()
	require.NoError(t, err)
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	for _, e := range entries {
		assert.NotContains(t, e.Name(), ".tmp", "stray temp file left behind: %s", e.Name())
	}

	require.NoError(t, deleteResumeState(key))
	_, ok, err = loadResumeState(key)
	require.NoError(t, err)
	assert.False(t, ok)
}

// TestLoadResumeStateRejectsOldVersion guards against silently accepting a
// state file written by a previous version of this code — missing Version
// deserializes to 0, which pre-dated the PrefixHashHex field and would let
// an empty saved prefix hash "match" a real recomputed hash.
func TestLoadResumeStateRejectsOldVersion(t *testing.T) {
	withTempCacheDir(t)

	key := "old-version"
	path, err := resumeStatePath(key)
	require.NoError(t, err)
	// Write a state that pre-dates the Version field.
	require.NoError(t, os.WriteFile(path, []byte(`{"session_id":"s","offset":42,"source_size":100,"dest_path":"/x"}`), 0600))

	_, ok, err := loadResumeState(key)
	assert.Error(t, err, "old-version state must be rejected")
	assert.False(t, ok)
}

func TestLoadResumeStateMissingReturnsFalseNoError(t *testing.T) {
	withTempCacheDir(t)

	_, ok, err := loadResumeState("no-such-key")
	require.NoError(t, err)
	assert.False(t, ok)
}

func TestLoadResumeStateCorruptErrors(t *testing.T) {
	withTempCacheDir(t)

	key := "corrupt"
	path, err := resumeStatePath(key)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(path, []byte("{not json"), 0600))

	_, ok, err := loadResumeState(key)
	assert.Error(t, err, "corrupt state must error, not silently return zero-value")
	assert.False(t, ok)
}

func TestDeleteResumeStateMissingNoError(t *testing.T) {
	withTempCacheDir(t)
	assert.NoError(t, deleteResumeState("never-saved"))
}

func TestResumeKeyStableAndDistinct(t *testing.T) {
	mod := time.Unix(1_700_000_000, 0)
	a := resumeKey("/foo.bin", 100, mod)
	b := resumeKey("/foo.bin", 100, mod)
	assert.Equal(t, a, b, "same inputs must produce same key")

	assert.NotEqual(t, a, resumeKey("/bar.bin", 100, mod), "different dest path")
	assert.NotEqual(t, a, resumeKey("/foo.bin", 101, mod), "different size")
	assert.NotEqual(t, a, resumeKey("/foo.bin", 100, mod.Add(time.Second)), "different modtime")
}

func TestFingerprintMatches(t *testing.T) {
	mod := time.Unix(1_700_000_000, 0)
	state := &resumeState{
		SourceSize:    100,
		SourceModTime: mod,
		DestPath:      "/foo.bin",
	}

	assert.True(t, fingerprintMatches(state, 100, mod, "/foo.bin"))
	assert.False(t, fingerprintMatches(state, 101, mod, "/foo.bin"), "size mismatch")
	assert.False(t, fingerprintMatches(state, 100, mod.Add(time.Second), "/foo.bin"), "modtime mismatch")
	assert.False(t, fingerprintMatches(state, 100, mod, "/bar.bin"), "destPath mismatch")
	assert.False(t, fingerprintMatches(nil, 100, mod, "/foo.bin"), "nil state never matches")
}

func TestFingerprintMatchesDifferentTimeZones(t *testing.T) {
	// JSON round-trip can put modtime in UTC while the caller supplies local;
	// fingerprintMatches must compare by instant, not wall-clock representation.
	withTempCacheDir(t)

	loc, err := time.LoadLocation("America/Denver")
	require.NoError(t, err)
	mod := time.Date(2024, 1, 15, 10, 0, 0, 0, loc)

	key := resumeKey("/foo.bin", 100, mod)
	require.NoError(t, saveResumeState(key, &resumeState{
		Version:       resumeStateVersion,
		SessionID:     "s",
		SourceSize:    100,
		SourceModTime: mod,
		DestPath:      "/foo.bin",
	}))

	got, ok, err := loadResumeState(key)
	require.NoError(t, err)
	require.True(t, ok)
	assert.True(t, fingerprintMatches(got, 100, mod, "/foo.bin"),
		"same instant in different timezone representation must still match")
}

func TestSweepResumeCache(t *testing.T) {
	withTempCacheDir(t)

	dir, err := resumeCacheDir()
	require.NoError(t, err)

	oldKey := "old-entry"
	newKey := "new-entry"
	require.NoError(t, saveResumeState(oldKey, &resumeState{Version: resumeStateVersion, SessionID: "old"}))
	require.NoError(t, saveResumeState(newKey, &resumeState{Version: resumeStateVersion, SessionID: "new"}))

	// Age the "old" file by backdating its mtime.
	oldPath := filepath.Join(dir, oldKey+".json")
	backdated := time.Now().Add(-30 * 24 * time.Hour)
	require.NoError(t, os.Chtimes(oldPath, backdated, backdated))

	sweepResumeCache(7 * 24 * time.Hour)

	_, ok, err := loadResumeState(oldKey)
	require.NoError(t, err)
	assert.False(t, ok, "old entry must be swept")

	_, ok, err = loadResumeState(newKey)
	require.NoError(t, err)
	assert.True(t, ok, "recent entry must survive sweep")
}

func TestSweepResumeCacheMissingDirIsNoOp(t *testing.T) {
	// Point at a cache dir that has no dropbox-resume subdir yet.
	withTempCacheDir(t)
	// Don't call resumeCacheDir() first; sweep must handle missing dir silently.
	sweepResumeCache(7 * 24 * time.Hour)
}

package dbhash_test

import (
	"bytes"
	"encoding/hex"
	"fmt"
	"testing"

	"github.com/rclone/rclone/backend/dropbox/dbhash"
	"github.com/stretchr/testify/assert"
)

func testChunk(t *testing.T, chunk int) {
	data := make([]byte, chunk)
	for i := range chunk {
		data[i] = 'A'
	}
	for _, test := range []struct {
		n    int
		want string
	}{
		{0, "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"},
		{1, "1cd6ef71e6e0ff46ad2609d403dc3fee244417089aa4461245a4e4fe23a55e42"},
		{2, "01e0655fb754d10418a73760f57515f4903b298e6d67dda6bf0987fa79c22c88"},
		{4096, "8620913d33852befe09f16fff8fd75f77a83160d29f76f07e0276e9690903035"},
		{4194303, "647c8627d70f7a7d13ce96b1e7710a771a55d41a62c3da490d92e56044d311fa"},
		{4194304, "d4d63bac5b866c71620185392a8a6218ac1092454a2d16f820363b69852befa3"},
		{4194305, "8f553da8d00d0bf509d8470e242888be33019c20c0544811f5b2b89e98360b92"},
		{8388607, "83b30cf4fb5195b04a937727ae379cf3d06673bf8f77947f6a92858536e8369c"},
		{8388608, "e08b3ba1f538804075c5f939accdeaa9efc7b5c01865c94a41e78ca6550a88e7"},
		{8388609, "02c8a4aefc2bfc9036f89a7098001865885938ca580e5c9e5db672385edd303c"},
	} {
		d := dbhash.New()
		var toWrite int
		for toWrite = test.n; toWrite >= chunk; toWrite -= chunk {
			n, err := d.Write(data)
			assert.Nil(t, err)
			assert.Equal(t, chunk, n)
		}
		n, err := d.Write(data[:toWrite])
		assert.Nil(t, err)
		assert.Equal(t, toWrite, n)
		got := hex.EncodeToString(d.Sum(nil))
		assert.Equal(t, test.want, got, fmt.Sprintf("when testing length %d", n))

	}
}

func TestHashChunk16M(t *testing.T)  { testChunk(t, 16*1024*1024) }
func TestHashChunk8M(t *testing.T)   { testChunk(t, 8*1024*1024) }
func TestHashChunk4M(t *testing.T)   { testChunk(t, 4*1024*1024) }
func TestHashChunk2M(t *testing.T)   { testChunk(t, 2*1024*1024) }
func TestHashChunk1M(t *testing.T)   { testChunk(t, 1*1024*1024) }
func TestHashChunk64k(t *testing.T)  { testChunk(t, 64*1024) }
func TestHashChunk32k(t *testing.T)  { testChunk(t, 32*1024) }
func TestHashChunk2048(t *testing.T) { testChunk(t, 2048) }
func TestHashChunk2047(t *testing.T) { testChunk(t, 2047) }

// TestMultiSum verifies that Sum can be called repeatedly, including with
// Writes interleaved, and that each Sum reflects exactly the bytes written
// so far, matching a single-pass hash of the same input.
func TestMultiSum(t *testing.T) {
	d := dbhash.New()

	sumEmpty := hex.EncodeToString(d.Sum(nil))
	assert.Equal(t, "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855", sumEmpty)
	assert.Equal(t, sumEmpty, hex.EncodeToString(d.Sum(nil)), "Sum with no Writes must be stable")

	_, _ = d.Write([]byte{'A'})
	sumA := hex.EncodeToString(d.Sum(nil))
	assert.Equal(t, "1cd6ef71e6e0ff46ad2609d403dc3fee244417089aa4461245a4e4fe23a55e42", sumA)
	assert.Equal(t, sumA, hex.EncodeToString(d.Sum(nil)), "repeated Sum without Write must be stable")

	_, _ = d.Write([]byte("BCDEFG"))
	interleaved := hex.EncodeToString(d.Sum(nil))

	d2 := dbhash.New()
	_, _ = d2.Write([]byte("ABCDEFG"))
	expected := hex.EncodeToString(d2.Sum(nil))
	assert.Equal(t, expected, interleaved, "interleaved Write/Sum/Write/Sum must match single-pass hash")
}

// TestMultiSumAcrossBlockBoundary exercises the partial-block-clone path by
// Sum-ing at three points that each straddle a 4MB block boundary differently:
// before the boundary (partial block pending), just after it (clean boundary),
// and mid-way through the next block. Each Sum must match a fresh single-pass
// hash of the same cumulative prefix.
func TestMultiSumAcrossBlockBoundary(t *testing.T) {
	const block = 4 * 1024 * 1024
	first := bytes.Repeat([]byte{'A'}, block-100)
	second := bytes.Repeat([]byte{'B'}, 200)
	third := bytes.Repeat([]byte{'C'}, block)

	expect := func(parts ...[]byte) string {
		h := dbhash.New()
		for _, p := range parts {
			_, _ = h.Write(p)
		}
		return hex.EncodeToString(h.Sum(nil))
	}

	d := dbhash.New()
	_, _ = d.Write(first)
	assert.Equal(t, expect(first), hex.EncodeToString(d.Sum(nil)), "partial-block Sum before 4MB boundary")
	_, _ = d.Write(second)
	assert.Equal(t, expect(first, second), hex.EncodeToString(d.Sum(nil)), "partial-block Sum after crossing boundary")
	_, _ = d.Write(third)
	assert.Equal(t, expect(first, second, third), hex.EncodeToString(d.Sum(nil)), "partial-block Sum deep into next block")
}

func TestSize(t *testing.T) {
	d := dbhash.New()
	assert.Equal(t, 32, d.Size())
}

func TestBlockSize(t *testing.T) {
	d := dbhash.New()
	assert.Equal(t, 64, d.BlockSize())
}

func TestSum(t *testing.T) {
	assert.Equal(t,
		[64]byte{
			0x1c, 0xd6, 0xef, 0x71, 0xe6, 0xe0, 0xff, 0x46,
			0xad, 0x26, 0x09, 0xd4, 0x03, 0xdc, 0x3f, 0xee,
			0x24, 0x44, 0x17, 0x08, 0x9a, 0xa4, 0x46, 0x12,
			0x45, 0xa4, 0xe4, 0xfe, 0x23, 0xa5, 0x5e, 0x42,
		},
		dbhash.Sum([]byte{'A'}),
	)
}

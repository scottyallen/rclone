// Package dbhash implements the dropbox hash as described in
//
// https://www.dropbox.com/developers/reference/content-hash
package dbhash

import (
	"crypto/sha256"
	"encoding"
	"fmt"
	"hash"
)

const (
	// BlockSize of the checksum in bytes.
	BlockSize = sha256.BlockSize
	// Size of the checksum in bytes.
	Size              = sha256.BlockSize
	bytesPerBlock     = 4 * 1024 * 1024
	hashReturnedError = "hash function returned error"
)

type digest struct {
	n         int // bytes written into blockHash so far
	blockHash hash.Hash
	totalHash hash.Hash
}

// New returns a new hash.Hash computing the Dropbox checksum.
func New() hash.Hash {
	d := &digest{}
	d.Reset()
	return d
}

// writeBlockHash finalizes the current 4MB block's hash into totalHash and
// resets the block state. Called from Write when a block fills up.
func (d *digest) writeBlockHash() {
	blockHash := d.blockHash.Sum(nil)
	_, err := d.totalHash.Write(blockHash)
	if err != nil {
		panic(hashReturnedError)
	}
	// reset counters for blockhash
	d.n = 0
	d.blockHash.Reset()
}

// Write writes len(p) bytes from p to the underlying data stream. It returns
// the number of bytes written from p (0 <= n <= len(p)) and any error
// encountered that caused the write to stop early. Write must return a non-nil
// error if it returns n < len(p). Write must not modify the slice data, even
// temporarily.
//
// Implementations must not retain p.
func (d *digest) Write(p []byte) (n int, err error) {
	n = len(p)
	for len(p) > 0 {
		toWrite := min(bytesPerBlock-d.n, len(p))
		_, err = d.blockHash.Write(p[:toWrite])
		if err != nil {
			panic(hashReturnedError)
		}
		d.n += toWrite
		p = p[toWrite:]
		// Accumulate the total hash
		if d.n == bytesPerBlock {
			d.writeBlockHash()
		}
	}
	return n, nil
}

// Sum appends the current hash to b and returns the resulting slice.
// Sum is non-mutating: further Writes continue the stream as if Sum had not
// been called, and Sum may be called repeatedly.
func (d *digest) Sum(b []byte) []byte {
	if d.n == 0 {
		// No partial block pending; totalHash already reflects every block.
		return d.totalHash.Sum(b)
	}
	// A partial block is pending. Clone totalHash and flush the partial
	// block hash into the clone, so the live totalHash keeps accumulating
	// the in-progress block correctly when Write resumes.
	clone := d.cloneTotalHash()
	// blockHash.Sum is non-mutating per the stdlib sha256 contract, so this
	// reads the partial-block hash without disturbing d.blockHash.
	partialBlockHash := d.blockHash.Sum(nil)
	if _, err := clone.Write(partialBlockHash); err != nil {
		panic(hashReturnedError)
	}
	return clone.Sum(b)
}

// cloneTotalHash duplicates d.totalHash's sha256 state by round-tripping
// through encoding.BinaryMarshaler, which the stdlib sha256 digest
// implements. This lets Sum() finalize a partial block into a copy of
// totalHash without mutating the live hasher. Scoped to d.totalHash so the
// sha256 invariant (set by Reset) stays local to this file and callers can't
// pass in an arbitrary hash.Hash that would be silently coerced to sha256.
func (d *digest) cloneTotalHash() hash.Hash {
	marshaler, ok := d.totalHash.(encoding.BinaryMarshaler)
	if !ok {
		panic("dbhash: sha256 hasher does not implement BinaryMarshaler")
	}
	state, err := marshaler.MarshalBinary()
	if err != nil {
		panic(fmt.Errorf("dbhash: marshal sha256 state: %w", err))
	}
	clone := sha256.New()
	unmarshaler, ok := clone.(encoding.BinaryUnmarshaler)
	if !ok {
		panic("dbhash: sha256 hasher does not implement BinaryUnmarshaler")
	}
	if err := unmarshaler.UnmarshalBinary(state); err != nil {
		panic(fmt.Errorf("dbhash: unmarshal sha256 state: %w", err))
	}
	return clone
}

// Reset resets the Hash to its initial state.
func (d *digest) Reset() {
	d.n = 0
	d.totalHash = sha256.New()
	d.blockHash = sha256.New()
}

// Size returns the number of bytes Sum will return.
func (d *digest) Size() int {
	return d.totalHash.Size()
}

// BlockSize returns the hash's underlying block size.
// The Write method must be able to accept any amount
// of data, but it may operate more efficiently if all writes
// are a multiple of the block size.
func (d *digest) BlockSize() int {
	return d.totalHash.BlockSize()
}

// Sum returns the Dropbox checksum of the data.
func Sum(data []byte) [Size]byte {
	var d digest
	d.Reset()
	_, _ = d.Write(data)
	var out [Size]byte
	d.Sum(out[:0])
	return out
}

// must implement this interface
var _ hash.Hash = (*digest)(nil)

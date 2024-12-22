package blockstorage

import (
	"context"
	"crypto/sha256"
)

// additionally calculates and stores real hash of encrypted data.
// this enables the detection of bit-rot
type EncryptedHashBlockStorage struct {
	store HashBlockStorageI
}

// CalculateInternalPathRepresentationFromHash implements HashBlockStorageI.
func (e *EncryptedHashBlockStorage) CalculateInternalPathRepresentationFromHash(hash []byte) string {
	return e.store.CalculateInternalPathRepresentationFromHash(hash)
}

// UncheckedGet implements HashBlockStorageI.
func (e *EncryptedHashBlockStorage) UncheckedGet(hash []byte, downloadData bool) (data []byte, err error) {
	return e.store.UncheckedGet(hash, downloadData)
}

// AnnounceDelete implements HashBlockStorageI.
func (e *EncryptedHashBlockStorage) AnnounceDelete(hash []byte) error {
	return e.store.AnnounceDelete(hash)
}

// DeAnnounceDelete implements HashBlockStorageI.
func (e *EncryptedHashBlockStorage) DeAnnounceDelete(hash []byte) error {
	return e.store.DeAnnounceDelete(hash)
}

// GetBlockHashState implements HashBlockStorageI.
func (e *EncryptedHashBlockStorage) GetBlockHashState(hash []byte) (HashBlockState, error) {
	return e.store.GetBlockHashState(hash)
}

// UncheckedDelete implements HashBlockStorageI.
func (e *EncryptedHashBlockStorage) UncheckedDelete(hash []byte) error {
	return e.store.UncheckedDelete(hash)
}

const HASH_MAPPING_PREFIX = "real_hashes/"

func (e *EncryptedHashBlockStorage) genRealHashKey(hash []byte) string {
	return HASH_MAPPING_PREFIX + e.store.CalculateInternalPathRepresentationFromHash(hash)
}

// Close implements HashBlockStorageI.
func (e *EncryptedHashBlockStorage) Close() error {
	return e.store.Close()
}

// Delete implements HashBlockStorageI.
func (e *EncryptedHashBlockStorage) DeleteReservation(hash []byte) error {
	return e.store.DeleteReservation(hash)
	// TODO: how to cleanup related metadata?
	//e.store.DeleteMeta(e.genRealHashKey(hash))
}

// DeleteMeta implements HashBlockStorageI.
func (e *EncryptedHashBlockStorage) DeleteMeta(name string) error {
	return e.store.DeleteMeta(name)
}

// Get implements HashBlockStorageI.
func (e *EncryptedHashBlockStorage) ReserveAndGet(hash []byte, downloadData bool) (data []byte, err error) {
	return e.store.ReserveAndGet(hash, downloadData)
}

// GetBlockHashesCache implements HashBlockStorageI.
func (e *EncryptedHashBlockStorage) GetBlockHashesCache(
	ctx context.Context, progressNotifier func(count int, currentHash []byte)) (HashBlockStateMap, error) {
	return e.store.GetBlockHashesCache(ctx, progressNotifier)
}

// GetBlockHashesCountHint implements HashBlockStorageI.
func (e *EncryptedHashBlockStorage) GetBlockHashesCountHint() (int, error) {
	return e.store.GetBlockHashesCountHint()
}

// GetMeta implements HashBlockStorageI.
func (e *EncryptedHashBlockStorage) GetMeta(name string) (data []byte, err error) {
	return e.store.GetMeta(name)
}

// Set implements HashBlockStorageI.
func (e *EncryptedHashBlockStorage) ReserveAndSet(hash []byte, data []byte) error {
	real_hash := sha256.Sum256(data)
	err := e.store.SetMeta(e.genRealHashKey(hash), real_hash[:])
	if err != nil {
		return err
	}
	return e.store.ReserveAndSet(hash, data)
}

// SetMeta implements HashBlockStorageI.
func (e *EncryptedHashBlockStorage) SetMeta(name string, data []byte) error {
	return e.store.SetMeta(name, data)
}

func NewEncryptedHashBlockStorage(store HashBlockStorageI) *EncryptedHashBlockStorage {
	return &EncryptedHashBlockStorage{
		store: store,
	}
}

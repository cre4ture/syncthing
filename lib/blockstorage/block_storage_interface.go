// Copyright (C) 2024 The Syncthing Authors.
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this file,
// You can obtain one at https://mozilla.org/MPL/2.0/.

package blockstorage

import (
	"context"
	"errors"
	"io"
)

const LOCAL_HAVE_FI_META_PREFIX = "LocalHaveMeta"

var ErrConnectionFailed = errors.New("connection to bucket failed. Retry later again")
var ErrNotAvailable = errors.New("bucket entry doesn't exist")
var ErrRetryLater = errors.New("bucket entry is blocked by pending delete. Retry later")
var ErrReadOnly = errors.New("bucket access is limited to read only")

type HashBlockState struct {
	dataExists       bool
	reservedByMe     bool
	reservedByOthers bool
	deletionPending  bool
}

type HashAndState struct {
	hash  []byte
	state HashBlockState
}

func (s HashBlockState) IsAvailable() bool {
	return s.dataExists && (!s.deletionPending)
}

func (s HashBlockState) IsAvailableAndReservedByMe() bool {
	return s.dataExists && s.reservedByMe
}

func (s HashBlockState) IsAvailableAndFree() bool {
	return s.dataExists && (!s.reservedByMe) && (!s.reservedByOthers)
}

func (s HashBlockState) IsReservedBySomeone() bool {
	return s.dataExists && (s.reservedByMe || s.reservedByOthers)
}

type HashBlockStateMap map[string]HashBlockState

type HashBlockStorageI interface {
	io.Closer

	// just Has() alone is not allowed as this doesn't allow proper reference counting
	// Has(hash []byte) (ok bool)

	ReserveAndGet(hash []byte, downloadData bool) (data []byte, err error)
	ReserveAndSet(hash []byte, data []byte) error
	DeleteReservation(hash []byte) error
	AnnounceDelete(hash []byte) error
	DeAnnounceDelete(hash []byte) error
	UncheckedDelete(hash []byte) error
	GetMeta(name string) (data []byte, err error)
	SetMeta(name string, data []byte) error
	DeleteMeta(name string) error

	// internal use only - so far
	//IterateBlocks(fn func(hash []byte, state HashBlockState) bool) error
	GetBlockHashesCountHint() (int, error)
	GetBlockHashesCache(ctx context.Context, progressNotifier func(count int, currentHash []byte)) (HashBlockStateMap, error)
	GetBlockHashState(hash []byte) (HashBlockState, error)
}

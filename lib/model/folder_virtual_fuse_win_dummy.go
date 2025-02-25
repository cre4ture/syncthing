// Copyright (C) 2024 The Syncthing Authors.
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this file,
// You can obtain one at https://mozilla.org/MPL/2.0/.

//go:build windows
// +build windows

package model

import (
	"io"

	"github.com/syncthing/syncthing/lib/config"
	"github.com/syncthing/syncthing/lib/protocol"
)

type syncthingVirtualFolderFuseAdapter struct{}
type DbFileSetReadI interface{}
type DbFileSetWriteI interface{}
type BlockDataAccessI interface{}

func NewSyncthingVirtualFolderFuseAdapter(
	modelID protocol.ShortID,
	folderID string,
	folderType config.FolderType,
	fset DbFileSetReadI,
	fsetRW DbFileSetWriteI,
	dataAccess BlockDataAccessI,
) *syncthingVirtualFolderFuseAdapter {
	panic("not implemented")
}

type SyncthingVirtualFolderAccessI interface{}

func NewVirtualFolderMount(mountPath string, folderId, folderLabel string,
	stFolder SyncthingVirtualFolderAccessI,
) (io.Closer, error) {
	panic("not implemented")
}

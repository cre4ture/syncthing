// Copyright (C) 2021 The Syncthing Authors.
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this file,
// You can obtain one at https://mozilla.org/MPL/2.0/.

// Package virtualmount implements the `syncthing virtualmount` subcommand.
package virtualmount

import (
	"log"
	"os"
	"os/signal"
	"time"

	"github.com/patrickmn/go-cache"
	"github.com/syncthing/syncthing/cmd/syncthing/virtual"
	"github.com/syncthing/syncthing/lib/blockstorage"
	"github.com/syncthing/syncthing/lib/config"
	"github.com/syncthing/syncthing/lib/hashutil"
	"github.com/syncthing/syncthing/lib/model"
	"github.com/syncthing/syncthing/lib/protocol"
	"github.com/syncthing/syncthing/lib/utils"
)

type CLI struct {
	DeviceID  string `help:"Device ID of the virtual folder, if it cannot be determined automatically"`
	FolderID  string `help:"Folder ID of the virtual folder, if it cannot be determined automatically"`
	URL       string `arg:"" required:"1" help:"URL to virtual folder. Including \":virtual:\" or \":virtual-s:\""`
	MountPath string `required:"1" xor:"mode" placeholder:"PATH" help:"Directory where to mount the virtual folder"`
}

func (c *CLI) Run() error {

	osa, err := virtual.NewOfflineDataAccess(
		c.URL, c.DeviceID, c.FolderID,
	)

	if err != nil {
		return err
	}

	folderLabel := "offline-folder-mount"

	fsetRW := &OfflineDbFileSetWrite{}
	dataCache := cache.New(5*time.Minute, 1*time.Minute)
	dataAccess := &OfflineBlockDataAccess{
		blockStorage:   osa.BlockStorage,
		blockDataCache: utils.NewProtected(dataCache),
	}

	stVF := model.NewSyncthingVirtualFolderFuseAdapter(
		protocol.ShortID(0),
		c.FolderID,
		config.FolderTypeReceiveOnly, // for read only
		osa.FSetRO,
		fsetRW,
		dataAccess,
	)

	mount, err := model.NewVirtualFolderMount(
		c.MountPath, c.FolderID, folderLabel, stVF,
	)

	if err != nil {
		return err
	}

	defer mount.Close()

	// block till signal
	signalChan := make(chan os.Signal, 1)
	signal.Notify(signalChan, os.Interrupt)
	sig := <-signalChan
	log.Printf("Received signal %s; shutting down", sig)

	return nil
}

type OfflineBlockDataAccess struct {
	blockStorage   *blockstorage.GoCloudUrlStorage
	blockDataCache *utils.Protected[*cache.Cache]
}

type CachedBlock struct {
	data   []byte
	err    error
	result model.GetBlockDataResult
}

// GetBlockDataFromCacheOrDownloadI implements model.BlockDataAccessI.
func (o *OfflineBlockDataAccess) GetBlockDataFromCacheOrDownloadI(
	file *protocol.FileInfo, block protocol.BlockInfo,
) ([]byte, error, model.GetBlockDataResult) {

	cacheKey := hashutil.HashToStringMapKey(block.Hash)
	var dataBuffer *CachedBlock = nil
	var pCD *utils.Protected[CachedBlock] = nil
	ok := false
	func() {
		cachemap := o.blockDataCache.Lock()
		defer o.blockDataCache.Unlock()

		var cachedData interface{}
		cachedData, ok = (*cachemap).Get(cacheKey)
		if ok {
			pCD = cachedData.(*utils.Protected[CachedBlock])
		} else {
			pCD = utils.NewProtected(CachedBlock{})
			(*cachemap).Set(cacheKey, pCD, 0)
			dataBuffer = pCD.Lock() // lock before o.blockDataCache.Unlock()
		}
	}()
	defer pCD.Unlock()

	if ok {
		dataBuffer = pCD.Lock()
		return dataBuffer.data, dataBuffer.err, dataBuffer.result
	}

	data, err := o.blockStorage.ReserveAndGet(block.Hash, true)
	dataBuffer.data = data
	dataBuffer.err = err
	if err != nil {
		dataBuffer.result = model.GET_BLOCK_FAILED
	} else {
		dataBuffer.result = model.GET_BLOCK_CACHED
	}

	return dataBuffer.data, dataBuffer.err, dataBuffer.result
}

// RequestBackgroundDownloadI implements model.BlockDataAccessI.
func (o *OfflineBlockDataAccess) RequestBackgroundDownloadI(filename string, size int64, modified time.Time) {
	// ignore
}

// ReserveAndSetI implements model.BlockDataAccessI.
func (o *OfflineBlockDataAccess) ReserveAndSetI(hash []byte, data []byte) {
	panic("OfflineBlockDataAccess::ReserveAndSetI(): should not be called for read only folder")
}

type OfflineDbFileSetWrite struct {
}

// Update implements model.DbFileSetWriteI.
func (o *OfflineDbFileSetWrite) Update(fs []protocol.FileInfo) {
	panic("OfflineDbFileSetWrite::Update(): should not be called for read only folder")
}

// UpdateOneLocalFileInfoLocalChangeDetected implements model.DbFileSetWriteI.
func (o *OfflineDbFileSetWrite) UpdateOneLocalFileInfoLocalChangeDetected(fi *protocol.FileInfo) {
	panic("OfflineDbFileSetWrite::UpdateOneLocalFileInfoLocalChangeDetected(): should not be called for read only folder")
}

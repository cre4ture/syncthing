// Copyright (C) 2021 The Syncthing Authors.
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this file,
// You can obtain one at https://mozilla.org/MPL/2.0/.

// Package virtualmount implements the `syncthing virtualcheck` subcommand.
package virtualcheck

import (
	"crypto/sha256"
	"errors"
	"fmt"

	progressbar "github.com/schollz/progressbar/v3"
	"github.com/syncthing/syncthing/cmd/syncthing/virtual"
	"github.com/syncthing/syncthing/lib/hashutil"
	"github.com/syncthing/syncthing/lib/logger"
	"github.com/syncthing/syncthing/lib/protocol"
	"github.com/syncthing/syncthing/lib/utils"
)

type CLI struct {
	DeviceID        string `help:"Device ID of the virtual folder, if it cannot be determined automatically"`
	FolderID        string `help:"Folder ID of the virtual folder, if it cannot be determined automatically"`
	URL             string `arg:"" required:"1" help:"URL to virtual folder. Excluding \":virtual:\""`
	ValidateData    bool   `help:"If 1 (default), content of data is fetched and checksum validated" default:"1"`
	RemoveCorrupted bool   `help:"If 1 (default is 0), data blocks that doesn't match the hash are deleted" default:"0"`
}

type BlockStatus int

const (
	BLOCK_STATUS_IN_VALIDATION BlockStatus = iota
	BLOCK_STATUS_GOOD
	BLOCK_STATUS_NOT_AVAILABLE
	BLOCK_STATUS_CORRUPTED
)

type BlockWithStatus struct {
	hashKey string
	status  BlockStatus
	size    uint64
	index   int
}

func (c *CLI) Run() error {

	if !c.ValidateData {
		println("Validation of data is turned OFF")
	} else {
		println("Validation of data is turned ON")
	}

	osa, err := virtual.NewOfflineDataAccess(
		c.URL, c.DeviceID, c.FolderID,
	)

	if err != nil {
		return err
	}

	snap, err := osa.FSetRO.SnapshotI()
	if err != nil {
		return err
	}

	dummy := struct{}{}

	checkedBlocks := utils.NewProtected(make(map[string]BlockStatus))
	corruptedFiles := make(map[string]struct{})
	corruptedBlocks := make(map[string]struct{})

	directoriesTodo := make(map[string]*protocol.FileInfo)
	filesTodo := make(map[string]*protocol.FileInfo)
	filesTodoSize := uint64(0)

	getFirstDirTodo := func() (prefix string, info *protocol.FileInfo) {
		for k, v := range directoriesTodo {
			delete(directoriesTodo, k)
			return k, v
		}

		return "", nil
	}

	iterateDir := func(prefix string) {
		println(fmt.Sprintf("Start iterating prefix: %v", prefix))
		snap.WithPrefixedGlobalTruncated(prefix, func(f protocol.FileInfo) bool {

			fullPath := f.Name
			if len(prefix) != 0 {
				fullPath = prefix + "/" + f.Name
			}
			println(fmt.Sprintf("Queue validation of %v - isDir: %v", fullPath, f.IsDirectory()))
			if f.IsDirectory() {
				directoriesTodo[fullPath] = &f
			} else {
				filesTodo[fullPath] = &f
				filesTodoSize += uint64(f.Size)
			}
			return true
		})
	}

	directoriesTodo[""] = &protocol.FileInfo{Type: protocol.FileInfoTypeDirectory}
	for {
		prefix, info := getFirstDirTodo()
		if info == nil {
			// done
			break
		}

		iterateDir(prefix)
	}

	println("Starting validation of blocks...")

	bar := progressbar.DefaultBytes(int64(filesTodoSize), "validating data hashes ...")
	defer func() {
		bar.Finish()
		errors := len(corruptedFiles)
		if errors != 0 {
			println(fmt.Sprintf("Validation FINISHED with %v ERRORS.", errors))
		} else {
			println("Validation FINISHED with SUCCESS. No errors found.")
		}
	}()

	handleCorruptedBlock := func(bi *protocol.BlockInfo) {
		if c.RemoveCorrupted {
			osa.BlockStorage.UncheckedDelete(bi.Hash)
			osa.BlockStorage.SetMeta("recheckBlocks/"+hashutil.HashToStringMapKey(bi.Hash), []byte{})
		}
	}

	for filePath, f := range filesTodo {
		bar.Describe(filePath)
		validatedBlocksChan := make(chan BlockWithStatus, 5)
		leases := utils.NewParallelLeases(5, "block validators")
		go func() {
			for i, bi := range f.Blocks {

				//logger.DefaultLogger.Infof("validating %v:%v - expected size: %v",
				//	filePath, i, bi.Size)

				func() {
					hashKey := hashutil.HashToStringMapKey(bi.Hash)
					var status BlockStatus = BLOCK_STATUS_NOT_AVAILABLE
					var isInCache bool = false
					func() {
						lockedMap := checkedBlocks.Lock()
						defer checkedBlocks.Unlock()
						status, isInCache = (*lockedMap)[hashKey]
						if !isInCache {
							status = BLOCK_STATUS_IN_VALIDATION
							(*lockedMap)[hashKey] = status
						}
					}()
					if isInCache {
						//logger.DefaultLogger.Infof("validating isInCache push before %v:%v - expected size: %v",
						//	filePath, i, bi.Size)

						validatedBlocksChan <- BlockWithStatus{
							hashKey: hashKey,
							status:  status,
							size:    uint64(bi.Size),
							index:   i,
						}

						//logger.DefaultLogger.Infof("validating isInCache push after %v:%v - expected size: %v",
						//	filePath, i, bi.Size)
					} else {
						leases.AsyncRunOne(fmt.Sprintf("block %s:%v", filePath, i), func() {

							//logger.DefaultLogger.Infof("validating parallel %v:%v - expected size: %v",
							//	filePath, i, bi.Size)

							data, err := osa.BlockStorage.UncheckedGet(bi.Hash, c.ValidateData)

							//logger.DefaultLogger.Infof("validating parallel 2 %v:%v - expected size: %v",
							//	filePath, i, bi.Size)

							if err != nil {
								status = BLOCK_STATUS_NOT_AVAILABLE
							} else {
								if c.ValidateData {
									currentHash := sha256.Sum256(data)
									currentHashKey := hashutil.HashToStringMapKey(currentHash[:])
									if currentHashKey == hashKey {
										status = BLOCK_STATUS_GOOD
									} else {
										status = BLOCK_STATUS_CORRUPTED
										logger.DefaultLogger.Warnf("validating %v:%v - hash mismatch: %s != %s, size: %v, expected size: %v",
											filePath, i, currentHashKey, hashKey, len(data), bi.Size)
										handleCorruptedBlock(&bi)
										corruptedBlocks[hashKey] = dummy
									}
								} else {
									status = BLOCK_STATUS_GOOD
								}
							}

							//logger.DefaultLogger.Infof("validating parallel push before %v:%v - expected size: %v",
							//	filePath, i, bi.Size)

							validatedBlocksChan <- BlockWithStatus{
								hashKey: hashKey,
								status:  status,
								size:    uint64(bi.Size),
								index:   i,
							}

							//logger.DefaultLogger.Infof("validating parallel push after %v:%v - expected size: %v",
							//	filePath, i, bi.Size)
						})

					}
				}()
			}

			go func() {
				leases.WaitAllFinished()
				//logger.DefaultLogger.Infof("finished all leases, close: %v", filePath)
				close(validatedBlocksChan)
			}()
		}()

		for result := range validatedBlocksChan {
			//logger.DefaultLogger.Infof("validating %v:%v", filePath, result.index)
			func() {
				lockedMap := checkedBlocks.Lock()
				defer checkedBlocks.Unlock()
				(*lockedMap)[result.hashKey] = result.status
			}()
			bar.Add64(int64(result.size))

			if (result.status != BLOCK_STATUS_GOOD) &&
				(result.status != BLOCK_STATUS_IN_VALIDATION) {
				logger.DefaultLogger.Warnf("validating %v:%+v", filePath, result)
				corruptedFiles[f.Name] = dummy
			}
		}
	}

	if len(corruptedFiles) > 0 {
		return errors.New("found corrupted files/blocks")
	}

	return nil
}

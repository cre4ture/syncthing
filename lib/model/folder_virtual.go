// Copyright (C) 2024 The Syncthing Authors.
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this file,
// You can obtain one at https://mozilla.org/MPL/2.0/.

package model

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"log"
	"os"
	"strings"
	"time"

	"github.com/syncthing/syncthing/lib/blockstorage"
	"github.com/syncthing/syncthing/lib/config"
	"github.com/syncthing/syncthing/lib/db"
	"github.com/syncthing/syncthing/lib/events"
	"github.com/syncthing/syncthing/lib/hashutil"
	"github.com/syncthing/syncthing/lib/ignore"
	"github.com/syncthing/syncthing/lib/logger"
	"github.com/syncthing/syncthing/lib/protocol"
	"github.com/syncthing/syncthing/lib/semaphore"
	"github.com/syncthing/syncthing/lib/sync"
	"github.com/syncthing/syncthing/lib/utils"
	"github.com/syncthing/syncthing/lib/versioner"
)

func init() {
	log.SetFlags(log.Lmicroseconds)
	log.Default().SetOutput(os.Stdout)
	log.Default().SetPrefix("TESTLOG ")
}

type InitialScanState int

const (
	INITIAL_SCAN_IDLE      InitialScanState = iota
	INITIAL_SCAN_RUNNING   InitialScanState = iota
	INITIAL_SCAN_COMPLETED InitialScanState = iota
)

type virtualFolderSyncthingService struct {
	*folderBase
	lifetimeCtxCancel context.CancelFunc // TODO: when to call this function?
	mountPath         string
	blockCache        blockstorage.HashBlockStorageI // block cache needs to be early accessible as it is used to read the encryption token. TODO: when to close it?
	running           *runningVirtualFolderSyncthingService
}

type runningVirtualFolderSyncthingService struct {
	parent            *virtualFolderSyncthingService
	blockCache        blockstorage.HashBlockStorageI // convenience shortcut to parent
	serviceRunningCtx context.Context
	deleteService     *blockstorage.AsyncCheckedDeleteService

	backgroundDownloadPending chan struct{}
	backgroundDownloadQueue   jobQueue

	initialScanState InitialScanState
	InitialScanDone  chan struct{}
}

type GetBlockDataResult int

const (
	GET_BLOCK_FAILED   GetBlockDataResult = iota
	GET_BLOCK_CACHED   GetBlockDataResult = iota
	GET_BLOCK_DOWNLOAD GetBlockDataResult = iota
)

func (vFSS *virtualFolderSyncthingService) GetBlockDataFromCacheOrDownload(
	snap *db.Snapshot,
	file protocol.FileInfo,
	block protocol.BlockInfo,
) ([]byte, bool, GetBlockDataResult) {
	data, ok := vFSS.blockCache.ReserveAndGet(block.Hash, true)
	if ok {
		return data, true, GET_BLOCK_CACHED
	}

	err := vFSS.pullBlockBase(func(blockData []byte) {
		data = blockData
	}, snap, file, block)

	if err != nil {
		return nil, false, GET_BLOCK_FAILED
	}

	vFSS.blockCache.ReserveAndSet(block.Hash, data)

	return data, true, GET_BLOCK_DOWNLOAD
}

func newVirtualFolder(
	model *model,
	fset *db.FileSet,
	ignores *ignore.Matcher,
	cfg config.FolderConfiguration,
	ver versioner.Versioner,
	evLogger events.Logger,
	ioLimiter *semaphore.Semaphore,
) service {

	folderBase := newFolderBase(cfg, evLogger, model, fset)

	blobUrl := ""
	virtual_descriptor, hasVirtualDescriptor := strings.CutPrefix(folderBase.Path, ":virtual:")
	if !hasVirtualDescriptor {
		panic("missing :virtual:")
	}

	parts := strings.Split(virtual_descriptor, ":mount_at:")
	blobUrl = parts[0]
	mountPath := ""
	if len(parts) >= 2 {
		//url := "s3://bucket-syncthing-uli-virtual-folder-test1/" + myDir
		mountPath = parts[1]
	}

	lifetimeCtx, lifetimeCtxCancel := context.WithCancel(context.Background())
	var blockCache blockstorage.HashBlockStorageI = blockstorage.NewGoCloudUrlStorage(
		lifetimeCtx, blobUrl, folderBase.model.id.String())

	if folderBase.Type.IsReceiveEncrypted() {
		blockCache = blockstorage.NewEncryptedHashBlockStorage(blockCache)
	}

	f := &virtualFolderSyncthingService{
		folderBase:        folderBase,
		lifetimeCtxCancel: lifetimeCtxCancel,
		mountPath:         mountPath,
		blockCache:        blockCache,
		running:           nil,
	}

	return f
}

func (vf *virtualFolderSyncthingService) runVirtualFolderServiceCoroutine(
	ctx context.Context,
	ping_pong_chan chan error, /* simulate coroutine */
) {

	initError := func() error { // coroutine

		if vf.running != nil {
			return errors.New("internal error. virtual folder already running")
		}

		serviceRunningCtx, lifetimeCtxCancel := context.WithCancel(ctx)
		defer lifetimeCtxCancel()

		deleteService := blockstorage.NewAsyncCheckedDeleteService(serviceRunningCtx, vf.blockCache)
		defer deleteService.Close()

		rvf := &runningVirtualFolderSyncthingService{
			parent:                    vf,
			blockCache:                vf.blockCache,
			serviceRunningCtx:         serviceRunningCtx,
			deleteService:             deleteService,
			backgroundDownloadPending: make(chan struct{}, 1),
			backgroundDownloadQueue:   *newJobQueue(),
			initialScanState:          INITIAL_SCAN_IDLE,
			InitialScanDone:           make(chan struct{}, 1),
		}
		vf.running = rvf

		backgroundDownloadTasks := 5
		backgroundDownloadTaskWaitGroup := sync.NewWaitGroup()
		defer backgroundDownloadTaskWaitGroup.Wait()
		for i := 0; i < backgroundDownloadTasks; i++ {
			backgroundDownloadTaskWaitGroup.Add(1)
			go func() {
				defer backgroundDownloadTaskWaitGroup.Done()
				vf.running.serve_backgroundDownloadTask()
			}()
		}

		if vf.mountPath != "" {
			stVF := &syncthingVirtualFolderFuseAdapter{
				vFSS:           vf,
				folderID:       vf.ID,
				model:          vf.model,
				fset:           vf.fset,
				ino_mu:         sync.NewMutex(),
				next_ino_nr:    1,
				ino_mapping:    make(map[string]uint64),
				directories_mu: sync.NewMutex(),
				directories:    make(map[string]*TreeEntry),
			}
			mount, err := NewVirtualFolderMount(vf.mountPath, vf.ID, vf.Label, stVF)
			if err != nil {
				return err
			}

			defer func() {
				mount.Close()
			}()
		}

		if rvf.initialScanState == INITIAL_SCAN_IDLE {
			rvf.initialScanState = INITIAL_SCAN_RUNNING
			// TODO: rvf.Pull_x(ctx, PullOptions{false, true})
			rvf.initialScanState = INITIAL_SCAN_COMPLETED
			close(rvf.InitialScanDone)
			rvf.pullOrScan_x(ctx, PullOptions{true, false})
		}

		// unblock caller after successful init
		logger.DefaultLogger.Infof("Service coroutine running - unblock caller")
		ping_pong_chan <- nil

		logger.DefaultLogger.Infof("Service coroutine running - wait for shutdown signal")
		<-ping_pong_chan // wait for shutdown signal
		logger.DefaultLogger.Infof("Service coroutine running - shutdown signal received")

		return nil // all prepared defers needed for shutdown will be handled properly here
	}()

	ping_pong_chan <- initError // signal failed init (!= nil) or finalized shutdown (== nil)
	logger.DefaultLogger.Infof("Service coroutine shutdown - send DONE signal")
}

func (f *runningVirtualFolderSyncthingService) RequestBackgroundDownload(filename string, size int64, modified time.Time, fn jobQueueProgressFn) {
	wasNew := f.backgroundDownloadQueue.PushIfNew(filename, size, modified, fn)
	if !wasNew {
		fn(size, true)
		return
	}

	select {
	case f.backgroundDownloadPending <- struct{}{}:
	default:
	}
}

func (f *runningVirtualFolderSyncthingService) serve_backgroundDownloadTask() {
	for {
		select {
		case <-f.backgroundDownloadPending:
		case <-f.serviceRunningCtx.Done():
			return
		}

		for job, ok := f.backgroundDownloadQueue.Pop(); ok; job, ok = f.backgroundDownloadQueue.Pop() {
			func() {
				createVirtualFolderFilePullerAndPull(f, job)
			}()
		}
	}
}

// model.service API
func (f *virtualFolderSyncthingService) Serve(ctx context.Context) error {
	f.ctx = ctx // legacy compatibility

	f.model.foldersRunning.Add(1)
	defer f.model.foldersRunning.Add(-1)

	defer l.Infof("vf.Serve exits")

	co_chan := make(chan error) // un-buffered!
	go f.runVirtualFolderServiceCoroutine(ctx, co_chan)
	initError := <-co_chan
	if initError != nil {
		return initError
	} // else the service is initialized

	defer func() {
		// release service coroutine:
		logger.DefaultLogger.Infof("release service coroutine ...")
		co_chan <- nil
		logger.DefaultLogger.Infof("wait for stop of service coroutine ...")
		<-co_chan
		logger.DefaultLogger.Infof("service coroutine STOPPED")
	}()

	for {
		logger.DefaultLogger.Infof("virtualFolderServe: waiting for signal to process ...")
		select {
		case <-f.ctx.Done():
			close(f.done)
			l.Debugf("Serve: case <-ctx.Done()")
			return nil

		case req := <-f.doInSyncChan:
			l.Debugln(f, "Running something due to request")
			err := req.fn()
			req.err <- err
			continue

		case <-f.pullScheduled: // TODO: replace with "doInSyncChan"
			logger.DefaultLogger.Infof("virtualFolderServe: case <-f.pullScheduled")
			l.Debugf("Serve: f.pullAllMissing(false) - START")
			err := f.running.pullOrScan_x(ctx, PullOptions{true, false})
			l.Debugf("Serve: f.pullAllMissing(false) - DONE. Err: %v", err)
			logger.DefaultLogger.Infof("virtualFolderServe: case <-f.pullScheduled - 2")
			continue
		}
	}
}

func (f *virtualFolderSyncthingService) Override()                 {} // model.service API
func (f *virtualFolderSyncthingService) Revert()                   {} // model.service API
func (f *virtualFolderSyncthingService) DelayScan(d time.Duration) {} // model.service API

// model.service API
func (f *virtualFolderSyncthingService) ScheduleScan() {
	logger.DefaultLogger.Infof("ScheduleScan - pull_x")
	f.doInSync(func() error {
		if f.running == nil {
			return nil // ignore request
		}
		err := f.running.pullOrScan_x(f.ctx, PullOptions{false, true})
		logger.DefaultLogger.Infof("ScheduleScan - pull_x - DONE. Err: %v", err)
		return err
	})
}

// model.service API
func (f *virtualFolderSyncthingService) Jobs(page, per_page int) ([]string, []string, int) {
	if f.running == nil {
		return []string{}, []string{}, 0
	}
	return f.running.backgroundDownloadQueue.Jobs(page, per_page)
}

// model.service API
func (f *virtualFolderSyncthingService) BringToFront(filename string) {
	if f.running == nil {
		return
	}

	f.running.backgroundDownloadQueue.BringToFront(filename)
}

// model.service API
func (vf *virtualFolderSyncthingService) Scan(subs []string) error {
	if vf.running == nil {
		return nil
	}

	logger.DefaultLogger.Infof("Scan(%+v) - pull_x", subs)
	return vf.running.pullOrScan_x_doInSync(vf.ctx, PullOptions{false, true})
}

type PullOptions struct {
	onlyMissing bool
	onlyCheck   bool
}

func (f *runningVirtualFolderSyncthingService) pullOrScan_x_doInSync(ctx context.Context, opts PullOptions) error {
	logger.DefaultLogger.Infof("request pullOrScan_x_doInSync - %+v", opts)
	return f.parent.doInSync(func() error {
		logger.DefaultLogger.Infof("execute pullOrScan_x_doInSync - %+v", opts)
		return f.pullOrScan_x(ctx, opts)
	})
}

func (vf *runningVirtualFolderSyncthingService) pullOrScan_x(ctx context.Context, opts PullOptions) error {
	defer logger.DefaultLogger.Infof("pull_x END z - opts: %+v", opts)
	snap, err := vf.parent.fset.Snapshot()
	if err != nil {
		return err
	}
	defer logger.DefaultLogger.Infof("pull_x END snap - opts: %+v", opts)
	defer snap.Release()

	if opts.onlyCheck {
		vf.parent.setState(FolderScanning)
	} else {
		vf.parent.setState(FolderSyncing)
	}
	defer logger.DefaultLogger.Infof("pull_x END setState - opts: %+v", opts)
	defer vf.parent.setState(FolderIdle)

	logger.DefaultLogger.Infof("pull_x START - opts: %+v", opts)
	defer logger.DefaultLogger.Infof("pull_x END a")

	checkMap := blockstorage.HashBlockStateMap(nil)
	if opts.onlyCheck {
		func() {
			asyncNotifier := utils.NewAsyncProgressNotifier(vf.serviceRunningCtx)
			asyncNotifier.StartAsyncProgressNotification(
				logger.DefaultLogger,
				uint64(255), // use first hash byte as progress indicator. This works as storage is sorted.
				uint(5),
				vf.parent.evLogger,
				vf.parent.folderID,
				make([]string, 0),
				nil)
			defer logger.DefaultLogger.Infof("pull_x END1 asyncNotifier.Stop()")
			defer asyncNotifier.Stop()

			checkMap = vf.blockCache.GetBlockHashesCache(ctx, func(count int, currentHash []byte) {
				if len(currentHash) < 1 {
					log.Panicf("Scan progress: Length of currentHash is zero! %v", currentHash)
				}
				progressByte := uint64(currentHash[0])
				// logger.DefaultLogger.Infof("GetBlockHashesCache - progress: %v, byte: 0x%x", count, progressByte)
				asyncNotifier.Progress.UpdateTotal(progressByte)
			})
		}()
	}

	jobs := newJobQueue()
	totalBytes := uint64(0)
	{
		prepareFn := func(f protocol.FileIntf) bool {
			totalBytes += uint64(f.FileSize())
			jobs.Push(f.FileName(), f.FileSize(), f.ModTime())
			return true
		}

		if opts.onlyMissing {
			snap.WithNeedTruncated(protocol.LocalDeviceID, prepareFn)
		} else {
			if opts.onlyCheck {
				snap.WithHaveTruncated(protocol.LocalDeviceID, prepareFn)
			} else {
				snap.WithGlobalTruncated(prepareFn)
			}
		}

		jobs.SortAccordingToConfig(vf.parent.Order)
	}

	asyncNotifier := utils.NewAsyncProgressNotifier(vf.serviceRunningCtx)
	asyncNotifier.StartAsyncProgressNotification(
		logger.DefaultLogger, totalBytes, uint(1), vf.parent.evLogger, vf.parent.folderID, make([]string, 0), nil)
	defer logger.DefaultLogger.Infof("pull_x END asyncNotifier.Stop()")
	defer asyncNotifier.Stop()
	defer logger.DefaultLogger.Infof("pull_x END b")

	leases := utils.NewParallelLeases(60, 1)
	defer leases.WaitAllDone()

	isAbortOrErr := false
	pullF := func(f protocol.FileIntf) bool /* true to continue */ {
		myFileSize := f.FileSize()
		leases.AsyncRunOneWithDoneFn(func(doneFn func()) {
			doScan := checkMap != nil
			actionName := "Pull"
			if doScan {
				actionName = "Scan"
			}
			if !doScan {
				logger.DefaultLogger.Infof("%v ONE - START, size: %v", actionName, myFileSize)
			}
			progressFn := func(deltaBytes int64, done bool) {
				asyncNotifier.Progress.Update(deltaBytes)
				if done {
					doneFn()
					if !doScan {
						logger.DefaultLogger.Infof("%v ONE - DONE, size: %v", actionName, myFileSize)
					}
				}
			}
			if checkMap != nil {
				vf.scanOne(snap, f, checkMap, progressFn)
			} else {
				vf.pullOne(snap, f, false, progressFn)
			}
		})

		select {
		case <-vf.serviceRunningCtx.Done():
			logger.DefaultLogger.Infof("pull ONE - stop continue")
			isAbortOrErr = true
			return false
		default:
			return true
		}
	}

	if isAbortOrErr {
		return nil
	}

	for job, ok := jobs.Pop(); ok; job, ok = jobs.Pop() {
		fi, ok := snap.GetGlobalTruncated(job.name)
		if ok {
			good := pullF(fi)
			if !good {
				isAbortOrErr = true
				break
			}
		}
	}

	if isAbortOrErr {
		return nil
	}

	if checkMap != nil {
		vf.cleanupUnneededReservations(checkMap)
		vf.parent.ScanCompleted()
	}

	return nil
}

func (vf *runningVirtualFolderSyncthingService) cleanupUnneededReservations(checkMap blockstorage.HashBlockStateMap) error {
	snap, err := vf.parent.fset.Snapshot()
	if err != nil {
		return err
	}
	defer logger.DefaultLogger.Infof("cleanupUnneeded END snap")
	defer snap.Release()

	dummyValue := struct{}{}
	usedBlockHashes := map[string]struct{}{}
	snap.WithHave(protocol.LocalDeviceID, func(f protocol.FileIntf) bool {
		fi, ok := snap.Get(protocol.LocalDeviceID, f.FileName())
		if !ok {
			log.Panicf("cleanupUnneeded: inconsistent snapshot! %v", f.FileName())
		}
		for _, bi := range fi.Blocks {
			usedBlockHashes[hashutil.HashToStringMapKey(bi.Hash)] = dummyValue
		}
		return true
	})

	for hash, state := range checkMap {
		if state.IsAvailableAndFree() {
			byteHash := hashutil.StringMapKeyToHashNoError(hash)
			vf.deleteService.RequestCheckedDelete(byteHash)
		} else if state.IsAvailableAndReservedByMe() {
			_, stillNeeded := usedBlockHashes[hash]
			if !stillNeeded {
				byteHash := hashutil.StringMapKeyToHashNoError(hash)
				vf.blockCache.DeleteReservation(byteHash)
				vf.deleteService.RequestCheckedDelete(byteHash)
			}
		}
	}

	return nil
}

func (vf *runningVirtualFolderSyncthingService) pullOne(
	snap *db.Snapshot, f protocol.FileIntf, synchronous bool, fn jobQueueProgressFn,
) {

	vf.parent.evLogger.Log(events.ItemStarted, map[string]string{
		"folder": vf.parent.folderID,
		"item":   f.FileName(),
		"type":   "file",
		"action": "update",
	})

	err := error(nil)

	fn2 := func(deltaBytes int64, done bool) {
		fn(deltaBytes, done)

		if done {
			vf.parent.evLogger.Log(events.ItemFinished, map[string]interface{}{
				"folder": vf.parent.folderID,
				"item":   f.FileName(),
				"error":  events.Error(err),
				"type":   "dir",
				"action": "update",
			})
		}
	}

	if f.IsDirectory() {
		// no work to do for directories. directly take over:
		fi, ok := snap.GetGlobal(f.FileName())
		if ok {
			vf.parent.fset.UpdateOne(protocol.LocalDeviceID, &fi)
			vf.parent.ReceivedFile(fi.Name, fi.IsDeleted())
		}
		fn2(f.FileSize(), true)
	} else {
		vf.RequestBackgroundDownload(f.FileName(), f.FileSize(), f.ModTime(), fn2)
	}
}

func (vf *runningVirtualFolderSyncthingService) scanOne(snap *db.Snapshot, f protocol.FileIntf, checkMap blockstorage.HashBlockStateMap, fn jobQueueProgressFn) {

	if f.IsDirectory() {
		// no work to do for directories.
		fn(f.FileSize(), true)
	} else {
		func() {
			defer fn(0, true)

			fi, ok := snap.Get(protocol.LocalDeviceID, f.FileName())
			if !ok {
				return
			}

			all_ok := true
			for _, bi := range fi.Blocks {
				//logger.DefaultLogger.Debugf("synchronous NEW check(%v) block info #%v: %+v", onlyCheck, i, bi, hashutil.HashToStringMapKey(bi.Hash))
				blockState, inMap := checkMap[hashutil.HashToStringMapKey(bi.Hash)]
				ok = inMap
				if inMap && (!blockState.IsAvailableAndReservedByMe()) {
					// block is there but not hold, add missing hold - checking again for existence as in unhold state it could have been removed meanwhile
					_, reservationOk := vf.parent.blockCache.ReserveAndGet(bi.Hash, false)
					ok = reservationOk
				}
				if !ok {
					logger.DefaultLogger.Debugf("synchronous cache-map based check(%v) failed for block info #%v: %+v, inMap: %v",
						f.FileName(), bi.Offset, hashutil.HashToStringMapKey(bi.Hash), inMap)
				}
				all_ok = all_ok && ok

				fn(int64(bi.Size), false)

				if utils.IsDone(vf.serviceRunningCtx) {
					return
				}
			}

			if !all_ok {
				//logger.DefaultLogger.Debugf("synchronous check block info result: incomplete, file: %s", fi.Name)
				// Revert means to throw away our local changes. We reset the
				// version to the empty vector, which is strictly older than any
				// other existing version. It is not in conflict with anything,
				// either, so we will not create a conflict copy of our local
				// changes.
				fi.Version = protocol.Vector{}
				vf.parent.fset.UpdateOne(protocol.LocalDeviceID, &fi)
			}

		}()
	}
}

func (f *virtualFolderSyncthingService) Errors() []FileError             { return []FileError{} }
func (f *virtualFolderSyncthingService) WatchError() error               { return nil }
func (f *virtualFolderSyncthingService) ScheduleForceRescan(path string) {}

var _ = (virtualFolderServiceI)((*virtualFolderSyncthingService)(nil))

// API to model
func (vf *virtualFolderSyncthingService) GetHashBlockData(hash []byte, response_data []byte) (int, error) {
	data, ok := vf.blockCache.ReserveAndGet(hash, true)
	if !ok {
		return 0, protocol.ErrNoSuchFile
	}
	n := copy(response_data, data)
	return n, nil
}

func (f *virtualFolderSyncthingService) ReadEncryptionToken() ([]byte, error) {
	data, ok := f.blockCache.GetMeta(config.EncryptionTokenName)
	if !ok {
		return nil, fs.ErrNotExist
	}
	dataBuf := bytes.NewBuffer(data)
	var stored storedEncryptionToken
	if err := json.NewDecoder(dataBuf).Decode(&stored); err != nil {
		return nil, err
	}
	return stored.Token, nil
}
func (f *virtualFolderSyncthingService) WriteEncryptionToken(token []byte) error {
	data := bytes.Buffer{}
	err := json.NewEncoder(&data).Encode(storedEncryptionToken{
		FolderID: f.ID,
		Token:    token,
	})
	if err != nil {
		return err
	}
	f.blockCache.SetMeta(config.EncryptionTokenName, data.Bytes())
	return nil
}

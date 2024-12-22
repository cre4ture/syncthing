package model

import (
	"context"
	"fmt"
	"sync/atomic"
	"time"

	"github.com/syncthing/syncthing/lib/blockstorage"
	"github.com/syncthing/syncthing/lib/db"
	"github.com/syncthing/syncthing/lib/events"
	"github.com/syncthing/syncthing/lib/protocol"
	"github.com/syncthing/syncthing/lib/sync"
	"github.com/syncthing/syncthing/lib/utils"
)

type VirtualFolderFilePuller struct {
	sharedPullerStateBase
	job                     *jobQueueEntry
	snap                    *db.Snapshot
	folderService           *virtualFolderSyncthingService
	backgroundDownloadQueue *jobQueue
	fset                    *db.FileSet
	ctx                     context.Context
}

func createVirtualFolderFilePullerAndPull(f *runningVirtualFolderSyncthingService, job *jobQueueEntry) {
	defer f.backgroundDownloadQueue.Done(job.name)

	now := time.Now()

	snap, err := f.parent.fset.Snapshot()
	if err != nil {
		return
	}
	defer snap.Release()

	fi, ok := snap.GetGlobal(job.name)
	if !ok {
		return
	}

	instance := &VirtualFolderFilePuller{
		sharedPullerStateBase: sharedPullerStateBase{
			created:          now,
			file:             fi,
			folder:           f.parent.folderID,
			reused:           0,
			copyTotal:        len(fi.Blocks),
			copyNeeded:       len(fi.Blocks),
			updated:          now,
			available:        make([]int, 0),
			availableUpdated: now,
			mut:              sync.NewRWMutex(),
		},
		job:                     job,
		snap:                    snap,
		folderService:           f.parent,
		backgroundDownloadQueue: f.backgroundDownloadQueue,
		fset:                    f.parent.fset,
		ctx:                     f.serviceRunningCtx,
	}

	f.parent.model.progressEmitter.Register(instance)
	defer f.parent.model.progressEmitter.Deregister(instance)

	instance.doPull()
}

func (f *VirtualFolderFilePuller) doPull() {

	all_ok := atomic.Bool{}
	all_ok.Store(true)
	func() {
		leases := utils.NewParallelLeases(10, "vPuller.doPull")
		defer leases.AbortAndWait()

		for i, bi := range f.file.Blocks {
			//logger.DefaultLogger.Debugf("check block info #%v: %+v", i, bi)

			leases.AsyncRunOne(fmt.Sprintf("%v:%v", f.job.name, i), func() {

				err := utils.AbortableTimeDelayedRetry(f.ctx, 6, time.Minute, func(tryNr uint) error {
					_, err, variant := f.folderService.GetBlockDataFromCacheOrDownload(
						&f.file, bi, func() {
							f.pullStarted()
						})

					if err != nil {
						// trigger retry
						return err
					}

					switch variant {
					case GET_BLOCK_CACHED:
						f.copiedFromElsewhere(bi.Size)
						f.copyDone(bi)
					case GET_BLOCK_DOWNLOAD:
						f.pullDone(bi)
					case GET_BLOCK_FAILED:
						err = blockstorage.ErrNotAvailable
					}

					f.job.progressCb(int64(bi.Size), false)
					return err
				})

				if err != nil {
					all_ok.Store(false)
				}
			})

			if utils.IsDone(f.ctx) {
				return
			}
		}
	}()

	if utils.IsDone(f.ctx) {
		return
	}

	if !all_ok.Load() {
		f.folderService.evLogger.Log(events.Failure, fmt.Sprintf("failed to pull all blocks for: %v", f.job))
		return
	}

	// note: this also handles deletes
	// as the deleted file will not have any blocks,
	// the loop before is just skipped
	f.folderService.updateOneLocalFileInfo(&f.file, events.RemoteChangeDetected)

	seq := f.fset.Sequence(protocol.LocalDeviceID)
	f.folderService.evLogger.Log(events.LocalIndexUpdated, map[string]interface{}{
		"folder":    f.folderService.ID,
		"items":     1,
		"filenames": append([]string(nil), f.file.Name),
		"sequence":  seq,
		"version":   seq, // legacy for sequence
	})
}

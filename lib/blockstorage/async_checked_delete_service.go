package blockstorage

import (
	"context"
	"sync"
	"time"

	"github.com/syncthing/syncthing/lib/hashutil"
	"github.com/syncthing/syncthing/lib/logger"
	"github.com/syncthing/syncthing/lib/utils"
)

type AsyncCheckedDeleteService struct {
	// io.Closer
	ctx            context.Context
	cancel         context.CancelFunc
	hbs            HashBlockStorageI
	serviceDone    sync.WaitGroup
	todoList       chan []byte // hashes
	pendingDeletes chan PendingDelete
}

type PendingDelete struct {
	time time.Time
	hash []byte
}

const MAX_PENDING_DELETES = 10 // this needs to be limited to keep timing constrains of pending deletes
const TIME_CONSTANT_BASE = time.Minute
const TIME_CONSTANT = TIME_CONSTANT_BASE / 3 // grace period + deletion window + another grace period

func NewAsyncCheckedDeleteService(ctx context.Context, hbs HashBlockStorageI) *AsyncCheckedDeleteService {
	myCtx, cancel := context.WithCancel(ctx)
	instance := &AsyncCheckedDeleteService{
		ctx:            myCtx,
		cancel:         cancel,
		hbs:            hbs,
		serviceDone:    sync.WaitGroup{},
		todoList:       make(chan []byte, 10000),
		pendingDeletes: make(chan PendingDelete, MAX_PENDING_DELETES),
	}

	instance.serviceDone.Add(1)
	go func() {
		defer instance.serviceDone.Done()
		instance.serveTodoList()
	}()
	instance.serviceDone.Add(1)
	go func() {
		defer instance.serviceDone.Done()
		instance.servePendingList()
	}()

	return instance
}

func (ds *AsyncCheckedDeleteService) Close() error {
	ds.cancel()
	ds.serviceDone.Wait()
	return nil
}

func (ds *AsyncCheckedDeleteService) RequestCheckedDelete(hash []byte) {
	ds.todoList <- hash
}

func (ds *AsyncCheckedDeleteService) serveTodoList() {
	defer close(ds.pendingDeletes)
	for {
		select {
		case <-ds.ctx.Done():
			return
		case currentJob := <-ds.todoList:
			err := utils.AbortableTimeDelayedRetry(ds.ctx, 6, time.Minute, func(tryNr uint) error {
				// first check again, as the job might have been waiting in TODO list for a while
				currentState, err := ds.hbs.GetBlockHashState(currentJob)
				if err != nil {
					return err
				}

				if currentState.IsReservedBySomeone() {
					// logger.DefaultLogger.Infof("skip delete as still reserved: %v, state: %v",
					// 	hashutil.HashToStringMapKey(currentJob), currentState)
					return nil
				}

				err = ds.hbs.AnnounceDelete(currentJob)
				if err != nil {
					return err
				}

				pendingDeleteJob := PendingDelete{
					time: time.Now().Add(TIME_CONSTANT),
					hash: currentJob,
				}
				select {
				case <-ds.ctx.Done():
					_ = ds.hbs.DeAnnounceDelete(currentJob)
					return context.Canceled
				case ds.pendingDeletes <- pendingDeleteJob:
				}
				// logger.DefaultLogger.Infof("announced delete for: %v", hashutil.HashToStringMapKey(currentJob))

				return nil
			})

			if err != nil {
				logger.DefaultLogger.Warnf("deletion of %v failed to to persistent errors", hashutil.HashToStringMapKey(currentJob))
			}
		}
	}
}

func (ds *AsyncCheckedDeleteService) servePendingList() {
	for {
		select {
		case <-ds.ctx.Done():
			// abort pending operations:
			for currentJob := range ds.pendingDeletes {
				// clear all pending delete markers immediately
				ds.hbs.DeAnnounceDelete(currentJob.hash)
			}
			return
		case currentJob := <-ds.pendingDeletes:
			func() {
				defer ds.hbs.DeAnnounceDelete(currentJob.hash)

				timeTillDue := time.Until(currentJob.time)
				err := utils.AbortableTimeSleep(ds.ctx, timeTillDue)
				if err != nil {
					return // aborted
				}

				// check again, abort if state changed, delete when state same and we are still in time
				currentState, err := ds.hbs.GetBlockHashState(currentJob.hash)
				if err != nil {
					return // error, abort
				}

				if currentState.IsReservedBySomeone() {
					// if state changed in grace period, this needs to be accepted
					// logger.DefaultLogger.Infof("abort delete due to new reservation for: %v, state: %v",
					//	hashutil.HashToStringMapKey(currentJob.hash), currentState)
					return
				}

				timeSinceDue := time.Since(currentJob.time)
				if timeSinceDue >= TIME_CONSTANT {
					// if we where delayed by whatever being busy, we can't guarantee safe deletion
					return
				}

				// we are still in time window for deletion, no state changed, finally delete
				err = ds.hbs.UncheckedDelete(currentJob.hash)
				if err != nil {
					logger.DefaultLogger.Warnf("delete for: %v failed. Err: %+v", hashutil.HashToStringMapKey(currentJob.hash), err)
				}
				// logger.DefaultLogger.Infof("delete done for: %v",
				// 	hashutil.HashToStringMapKey(currentJob.hash))
			}()
		}
	}
}

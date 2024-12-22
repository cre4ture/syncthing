// Copyright (C) 2014 The Syncthing Authors.
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this file,
// You can obtain one at https://mozilla.org/MPL/2.0/.

package model

import (
	"strings"
	"time"

	"github.com/samber/lo"
	"github.com/syncthing/syncthing/lib/config"
	"github.com/syncthing/syncthing/lib/utils"
)

type jobQueue struct {
	queued   *utils.SortableChannel[*jobQueueEntry]
	progress *utils.SortableChannel[*jobQueueEntry]
}

type jobQueueProgressFn func(deltaBytes int64, done bool)

type jobQueueEntry struct {
	name       string
	size       int64
	modified   time.Time
	progressCb jobQueueProgressFn
}

func newJobQueue() *jobQueue {
	return &jobQueue{
		queued:   utils.NewSortableChannel[*jobQueueEntry](),
		progress: utils.NewSortableChannel[*jobQueueEntry](),
	}
}

func (e *jobQueueEntry) abort() {
	if e.progressCb != nil {
		e.progressCb(0, true)
		e.progressCb = nil
	}
}

func nameComparer(jqe1, jqe2 *jobQueueEntry) int {
	return strings.Compare(jqe1.name, jqe2.name)
}

func sizeComparer(jqe1, jqe2 *jobQueueEntry) int {
	return int(jqe1.size) - int(jqe2.size)
}

func inverseSizeComparer(jqe1, jqe2 *jobQueueEntry) int {
	return sizeComparer(jqe2, jqe1)
}

func modifiedDateComparer(jqe1, jqe2 *jobQueueEntry) int {
	return jqe1.modified.Compare(jqe2.modified)
}

func inverseModifiedDateComparer(jqe1, jqe2 *jobQueueEntry) int {
	return modifiedDateComparer(jqe2, jqe1)
}

func (q *jobQueue) PushIfNew(file string, size int64, modified time.Time, fn jobQueueProgressFn) bool {
	return q.queued.PushIfNew(&jobQueueEntry{file, size, modified, fn}, nameComparer)
}

func (q *jobQueue) Push(file string, size int64, modified time.Time) {
	q.queued.Push(&jobQueueEntry{file, size, modified, nil})
}

func (q *jobQueue) Pop() (*jobQueueEntry, bool) {
	job, ok := q.queued.Pop()
	if ok {
		q.progress.Push(job)
	}

	return job, ok
}

func (q *jobQueue) tryPopWithTimeout(duration time.Duration) (*jobQueueEntry, error) {
	var job *jobQueueEntry
	ok, err := q.queued.TryPopWithTimeout(&job, duration)
	if err != nil {
		return nil, err
	}

	if !ok {
		return nil, nil
	}

	q.progress.Push(job)

	return job, nil
}

func (q *jobQueue) BringToFront(filename string) {
	q.queued.BringToFront(&jobQueueEntry{name: filename}, nameComparer)
}

func (q *jobQueue) Done(file string) {
	removed := q.progress.Remove(&jobQueueEntry{name: file}, nameComparer)
	if removed != nil {
		(*removed).abort()
	}
}

// Jobs returns a paginated list of file currently being pulled and files queued
// to be pulled. It also returns how many items were skipped.
func (q *jobQueue) Jobs(page, perpage uint) ([]string, []string, uint) {
	if page < 1 {
		return nil, nil, 0
	}
	pageEndOffset := int(page * perpage)
	pageSize := int(perpage)

	progressPage, progressSkipped := q.progress.GetPage(uint(pageEndOffset-pageSize), uint(pageSize))
	progressPageNamesOnly := lo.Map(progressPage, func(j *jobQueueEntry, i int) string { return j.name })
	pageSize -= len(progressPage)
	pageEndOffset -= int(progressSkipped) + len(progressPage)
	if (pageSize <= 0) || (pageEndOffset-pageSize < 0) {
		return progressPageNamesOnly, nil, progressSkipped
	}
	queuedPage, queuedSkipped := q.queued.GetPage(uint(pageEndOffset-pageSize), uint(pageSize))
	queuedPageNamesOnly := lo.Map(queuedPage, func(j *jobQueueEntry, i int) string { return j.name })

	return progressPageNamesOnly, queuedPageNamesOnly, progressSkipped + queuedSkipped
}

func (q *jobQueue) Shuffle() {
	q.queued.Shuffle()
}

func (q *jobQueue) lenQueued() int {
	return q.queued.LenQueued()
}

func (q *jobQueue) lenProgress() int {
	return q.progress.LenQueued()
}

func (q *jobQueue) Close() {
	q.queued.Close()
	q.progress.Close()
}

func (q *jobQueue) SortAccordingToConfig(Order config.PullOrder) {
	switch Order {
	case config.PullOrderRandom:
		q.Shuffle()
	case config.PullOrderAlphabetic:
	// The queue is already in alphabetic order.
	case config.PullOrderSmallestFirst:
		q.SortSmallestFirst()
	case config.PullOrderLargestFirst:
		q.SortLargestFirst()
	case config.PullOrderOldestFirst:
		q.SortOldestFirst()
	case config.PullOrderNewestFirst:
		q.SortNewestFirst()
	}
}

func (q *jobQueue) SortSmallestFirst() {
	q.queued.Sort(sizeComparer)
}

func (q *jobQueue) SortLargestFirst() {
	q.queued.Sort(inverseSizeComparer)
}

func (q *jobQueue) SortOldestFirst() {
	q.queued.Sort(modifiedDateComparer)
}

func (q *jobQueue) SortNewestFirst() {
	q.queued.Sort(inverseModifiedDateComparer)
}

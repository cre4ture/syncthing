package utils

import (
	"context"
	"sync/atomic"
	"time"

	"github.com/rcrowley/go-metrics"
	"github.com/syncthing/syncthing/lib/events"
	"github.com/syncthing/syncthing/lib/ignore"
	"github.com/syncthing/syncthing/lib/logger"
)

// A byteCounter gets bytes added to it via Update() and then provides the
// Total() and one minute moving average Rate() in bytes per second.
type byteCounter struct {
	total atomic.Uint64
	metrics.EWMA
	stop chan struct{}
}

func NewByteCounter() *byteCounter {
	c := &byteCounter{
		EWMA: metrics.NewEWMA1(), // a one minute exponentially weighted moving average
		stop: make(chan struct{}),
	}
	go c.ticker()
	return c
}

func (c *byteCounter) ticker() {
	// The metrics.EWMA expects clock ticks every five seconds in order to
	// decay the average properly.
	t := time.NewTicker(5 * time.Second)
	for {
		select {
		case <-t.C:
			c.Tick()
		case <-c.stop:
			t.Stop()
			return
		}
	}
}

func (c *byteCounter) Update(bytes int64) {
	c.total.Add(uint64(bytes))
	c.EWMA.Update(bytes)
}

func (c *byteCounter) UpdateTotal(newTotal uint64) {
	currentTotal := c.total.Load()
	if newTotal > currentTotal {
		if c.total.CompareAndSwap(currentTotal, newTotal) {
			delta := newTotal - c.total.Load()
			c.EWMA.Update(int64(delta))
		}
	}
}

func (c *byteCounter) Total() uint64 { return c.total.Load() }

func (c *byteCounter) Close() {
	close(c.stop)
}

type AsyncProgressNotifier struct {
	ctx      context.Context
	Done     chan struct{}
	Progress *byteCounter
}

func NewAsyncProgressNotifier(ctx context.Context) *AsyncProgressNotifier {
	return &AsyncProgressNotifier{
		ctx:      ctx,
		Done:     make(chan struct{}),
		Progress: NewByteCounter(),
	}
}

func (apn *AsyncProgressNotifier) Stop() {
	close(apn.Done)
}

func (apn *AsyncProgressNotifier) StartAsyncProgressNotification(
	l logger.Logger,
	total uint64,
	ProgressTickIntervalS uint,
	w events.Logger,
	folderID string,
	Subs []string,
	matcher *ignore.Matcher,
) {
	if total == 0 {
		// avoid crashes (divide by zero) when noting is to be done!
		total = 1
	}

	// A routine which actually emits the FolderScanProgress events
	// every w.ProgressTicker ticks, until the hasher routines terminate.
	go func() {
		defer apn.Progress.Close()

		emitProgressEvent := func() {
			current := apn.Progress.Total()
			rate := apn.Progress.Rate()
			if current > total {
				total = current + (current / 2)
			}
			l.Debugf("%v: Walk %s %s current progress %d/%d at %.01f MiB/s (%d%%)", w, folderID, Subs, current, total, rate/1024/1024, current*100/total)
			w.Log(events.FolderScanProgress, map[string]interface{}{
				"folder":  folderID,
				"current": current,
				"total":   total,
				"rate":    rate, // bytes per second
			})
		}

		ticker := time.NewTicker(time.Duration(ProgressTickIntervalS) * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-apn.Done:
				emitProgressEvent()
				l.Debugln(w, "Walk progress done", folderID, Subs, matcher)
				return
			case <-ticker.C:
				emitProgressEvent()
			case <-apn.ctx.Done():
				return
			}
		}
	}()
}

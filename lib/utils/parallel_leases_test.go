package utils_test

import (
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/syncthing/syncthing/lib/utils"
)

func TestParallelLeases(t *testing.T) {
	jobCount := uint(10)
	workerCount := uint(3)
	ch := make([][]chan int, jobCount)
	for i := range jobCount {
		ch[i] = make([]chan int, 2)
		for j := range 2 {
			ch[i][j] = make(chan int)
		}
	}
	pl := utils.NewParallelLeases(workerCount, "testgroup")
	go func() {
		for i := range jobCount {
			pl.AsyncRunOne(fmt.Sprintf("worker %v", i), func() {
				fmt.Printf("worker #%v is running ...\n", i)
				nr := <-ch[i][0]
				ch[i][1] <- nr + 1000
				fmt.Printf("worker #%v is DONE\n", i)
			})
		}
	}()

	select {
	case ch[3][0] <- 3:
		assert.Fail(t, "too many parallel leases granted")
	case <-time.After(time.Second):
	}

	ch[2][0] <- 20
	assert.Equal(t, 1020, <-ch[2][1])

	select {
	case ch[3][0] <- 30:
	case <-time.After(time.Second):
		assert.Fail(t, "handover of leases fails")
	}

	assert.Equal(t, 1030, <-ch[3][1])

	for i := range jobCount {
		if (i != 2) && (i != 3) {
			go func() {
				ch[i][0] <- int(i)
			}()
		}
	}

	for i := range jobCount {
		if (i != 2) && (i != 3) {
			assert.Equal(t, 1000+int(i), <-ch[i][1])
		}
	}

	pl.WaitAllDone()
}

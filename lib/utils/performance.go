package utils

import (
	"time"

	"github.com/syncthing/syncthing/lib/logger"
)

type PerformanceStep struct {
	time time.Time
	name string
}

type PerformanceStopWatch struct {
	start time.Time
	steps []PerformanceStep
}

func PerformanceStopWatchStart() *PerformanceStopWatch {
	return &PerformanceStopWatch{
		start: time.Now(),
		steps: []PerformanceStep{},
	}
}

func (sw *PerformanceStopWatch) Step(name string) {
	sw.steps = append(sw.steps, PerformanceStep{
		time: time.Now(),
		name: name,
	})
}

func (sw *PerformanceStopWatch) LastStep(groupName, name string) {
	sw.Step(name)
	statement := groupName + ": "
	start := sw.start
	for _, step := range sw.steps {
		delta := step.time.Sub(start)
		start = step.time
		statement = statement + step.name + ": " + delta.String() + " "
	}
	logger.DefaultLogger.Debugf("%s", statement)
}

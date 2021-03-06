package stampede

import (
	"fmt"
	"github.com/cloudfoundry-incubator/runtime-schema/bbs"
	"github.com/cloudfoundry-incubator/runtime-schema/models"
	"github.com/nu7hatch/gouuid"
	datadog "github.com/xb95/go-datadog-api"
	"log"
	"sync"
	"time"
)

type runOnceTime struct {
	guid      string
	startTime time.Time
}

type RunOnceResult struct {
	Guid     string
	Duration time.Duration
	Failed   bool
}

func RunonceStampede(bbs *bbs.BBS, datadogClient *datadog.Client, runOnce *models.RunOnce, runOnceCount int) {

	completed, stop, errs := bbs.WatchForCompletedRunOnce()

	startAll := time.Now()

	if datadogClient != nil {
		event, err := datadogClient.PostEvent(&datadog.Event{
			Title: "diego_runonce_stampede_start",
			Text:  "started the stampede",
			Tags:  []string{fmt.Sprintf("count:%d", runOnceCount)},
		})

		log.Println("posted start event:", event, err)

		defer func() {
			event, err := datadogClient.PostEvent(&datadog.Event{
				Title: "diego_runonce_stampede_stop",
				Text:  "stopped the stampede",
				Tags: []string{
					fmt.Sprintf("count:%d", runOnceCount),
					fmt.Sprintf("duration:%s", time.Since(startAll)),
				},
			})

			log.Println("posted stop event:", event, err)
		}()
	}

	startTimes := make(chan runOnceTime, runOnceCount)
	go func() {
		for i := 0; i < runOnceCount; i++ {
			go createRunOnce(runOnce, startTimes, bbs)
		}
	}()

	seenRunOnces := 0
	runOnceStartTimes := make(map[string]time.Time)
	waitGroup := &sync.WaitGroup{}

	timer := time.After(100 * time.Minute)

OUTER:
	for {
		if seenRunOnces == runOnceCount {
			timer = time.After(30 * time.Second)
		}

		select {
		case startTime := <-startTimes:
			runOnceStartTimes[startTime.guid] = startTime.startTime

		case completedRunOnce := <-completed:
			startedAt, found := runOnceStartTimes[completedRunOnce.Guid]
			if !found {
				continue
			}

			log.Println("done:", seenRunOnces, RunOnceResult{
				Guid:     completedRunOnce.Guid,
				Duration: time.Since(startedAt),
				Failed:   completedRunOnce.Failed,
			})

			seenRunOnces++
			waitGroup.Add(1)
			go func() {
				log.Println("deleting", completedRunOnce.Guid)
				err := bbs.ResolveRunOnce(completedRunOnce)
				if err != nil {
					log.Println("failed to resolve run once:", completedRunOnce.Guid, err)
				} else {
					log.Println("deleted:", completedRunOnce.Guid)
				}
				waitGroup.Done()
			}()
		case err := <-errs:
			log.Println("watch error:", err)
		case <-timer:
			break OUTER
		}
	}

	waitGroup.Wait()

	close(stop)
}

func createRunOnce(runOnce *models.RunOnce, startTimes chan runOnceTime, bbs *bbs.BBS) {
	randomGuid, err := uuid.NewV4()
	if err != nil {
		log.Fatalln("somehow failed to create a guid:", err)
	}

	runOnce.Guid = randomGuid.String()
	runOnce.Actions = []models.ExecutorAction{
		{models.RunAction{Script: fmt.Sprintf("echo '%s'", runOnce.Guid), Timeout: 10 * time.Second}},
		runOnce.Actions[0],
	}

	startTimes <- runOnceTime{guid: runOnce.Guid, startTime: time.Now()}

	err = bbs.DesireRunOnce(runOnce)
	if err != nil {
		log.Fatalln("failed to desire RunOnce:", err)
	}

	log.Println("desired RunOnce:", runOnce.Guid)
}

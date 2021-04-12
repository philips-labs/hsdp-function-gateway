package crontab

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/philips-labs/siderite"
	"github.com/philips-software/go-hsdp-api/iron"
	"github.com/robfig/cron/v3"
)

type Job struct {
	client      *iron.Client
	ScheduleID  string
	CronPayload siderite.CronPayload
}

func (j Job) Run() {
	schedule, _, err := j.client.Schedules.GetSchedule(j.ScheduleID)
	if err != nil {
		fmt.Printf("Run %s: failed to find schedule: %v\n", j.ScheduleID, err)
		return
	}
	task, _, err := j.client.Tasks.QueueTask(iron.Task{
		CodeName: schedule.CodeName,
		Payload:  j.CronPayload.EncryptedPayload,
		Cluster:  schedule.Cluster,
		Timeout:  j.CronPayload.Timeout,
	})
	if err != nil {
		fmt.Printf("Run %s: error queuing task: %v\n", j.ScheduleID, err)
		return
	}
	fmt.Printf("Run %s: triggered task %s\n", j.ScheduleID, task.ID)
}

func Start(client *iron.Client) (chan bool, error) {
	ch := make(chan bool)
	ticker := time.NewTicker(15 * time.Second)
	crontab := cron.New()
	crontab.Start()

	go func() {
		fmt.Printf("Start crontab...\n")
		for {
			select {
			case <-ch:
				fmt.Printf("exiting...\n")
				return
			case <-ticker.C: // Refresh
				fmt.Printf("Refreshing...\n")
				// Collect all cronjob entries
				cronSchedules, err := getCronEntries(client)
				if err != nil {
					fmt.Printf("Error retrieving Iron schedules: %v\n", err)
					continue
				}
				updateEntries(client, crontab, cronSchedules)
				entries := crontab.Entries()
				for _, e := range entries {
					if job, ok := e.Job.(Job); ok {
						fmt.Printf("Active entry %d: %s, %v\n", e.ID, job.ScheduleID, e.Schedule)
					}
				}
			}
		}
	}()

	return ch, nil
}

func updateEntries(client *iron.Client, crontab *cron.Cron, schedules map[string]siderite.CronPayload) {
	entries := crontab.Entries()
	// Add new entries
	for id, cronPayload := range schedules {
		found := false
		for _, e := range entries {
			if job, ok := e.Job.(Job); ok {
				if job.ScheduleID == id {
					found = true
					break
				}
			}
		}
		if !found { // New cronjob
			job := Job{
				ScheduleID:  id,
				CronPayload: cronPayload,
				client:      client,
			}
			newID, err := crontab.AddJob(cronPayload.Schedule, job)
			if err != nil {
				fmt.Printf("error adding job %s: %v\n", id, err)
			}
			fmt.Printf("Added new job %d for schedule %s\n", newID, id)
		}
	}
	// Purge stale ones
	entries = crontab.Entries()
	for _, entry := range entries {
		if job, ok := entry.Job.(Job); ok {
			found := false
			for id := range schedules {
				if job.ScheduleID == id {
					found = true
					break
				}
			}
			if !found { // Stale
				fmt.Printf("Removing stale job %d for schedule %s\n", entry.ID, job.ScheduleID)
				crontab.Remove(entry.ID)
			}
		}
	}
}

func getCronEntries(client *iron.Client) (map[string]siderite.CronPayload, error) {
	cronSchedules := make(map[string]siderite.CronPayload, 0)
	schedules, _, err := client.Schedules.GetSchedules()
	if err != nil {
		return cronSchedules, nil
	}
	for _, schedule := range *schedules {
		var cronPayload siderite.CronPayload
		err := json.Unmarshal([]byte(schedule.Payload), &cronPayload)
		if err != nil {
			continue
		}
		if cronPayload.Schedule == "" {
			fmt.Printf("[%s] is not a cron schedule. skipping\n", schedule.ID)
			continue
		}
		cronSchedules[schedule.ID] = cronPayload
	}
	return cronSchedules, nil
}

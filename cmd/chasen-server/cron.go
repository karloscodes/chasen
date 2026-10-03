package main

import (
	"bytes"
	"database/sql"
	"log"
	"os/exec"
	"sync"
	"time"

	"github.com/karloscodes/chasen/protocol"
	"github.com/karloscodes/matcha"
)

// The cron jobs of the apps: `cron:` in chasen.yml, which travels in the
// settings of each deploy. At the start of each minute, by the clock, the API
// runs the jobs that are due with `chasen-server run`, like `chasen run`: in
// the container of the app, with no shell. The history of the app keeps each
// run as "cron <command>", with its output.
//
// The last minute it looked at is in the table cron_state. So a restart of
// the API, like the nightly update, skips no minute and runs none twice.
// After a longer stop it looks back 10 minutes at most: the server does not
// run a job many times in a row when it comes back.

const (
	cronCatchUp = 10 * time.Minute
	// cronKept is how many runs of each job the history keeps: a job that
	// runs each minute must not push the deploys out of it.
	cronKept = 20
)

// cronJob is one job of one app that is due.
type cronJob struct {
	app string
	job protocol.CronJob
}

func cronLoop(self string) {
	var mu sync.Mutex
	running := map[cronJob]bool{}
	for {
		time.Sleep(time.Until(time.Now().Truncate(time.Minute).Add(time.Minute)))
		for _, due := range dueCron(time.Now()) {
			mu.Lock()
			busy := running[due]
			running[due] = true
			mu.Unlock()
			// A job that still runs does not start again.
			if busy {
				log.Printf("cron: %s: %q still runs, this run is skipped", due.app, due.job.Run)
				continue
			}
			go func() {
				runCron(self, due)
				mu.Lock()
				delete(running, due)
				mu.Unlock()
			}()
		}
	}
}

// dueCron returns the jobs that are due in the minutes after the last look,
// up to now, and saves now as the last look.
func dueCron(now time.Time) []cronJob {
	now = now.UTC().Truncate(time.Minute)
	db, err := openServerDB()
	if err != nil {
		log.Print("cron: ", err)
		return nil
	}
	defer db.Close()
	var last time.Time
	var saved string
	if db.QueryRow("SELECT minute FROM cron_state WHERE id = 1").Scan(&saved) == nil {
		last, _ = time.Parse(time.RFC3339, saved)
	}
	if !now.After(last) {
		return nil
	}
	from := last.Add(time.Minute)
	if now.Sub(last) > cronCatchUp {
		from = now
	}
	if _, err := db.Exec("INSERT INTO cron_state (id, minute) VALUES (1, ?) ON CONFLICT (id) DO UPDATE SET minute = excluded.minute", now.Format(time.RFC3339)); err != nil {
		log.Print("cron: ", err)
		return nil
	}

	apps, err := listApps()
	if err != nil {
		log.Print("cron: ", err)
		return nil
	}
	var due []cronJob
	for _, name := range matcha.ListAppsSorted(apps) {
		settings, err := loadSettings(name)
		if err != nil {
			continue
		}
		for _, job := range settings.Cron {
			schedule, err := protocol.ParseSchedule(job.Schedule)
			if err != nil {
				continue
			}
			for minute := from; !minute.After(now); minute = minute.Add(time.Minute) {
				if schedule.Matches(minute) {
					due = append(due, cronJob{name, job})
					break
				}
			}
		}
	}
	return due
}

// runCron runs one job and keeps the run in the history of its app.
func runCron(self string, due cronJob) {
	words, err := protocol.CommandWords(due.job.Run)
	if err != nil {
		return
	}
	db, err := openServerDB()
	if err != nil {
		log.Print("cron: ", err)
		return
	}
	defer db.Close()
	action := "cron " + due.job.Run
	id, err := startActivity(db, due.app, action)
	if err != nil {
		log.Print("cron: ", err)
		return
	}
	forgetOldRuns(db, due.app, action)
	out := &cronOutput{save: func(output string) { saveActivity(db, id, output) }}
	cmd := exec.Command(self, append([]string{"run", due.app}, words...)...)
	cmd.Stdout, cmd.Stderr = out, out
	err = cmd.Run()
	finishActivity(db, id, err == nil, out.buf.String())
}

// forgetOldRuns keeps the newest runs of one job in the history.
func forgetOldRuns(db *sql.DB, app, action string) {
	db.Exec(`DELETE FROM activity WHERE app = ? AND action = ? AND id NOT IN
		(SELECT id FROM activity WHERE app = ? AND action = ? ORDER BY id DESC LIMIT ?)`, app, action, app, action, cronKept)
}

// cronOutput keeps the output of a run, and saves it to the history once a
// second while the run goes on, like the output of a command of the API.
type cronOutput struct {
	buf   bytes.Buffer
	save  func(output string)
	saved time.Time
}

func (c *cronOutput) Write(p []byte) (int, error) {
	if c.buf.Len() < activityOutputMax {
		c.buf.Write(p)
		if time.Since(c.saved) >= saveEvery {
			c.save(c.buf.String())
			c.saved = time.Now()
		}
	}
	return len(p), nil
}

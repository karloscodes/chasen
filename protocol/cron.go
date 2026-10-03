package protocol

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// CronJob is a command that the server runs in the container of an app, on a
// schedule: `cron:` in chasen.yml. It runs like `chasen run`, with no shell,
// and the history of the app keeps each run.
type CronJob struct {
	// Schedule has the 5 fields of cron, in UTC: minute, hour, day of the
	// month, month, day of the week. Or @hourly, @daily, @weekly, @monthly.
	Schedule string `json:"schedule" yaml:"schedule"`
	// Run is the command, split into words like a shell does, with quotes.
	Run string `json:"run" yaml:"run"`
}

// MaxCronJobs is the most cron jobs an app can have.
const MaxCronJobs = 20

// Schedule is a parsed schedule: for each field, the values that match.
type Schedule struct {
	minute, hour, day, month, weekday []bool
	// anyDay and anyWeekday are true for a field that is *. When both day
	// fields are set, cron runs on a day that matches either of them.
	anyDay, anyWeekday bool
}

var scheduleNames = map[string]string{
	"@hourly":  "0 * * * *",
	"@daily":   "0 0 * * *",
	"@weekly":  "0 0 * * 0",
	"@monthly": "0 0 1 * *",
}

// ParseSchedule reads a schedule of cron: "0 4 * * *", "*/15 * * * *",
// "0 9 * * 1-5", or @daily.
func ParseSchedule(text string) (Schedule, error) {
	var s Schedule
	if named, ok := scheduleNames[text]; ok {
		text = named
	}
	fields := strings.Fields(text)
	if len(fields) != 5 {
		return s, fmt.Errorf("invalid schedule %q: use the 5 fields of cron, like \"0 4 * * *\" for 04:00 UTC each day, or @hourly, @daily, @weekly, @monthly", text)
	}
	limits := [5][2]int{{0, 59}, {0, 23}, {1, 31}, {1, 12}, {0, 7}}
	var sets [5][]bool
	for i, field := range fields {
		set, err := parseField(field, limits[i][0], limits[i][1])
		if err != nil {
			return s, fmt.Errorf("invalid schedule %q: %w", text, err)
		}
		sets[i] = set
	}
	// Sunday is 0 and 7.
	sets[4][0] = sets[4][0] || sets[4][7]
	s.minute, s.hour, s.day, s.month, s.weekday = sets[0], sets[1], sets[2], sets[3], sets[4]
	s.anyDay, s.anyWeekday = fields[2] == "*", fields[4] == "*"
	return s, nil
}

// parseField reads one field: *, a number, a range a-b, a step */n or a-b/n,
// or a list of these with commas.
func parseField(field string, low, high int) ([]bool, error) {
	set := make([]bool, high+1)
	for _, part := range strings.Split(field, ",") {
		span, stepText, hasStep := strings.Cut(part, "/")
		step := 1
		if hasStep {
			n, err := strconv.Atoi(stepText)
			if err != nil || n < 1 {
				return nil, fmt.Errorf("%q has a bad step", part)
			}
			step = n
		}
		from, to := low, high
		if span != "*" {
			a, b, isRange := strings.Cut(span, "-")
			var err error
			if from, err = strconv.Atoi(a); err != nil {
				return nil, fmt.Errorf("%q is not a number", part)
			}
			to = from
			if isRange {
				if to, err = strconv.Atoi(b); err != nil {
					return nil, fmt.Errorf("%q is not a range", part)
				}
			} else if hasStep {
				to = high
			}
		}
		if from < low || to > high || from > to {
			return nil, fmt.Errorf("%q is out of %d-%d", part, low, high)
		}
		for v := from; v <= to; v += step {
			set[v] = true
		}
	}
	return set, nil
}

// Matches reports whether the schedule runs in the minute of t, in UTC.
func (s Schedule) Matches(t time.Time) bool {
	t = t.UTC()
	if !s.minute[t.Minute()] || !s.hour[t.Hour()] || !s.month[int(t.Month())] {
		return false
	}
	day, weekday := s.day[t.Day()], s.weekday[int(t.Weekday())]
	switch {
	case s.anyDay && s.anyWeekday:
		return true
	case s.anyDay:
		return weekday
	case s.anyWeekday:
		return day
	}
	return day || weekday
}

// CommandWords splits the command of a cron job into words, like a shell
// does with spaces and quotes, but with nothing else: no variables, no pipes.
// For those, write sh -c '...'.
func CommandWords(run string) ([]string, error) {
	var words []string
	var word strings.Builder
	inWord := false
	var quote rune
	for _, r := range run {
		switch {
		case quote != 0 && r == quote:
			quote = 0
		case quote != 0:
			word.WriteRune(r)
		case r == '\'' || r == '"':
			quote, inWord = r, true
		case r == ' ' || r == '\t':
			if inWord {
				words = append(words, word.String())
				word.Reset()
				inWord = false
			}
		default:
			word.WriteRune(r)
			inWord = true
		}
	}
	if quote != 0 {
		return nil, fmt.Errorf("the command %q has a quote that does not close", run)
	}
	if inWord {
		words = append(words, word.String())
	}
	if len(words) == 0 {
		return nil, errors.New("a cron job has no command: add `run:`")
	}
	if strings.HasPrefix(words[0], "-") {
		return nil, fmt.Errorf("%q is not a command", words[0])
	}
	return words, nil
}

// checkCron applies the rules of cron: in Settings.Check.
func checkCron(jobs []CronJob) []error {
	var problems []error
	if len(jobs) > MaxCronJobs {
		problems = append(problems, fmt.Errorf("%d cron jobs: an app can have %d", len(jobs), MaxCronJobs))
	}
	for _, job := range jobs {
		if _, err := ParseSchedule(job.Schedule); err != nil {
			problems = append(problems, err)
		}
		if _, err := CommandWords(job.Run); err != nil {
			problems = append(problems, err)
		}
	}
	return problems
}

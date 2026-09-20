package main

import (
	"fmt"
	"strings"
	"time"
)

func sleepDuration(arg string) (time.Duration, error) {
	duration, err := time.ParseDuration(arg)
	if err != nil || duration <= 0 {
		return 0, fmt.Errorf("sleep needs a positive duration such as 45m or 1h, or off to cancel")
	}
	return duration, nil
}

func (d *Daemon) sleep(arg string) string {
	arg = strings.ToLower(strings.TrimSpace(arg))
	if arg == "off" {
		d.cancelSleep()
		return ok("sleep timer off")
	}
	if arg == "" {
		if d.sleepUntil.IsZero() {
			return ok("sleep timer off")
		}
		return ok("sleep in " + max(time.Duration(0), time.Until(d.sleepUntil)).Round(time.Second).String())
	}
	duration, err := sleepDuration(arg)
	if err != nil {
		return fail(err.Error())
	}
	if d.station == nil && d.episode == nil || d.state == "failed" || d.state == "ended" {
		return fail("nothing playing; start a station or podcast before setting a sleep timer")
	}
	d.cancelSleep()
	d.sleepUntil = time.Now().Add(duration)
	generation := d.sleepGeneration
	d.sleepTimer = time.AfterFunc(duration, func() {
		d.mu.Lock()
		defer d.mu.Unlock()
		if generation != d.sleepGeneration {
			return
		}
		d.cancelSleep()
		d.kill()
	})
	return ok("sleep in " + duration.String())
}

func (d *Daemon) cancelSleep() {
	d.sleepGeneration++
	if d.sleepTimer != nil {
		d.sleepTimer.Stop()
		d.sleepTimer = nil
	}
	d.sleepUntil = time.Time{}
}

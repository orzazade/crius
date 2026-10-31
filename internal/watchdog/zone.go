package watchdog

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// CalendarZone checks that launchd's calendar jobs fire at local time. They are fired by UserEventAgent (Aqua), which
// reads the time zone once, when it starts: a zone set after that (10-26 15:05, Baku to Riga, agent up since 10-25
// 09:47) moves every job by the difference (the 09:00 goal report ran at 07:00; Crius 10-31 09:46). ps is the output
// of `ps -axww -o uid=,lstart=,command=` (LC_ALL=C); each logged-in user has an agent, and this user's (uid) fires
// this user's jobs. zoneSet is the time /etc/localtime was last set. An agent it cannot find or read is an error too.
func CalendarZone(ps []byte, uid int, zoneSet time.Time) error {
	const agent = "/usr/libexec/UserEventAgent (Aqua)"
	for _, line := range strings.Split(string(ps), "\n") {
		f := strings.Fields(line)
		if len(f) < 7 || f[0] != strconv.Itoa(uid) || strings.Join(f[6:], " ") != agent {
			continue
		}
		start, err := time.ParseInLocation("Mon Jan 2 15:04:05 2006", strings.Join(f[1:6], " "), time.Local)
		if err != nil {
			return fmt.Errorf("ps line %q: %w", line, err)
		}
		if zoneSet.After(start) {
			return fmt.Errorf("the time zone was set %s, after UserEventAgent (Aqua) started %s, so launchd's calendar jobs "+
				"(StartCalendarInterval) fire on the old zone; restart it (killall UserEventAgent) or log out and in",
				zoneSet.Format("2006-01-02 15:04"), start.Format("2006-01-02 15:04"))
		}
		return nil
	}
	return fmt.Errorf("no %s of uid %d in ps, so the calendar jobs' zone is not checked", agent, uid)
}

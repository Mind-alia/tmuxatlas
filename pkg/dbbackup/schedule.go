package dbbackup

import (
	"errors"
	"fmt"
	"time"
)

// Schedule modes.
const (
	ModeOff    = "off"
	ModeDaily  = "daily"
	ModeWeekly = "weekly"
)

// Retention bounds; DefaultKeep applies to a database never configured.
const (
	DefaultKeep = 7
	MaxKeep     = 365
)

// Settings is the schedule and the retention of one database. Time is the
// server's local time.
type Settings struct {
	Mode string `json:"mode"`
	// Time is "HH:MM".
	Time string `json:"time"`
	// Weekday is 0 (Sunday) to 6 (Saturday); weekly mode only.
	Weekday int `json:"weekday"`
	// Keep is the number of complete backups kept.
	Keep int `json:"keep"`
}

func defaultSettings() Settings {
	return Settings{Mode: ModeOff, Time: "03:00", Weekday: 0, Keep: DefaultKeep}
}

// clock reads "HH:MM".
func (s Settings) clock() (hour, minute int, err error) {
	if len(s.Time) != 5 || s.Time[2] != ':' {
		return 0, 0, errors.New("heure invalide (HH:MM attendu)")
	}
	for _, i := range []int{0, 1, 3, 4} {
		if s.Time[i] < '0' || s.Time[i] > '9' {
			return 0, 0, errors.New("heure invalide (HH:MM attendu)")
		}
	}
	hour = int(s.Time[0]-'0')*10 + int(s.Time[1]-'0')
	minute = int(s.Time[3]-'0')*10 + int(s.Time[4]-'0')
	if hour > 23 || minute > 59 {
		return 0, 0, errors.New("heure invalide (HH:MM attendu)")
	}
	return hour, minute, nil
}

// validate refuses settings the scheduler could not follow.
func (s Settings) validate() error {
	switch s.Mode {
	case ModeOff, ModeDaily, ModeWeekly:
	default:
		return errors.New("planification inconnue")
	}
	if _, _, err := s.clock(); err != nil {
		return err
	}
	if s.Weekday < 0 || s.Weekday > 6 {
		return errors.New("jour de la semaine invalide")
	}
	if s.Keep < 1 || s.Keep > MaxKeep {
		return fmt.Errorf("la rétention doit être comprise entre 1 et %d sauvegardes", MaxKeep)
	}
	return nil
}

// occurrence returns the scheduled time on the day of ref shifted by days.
func (s Settings) occurrence(ref time.Time, days int) time.Time {
	hour, minute, _ := s.clock()
	return time.Date(ref.Year(), ref.Month(), ref.Day()+days, hour, minute, 0, 0, ref.Location())
}

func (s Settings) matches(t time.Time) bool {
	return s.Mode == ModeDaily || (s.Mode == ModeWeekly && int(t.Weekday()) == s.Weekday)
}

// previous is the last scheduled time at or before now; zero when off.
func (s Settings) previous(now time.Time) time.Time {
	if s.Mode != ModeDaily && s.Mode != ModeWeekly {
		return time.Time{}
	}
	for days := 0; days >= -7; days-- {
		if t := s.occurrence(now, days); !t.After(now) && s.matches(t) {
			return t
		}
	}
	return time.Time{}
}

// next is the first scheduled time after now; zero when off.
func (s Settings) next(now time.Time) time.Time {
	if s.Mode != ModeDaily && s.Mode != ModeWeekly {
		return time.Time{}
	}
	for days := 0; days <= 7; days++ {
		if t := s.occurrence(now, days); t.After(now) && s.matches(t) {
			return t
		}
	}
	return time.Time{}
}

package services

// timecards.go — staff clock-in, breaks and timecards (v1.12.0, migration v32).
//
// One row in time_entries per clock-in. A break pauses the clock
// (break_started set); resuming adds the paused seconds to break_seconds.
// Clocking out while on a break ends the break first. Staff punch with
// their own PIN on the PIN pad (no sign-in needed), or from the app while
// signed in. Managers (timecards.manage) correct entries: every edit needs
// a reason and keeps who made it; nothing is ever deleted (void instead).
//
// Hours and pay follow Kenya's Regulation of Wages (General) Order: a
// normal week is up to 52 hours (setting timecard_weekly_hours); hours
// above that in one Monday–Sunday week are overtime at 1.5x the hourly
// rate (setting timecard_overtime_rate). Weeks and days are Nairobi time.
//
// Team sync: each entry rides as one "timecard" event keyed by client_uuid
// (last write wins by updated_at), so the owner sees every till's staff.

import (
	"database/sql"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"

	"posapp/internal/auth"
)

var (
	ErrAlreadyClockedIn = errors.New("already clocked in")
	ErrNotClockedIn     = errors.New("not clocked in")
)

// openEntryFlagAfter: an entry still open after this long is flagged as a
// probable forgotten clock-out (never closed automatically: a guess would
// pay someone wrongly; the manager closes it with the real time).
const openEntryFlagAfter = 16 * time.Hour

// maxShiftLength rejects edits that make one entry longer than a day.
const maxShiftLength = 24 * time.Hour

type TimeEntry struct {
	ID           int64  `json:"id"`
	UserID       int64  `json:"userId"`
	Username     string `json:"username"`
	FullName     string `json:"fullName"`
	ClockIn      string `json:"clockIn"`
	ClockOut     string `json:"clockOut"`
	BreakSeconds int64  `json:"breakSeconds"`
	BreakStarted string `json:"breakStarted"`
	OnBreak      bool   `json:"onBreak"`
	Open         bool   `json:"open"`
	Note         string `json:"note"`
	Status       string `json:"status"`
	EditedBy     string `json:"editedBy"`
	EditReason   string `json:"editReason"`
	DeviceID     string `json:"deviceId"`
	// WorkedSeconds: clock time minus breaks (to now while still open).
	WorkedSeconds int64 `json:"workedSeconds"`
	// Flag explains anything a manager should look at ("" = fine).
	Flag string `json:"flag"`
}

type TimecardDay struct {
	Date          string `json:"date"`
	WorkedSeconds int64  `json:"workedSeconds"`
	Entries       int    `json:"entries"`
}

type TimecardSummary struct {
	UserID          int64         `json:"userId"`
	Username        string        `json:"username"`
	FullName        string        `json:"fullName"`
	HourlyRateCents int64         `json:"hourlyRateCents"`
	DaysWorked      int           `json:"daysWorked"`
	WorkedSeconds   int64         `json:"workedSeconds"`
	NormalSeconds   int64         `json:"normalSeconds"`
	OvertimeSeconds int64         `json:"overtimeSeconds"`
	BreakSeconds    int64         `json:"breakSeconds"`
	PayCents        int64         `json:"payCents"` // 0 when no rate is set
	OpenEntries     int           `json:"openEntries"`
	Flags           int           `json:"flags"`
	Days            []TimecardDay `json:"days"`
}

type TimecardReport struct {
	From          string            `json:"from"`
	To            string            `json:"to"`
	WeeklyHours   float64           `json:"weeklyHours"`
	OvertimeRate  float64           `json:"overtimeRate"`
	People        []TimecardSummary `json:"people"`
	Entries       []TimeEntry       `json:"entries"`
	TotalSeconds  int64             `json:"totalSeconds"`
	TotalPayCents int64             `json:"totalPayCents"`
}

type TimeEntryEdit struct {
	ClockIn      string `json:"clockIn"`
	ClockOut     string `json:"clockOut"`
	BreakMinutes int64  `json:"breakMinutes"`
	Note         string `json:"note"`
	Reason       string `json:"reason"`
}

// parseStamp reads any stamp the app writes or a browser sends.
func parseClock(raw string) (time.Time, error) {
	raw = strings.TrimSpace(raw)
	for _, layout := range []string{nowStampLayout, time.RFC3339Nano, time.RFC3339, "2006-01-02T15:04", "2006-01-02 15:04:05"} {
		if t, err := time.Parse(layout, raw); err == nil {
			return t.UTC(), nil
		}
	}
	// a local datetime-input value without zone is Nairobi time
	if t, err := time.ParseInLocation("2006-01-02T15:04", raw, eastAfrica); err == nil {
		return t.UTC(), nil
	}
	return time.Time{}, fmt.Errorf("%w: time %q not understood", ErrInvalidInput, raw)
}

func stampOf(t time.Time) string { return t.UTC().Format(nowStampLayout) }

const timeEntryCols = `t.id, t.user_id, COALESCE(t.username,''), COALESCE(u.full_name, t.username, ''), t.clock_in,
	COALESCE(t.clock_out,''), t.break_seconds, COALESCE(t.break_started,''), COALESCE(t.note,''), COALESCE(t.status,'active'),
	COALESCE(t.edited_by_name,''), COALESCE(t.edit_reason,''), COALESCE(t.device_id,'')`

func scanTimeEntry(sc interface{ Scan(...any) error }, now time.Time) (TimeEntry, error) {
	var e TimeEntry
	err := sc.Scan(&e.ID, &e.UserID, &e.Username, &e.FullName, &e.ClockIn, &e.ClockOut, &e.BreakSeconds,
		&e.BreakStarted, &e.Note, &e.Status, &e.EditedBy, &e.EditReason, &e.DeviceID)
	if err != nil {
		return e, err
	}
	if e.FullName == "" {
		e.FullName = e.Username
	}
	e.Open = e.ClockOut == ""
	e.OnBreak = e.BreakStarted != ""
	e.WorkedSeconds, e.Flag = workedOf(e, now)
	return e, nil
}

// workedOf is clock time minus breaks; an open entry counts to now (and a
// running break stops the count where it began).
func workedOf(e TimeEntry, now time.Time) (int64, string) {
	if e.Status == "void" {
		return 0, ""
	}
	in, err := parseClock(e.ClockIn)
	if err != nil {
		return 0, "bad clock-in time"
	}
	end := now
	flag := ""
	if e.ClockOut != "" {
		if out, err := parseClock(e.ClockOut); err == nil {
			end = out
		}
	} else if now.Sub(in) > openEntryFlagAfter {
		flag = "still clocked in after 16 hours, probably forgot to clock out"
	}
	if e.BreakStarted != "" {
		if bs, err := parseClock(e.BreakStarted); err == nil && bs.Before(end) {
			end = bs
		}
	}
	secs := int64(end.Sub(in)/time.Second) - e.BreakSeconds
	if secs < 0 {
		secs = 0
	}
	return secs, flag
}

func (s *Service) getTimeEntry(id int64) (*TimeEntry, error) {
	row := s.db.QueryRow(s.db.Rebind(`SELECT `+timeEntryCols+` FROM time_entries t LEFT JOIN users u ON u.id = t.user_id WHERE t.id = ?`), id)
	e, err := scanTimeEntry(row, time.Now())
	if err != nil {
		return nil, ErrNotFound
	}
	return &e, nil
}

// ClockStatus returns the user's open entry (nil when clocked out).
func (s *Service) ClockStatus(userID int64) (*TimeEntry, error) {
	var id int64
	err := s.db.QueryRow(s.db.Rebind(`SELECT id FROM time_entries WHERE user_id = ? AND clock_out = '' AND status = 'active' ORDER BY id DESC LIMIT 1`), userID).Scan(&id)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return s.getTimeEntry(id)
}

// Punch moves a user's clock: in | out | break | resume.
func (s *Service) Punch(userID int64, username, action string) (*TimeEntry, error) {
	s.clockMu.Lock()
	defer s.clockMu.Unlock()
	open, err := s.ClockStatus(userID)
	if err != nil {
		return nil, err
	}
	now := stampOf(time.Now())
	if action == "toggle" { // the PIN pad's one button: in when out, out when in
		action = "in"
		if open != nil {
			action = "out"
		}
	}
	var id int64
	switch action {
	case "in":
		if open != nil {
			return nil, ErrAlreadyClockedIn
		}
		res, err := s.db.Exec(s.db.Rebind(`INSERT INTO time_entries (client_uuid, user_id, username, clock_in, device_id, status, updated_at)
			VALUES (?, ?, ?, ?, ?, 'active', ?)`), randToken(12), userID, username, now, s.deviceID(), now)
		if err != nil {
			return nil, err
		}
		id, _ = res.LastInsertId()
		if id == 0 { // Postgres has no LastInsertId
			_ = s.db.QueryRow(s.db.Rebind(`SELECT id FROM time_entries WHERE user_id = ? AND clock_in = ? ORDER BY id DESC LIMIT 1`), userID, now).Scan(&id)
		}
		s.Audit(userID, username, "CLOCK_IN", "time_entry", fmt.Sprint(id), "")
	case "break", "resume", "out":
		if open == nil {
			return nil, ErrNotClockedIn
		}
		id = open.ID
		switch action {
		case "break":
			if open.OnBreak {
				return nil, fmt.Errorf("%w: already on a break", ErrInvalidState)
			}
			_, err = s.db.Exec(s.db.Rebind(`UPDATE time_entries SET break_started = ?, updated_at = ? WHERE id = ?`), now, now, id)
		case "resume":
			if !open.OnBreak {
				return nil, fmt.Errorf("%w: not on a break", ErrInvalidState)
			}
			_, err = s.db.Exec(s.db.Rebind(`UPDATE time_entries SET break_seconds = break_seconds + ?, break_started = '', updated_at = ? WHERE id = ?`),
				secondsSince(open.BreakStarted, time.Now()), now, id)
		case "out":
			add := int64(0)
			if open.OnBreak {
				add = secondsSince(open.BreakStarted, time.Now())
			}
			_, err = s.db.Exec(s.db.Rebind(`UPDATE time_entries SET clock_out = ?, break_seconds = break_seconds + ?, break_started = '', updated_at = ? WHERE id = ?`),
				now, add, now, id)
		}
		if err != nil {
			return nil, err
		}
		s.Audit(userID, username, "CLOCK_"+strings.ToUpper(action), "time_entry", fmt.Sprint(id), "")
	default:
		return nil, fmt.Errorf("%w: action must be in, out, break or resume", ErrInvalidInput)
	}
	s.EmitTimeEntry(id)
	return s.getTimeEntry(id)
}

func secondsSince(stamp string, now time.Time) int64 {
	t, err := parseClock(stamp)
	if err != nil || t.After(now) {
		return 0
	}
	return int64(now.Sub(t) / time.Second)
}

// dateRange turns two YYYY-MM-DD (inclusive) into a UTC stamp window.
func dateRange(from, to string) (string, string, error) {
	if from == "" {
		from = time.Now().In(eastAfrica).AddDate(0, 0, -6).Format("2006-01-02")
	}
	if to == "" {
		to = eatToday()
	}
	if !validDay(from) || !validDay(to) {
		return "", "", fmt.Errorf("%w: dates must be YYYY-MM-DD", ErrInvalidInput)
	}
	if to < from {
		from, to = to, from
	}
	f, _ := dayBounds(from)
	_, t := dayBounds(to)
	return f, t, nil
}

// ListTimeEntries returns entries that started in the window (userID 0 =
// everyone), oldest first.
func (s *Service) ListTimeEntries(from, to string, userID int64, withVoid bool) ([]TimeEntry, error) {
	f, t, err := dateRange(from, to)
	if err != nil {
		return nil, err
	}
	q := `SELECT ` + timeEntryCols + ` FROM time_entries t LEFT JOIN users u ON u.id = t.user_id WHERE t.clock_in >= ? AND t.clock_in < ?`
	args := []any{f, t}
	if userID > 0 {
		q += ` AND t.user_id = ?`
		args = append(args, userID)
	}
	if !withVoid {
		q += ` AND t.status = 'active'`
	}
	q += ` ORDER BY t.clock_in, t.id LIMIT 5000`
	rows, err := s.db.Query(s.db.Rebind(q), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	now := time.Now()
	out := []TimeEntry{}
	for rows.Next() {
		e, err := scanTimeEntry(rows, now)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// weekStart is the Monday (Nairobi) of the week holding t.
func weekStart(t time.Time) string {
	l := t.In(eastAfrica)
	wd := int(l.Weekday())
	if wd == 0 {
		wd = 7
	}
	return l.AddDate(0, 0, -(wd - 1)).Format("2006-01-02")
}

// Timecards builds the hours/overtime/pay report for a date window.
// Overtime is counted per Monday–Sunday week; a week cut by the window
// edge only counts the hours inside the window (pick whole weeks for pay).
func (s *Service) Timecards(from, to string, userID int64) (*TimecardReport, error) {
	entries, err := s.ListTimeEntries(from, to, userID, true)
	if err != nil {
		return nil, err
	}
	weekly := s.settings.GetFloat("timecard_weekly_hours", 52)
	if weekly <= 0 || weekly > 168 {
		weekly = 52
	}
	otRate := s.settings.GetFloat("timecard_overtime_rate", 1.5)
	if otRate < 1 || otRate > 5 {
		otRate = 1.5
	}
	f, t, _ := dateRange(from, to)
	rep := &TimecardReport{From: f, To: t, WeeklyHours: weekly, OvertimeRate: otRate, Entries: entries}
	if from != "" {
		rep.From = from
	}
	if to != "" {
		rep.To = to
	}
	// People are keyed by username: entries synced from another till may
	// belong to staff who have no account on this one (user_id 0).
	rates := map[string]int64{}
	ids := map[string]int64{}
	if rows, err := s.db.Query(`SELECT id, username, COALESCE(hourly_rate_cents,0) FROM users`); err == nil {
		for rows.Next() {
			var id, r int64
			var un string
			if rows.Scan(&id, &un, &r) == nil {
				rates[strings.ToLower(un)] = r
				ids[strings.ToLower(un)] = id
			}
		}
		rows.Close()
	}
	type acc struct {
		sum   *TimecardSummary
		weeks map[string]int64
		days  map[string]*TimecardDay
	}
	people := map[string]*acc{}
	weekLimit := int64(math.Round(weekly * 3600))
	for _, e := range entries {
		key := strings.ToLower(e.Username)
		a := people[key]
		if a == nil {
			a = &acc{sum: &TimecardSummary{UserID: ids[key], Username: e.Username, FullName: e.FullName, HourlyRateCents: rates[key]},
				weeks: map[string]int64{}, days: map[string]*TimecardDay{}}
			people[key] = a
		}
		if e.Status == "void" {
			continue
		}
		in, err := parseClock(e.ClockIn)
		if err != nil {
			continue
		}
		if e.Open {
			a.sum.OpenEntries++
		}
		if e.Flag != "" {
			a.sum.Flags++
		}
		a.sum.WorkedSeconds += e.WorkedSeconds
		a.sum.BreakSeconds += e.BreakSeconds
		a.weeks[weekStart(in)] += e.WorkedSeconds
		day := in.In(eastAfrica).Format("2006-01-02")
		d := a.days[day]
		if d == nil {
			d = &TimecardDay{Date: day}
			a.days[day] = d
		}
		d.WorkedSeconds += e.WorkedSeconds
		d.Entries++
	}
	for _, a := range people {
		for _, w := range a.weeks {
			if w > weekLimit {
				a.sum.OvertimeSeconds += w - weekLimit
				a.sum.NormalSeconds += weekLimit
			} else {
				a.sum.NormalSeconds += w
			}
		}
		for _, d := range a.days {
			a.sum.Days = append(a.sum.Days, *d)
		}
		sort.Slice(a.sum.Days, func(i, j int) bool { return a.sum.Days[i].Date < a.sum.Days[j].Date })
		a.sum.DaysWorked = len(a.sum.Days)
		a.sum.PayCents = payFor(a.sum.NormalSeconds, a.sum.OvertimeSeconds, a.sum.HourlyRateCents, otRate)
		if a.sum.Days == nil {
			a.sum.Days = []TimecardDay{}
		}
		rep.People = append(rep.People, *a.sum)
		rep.TotalSeconds += a.sum.WorkedSeconds
		rep.TotalPayCents += a.sum.PayCents
	}
	sort.Slice(rep.People, func(i, j int) bool {
		return strings.ToLower(rep.People[i].FullName) < strings.ToLower(rep.People[j].FullName)
	})
	if rep.People == nil {
		rep.People = []TimecardSummary{}
	}
	return rep, nil
}

// payFor: normal hours at the rate plus overtime at rate x otRate, rounded
// to the cent (half up). Seconds keep partial hours exact.
func payFor(normalSec, otSec, rateCents int64, otRate float64) int64 {
	if rateCents <= 0 {
		return 0
	}
	// otRate in hundredths keeps the maths in integers (1.5 -> 150)
	ot := int64(math.Round(otRate * 100))
	num := normalSec*rateCents*100 + otSec*rateCents*ot
	den := int64(3600 * 100)
	return (2*num + den) / (2 * den)
}

func (s *Service) validateEdit(userID, excludeID int64, req TimeEntryEdit) (in, out time.Time, breakSec int64, err error) {
	in, err = parseClock(req.ClockIn)
	if err != nil {
		return
	}
	if in.After(time.Now().Add(5 * time.Minute)) {
		err = fmt.Errorf("%w: clock-in cannot be in the future", ErrInvalidInput)
		return
	}
	if strings.TrimSpace(req.ClockOut) != "" {
		out, err = parseClock(req.ClockOut)
		if err != nil {
			return
		}
		if !out.After(in) {
			err = fmt.Errorf("%w: clock-out must be after clock-in", ErrInvalidInput)
			return
		}
		if out.Sub(in) > maxShiftLength {
			err = fmt.Errorf("%w: one entry cannot be longer than 24 hours, split it into two", ErrInvalidInput)
			return
		}
		if out.After(time.Now().Add(5 * time.Minute)) {
			err = fmt.Errorf("%w: clock-out cannot be in the future", ErrInvalidInput)
			return
		}
	}
	if req.BreakMinutes < 0 {
		err = fmt.Errorf("%w: break cannot be negative", ErrInvalidInput)
		return
	}
	breakSec = req.BreakMinutes * 60
	end := out
	if out.IsZero() {
		end = time.Now()
	}
	if breakSec > int64(end.Sub(in)/time.Second) {
		err = fmt.Errorf("%w: break is longer than the time worked", ErrInvalidInput)
		return
	}
	// no two entries of one person may overlap (it would pay twice)
	rows, qerr := s.db.Query(s.db.Rebind(`SELECT id, clock_in, COALESCE(clock_out,'') FROM time_entries WHERE user_id = ? AND status = 'active' AND id != ?`), userID, excludeID)
	if qerr != nil {
		err = qerr
		return
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var oi, oo string
		if rows.Scan(&id, &oi, &oo) != nil {
			continue
		}
		a, e1 := parseClock(oi)
		if e1 != nil {
			continue
		}
		b := time.Now()
		if oo != "" {
			if bb, e2 := parseClock(oo); e2 == nil {
				b = bb
			}
		}
		if out.IsZero() && oo == "" {
			err = fmt.Errorf("%w: this person already has an open entry, close it first", ErrInvalidState)
			return
		}
		if in.Before(b) && end.After(a) {
			err = fmt.Errorf("%w: overlaps another entry (%s)", ErrInvalidInput, a.In(eastAfrica).Format("Mon 2 Jan 15:04"))
			return
		}
	}
	return
}

// AddTimeEntry records a missed punch for someone (manager).
func (s *Service) AddTimeEntry(userID int64, req TimeEntryEdit, p *auth.Principal) (*TimeEntry, error) {
	if strings.TrimSpace(req.Reason) == "" {
		return nil, fmt.Errorf("%w: say why this entry is added", ErrInvalidInput)
	}
	var username string
	if err := s.db.QueryRow(s.db.Rebind(`SELECT username FROM users WHERE id = ?`), userID).Scan(&username); err != nil {
		return nil, ErrNotFound
	}
	s.clockMu.Lock()
	defer s.clockMu.Unlock()
	in, out, br, err := s.validateEdit(userID, 0, req)
	if err != nil {
		return nil, err
	}
	outS := ""
	if !out.IsZero() {
		outS = stampOf(out)
	}
	now := stampOf(time.Now())
	if _, err := s.db.Exec(s.db.Rebind(`INSERT INTO time_entries (client_uuid, user_id, username, clock_in, clock_out, break_seconds, note, device_id, status, edited_by_name, edit_reason, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, 'active', ?, ?, ?)`),
		randToken(12), userID, username, stampOf(in), outS, br, truncStr(req.Note, 200), s.deviceID(), p.Username, truncStr(req.Reason, 200), now); err != nil {
		return nil, err
	}
	var id int64
	_ = s.db.QueryRow(s.db.Rebind(`SELECT id FROM time_entries WHERE user_id = ? AND clock_in = ? ORDER BY id DESC LIMIT 1`), userID, stampOf(in)).Scan(&id)
	s.Audit(p.ID, p.Username, "TIME_ENTRY_ADDED", "time_entry", fmt.Sprint(id), fmt.Sprintf("%s: %s", username, req.Reason))
	s.EmitTimeEntry(id)
	return s.getTimeEntry(id)
}

// EditTimeEntry corrects an entry (manager). The old values go to the audit log.
func (s *Service) EditTimeEntry(id int64, req TimeEntryEdit, p *auth.Principal) (*TimeEntry, error) {
	if strings.TrimSpace(req.Reason) == "" {
		return nil, fmt.Errorf("%w: say why the entry is changed", ErrInvalidInput)
	}
	s.clockMu.Lock()
	defer s.clockMu.Unlock()
	old, err := s.getTimeEntry(id)
	if err != nil {
		return nil, err
	}
	if old.Status == "void" {
		return nil, fmt.Errorf("%w: entry is void", ErrInvalidState)
	}
	in, out, br, err := s.validateEdit(old.UserID, id, req)
	if err != nil {
		return nil, err
	}
	outS := ""
	if !out.IsZero() {
		outS = stampOf(out)
	}
	breakStarted := old.BreakStarted
	if outS != "" {
		breakStarted = ""
	}
	if _, err := s.db.Exec(s.db.Rebind(`UPDATE time_entries SET clock_in = ?, clock_out = ?, break_seconds = ?, break_started = ?, note = ?,
		edited_by_name = ?, edit_reason = ?, updated_at = ? WHERE id = ?`),
		stampOf(in), outS, br, breakStarted, truncStr(req.Note, 200), p.Username, truncStr(req.Reason, 200), stampOf(time.Now()), id); err != nil {
		return nil, err
	}
	s.Audit(p.ID, p.Username, "TIME_ENTRY_EDITED", "time_entry", fmt.Sprint(id),
		fmt.Sprintf("%s was %s to %s, break %dm; now %s to %s, break %dm; %s", old.Username, old.ClockIn, old.ClockOut, old.BreakSeconds/60,
			stampOf(in), outS, br/60, req.Reason))
	s.EmitTimeEntry(id)
	return s.getTimeEntry(id)
}

// VoidTimeEntry takes an entry out of every total (kept for the record).
func (s *Service) VoidTimeEntry(id int64, reason string, p *auth.Principal) error {
	if strings.TrimSpace(reason) == "" {
		return fmt.Errorf("%w: say why the entry is voided", ErrInvalidInput)
	}
	res, err := s.db.Exec(s.db.Rebind(`UPDATE time_entries SET status = 'void', break_started = '', edited_by_name = ?, edit_reason = ?, updated_at = ? WHERE id = ? AND status = 'active'`),
		p.Username, truncStr(reason, 200), stampOf(time.Now()), id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	s.Audit(p.ID, p.Username, "TIME_ENTRY_VOIDED", "time_entry", fmt.Sprint(id), reason)
	s.EmitTimeEntry(id)
	return nil
}

// SetHourlyRate stores a person's pay rate (manager; 0 = not set).
func (s *Service) SetHourlyRate(userID, cents int64, p *auth.Principal) error {
	if cents < 0 || cents > 100_000_00 {
		return fmt.Errorf("%w: hourly rate out of range", ErrInvalidInput)
	}
	res, err := s.db.Exec(s.db.Rebind(`UPDATE users SET hourly_rate_cents = ? WHERE id = ?`), cents, userID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	s.Audit(p.ID, p.Username, "HOURLY_RATE_SET", "user", fmt.Sprint(userID), fmt.Sprint(cents))
	return nil
}

// TimecardsCSV is the payroll hand-off: one row per entry, then one
// summary row per person.
func (s *Service) TimecardsCSV(from, to string) (string, error) {
	rep, err := s.Timecards(from, to, 0)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	w := csv.NewWriter(&b)
	hrs := func(sec int64) string { return strconv.FormatFloat(float64(sec)/3600, 'f', 2, 64) }
	local := func(st string) string {
		if st == "" {
			return ""
		}
		t, err := parseClock(st)
		if err != nil {
			return st
		}
		return t.In(eastAfrica).Format("2006-01-02 15:04")
	}
	_ = w.Write([]string{"Person", "Username", "Clock in", "Clock out", "Break (min)", "Hours", "Status", "Note", "Edited by", "Edit reason"})
	for _, e := range rep.Entries {
		_ = w.Write([]string{e.FullName, e.Username, local(e.ClockIn), local(e.ClockOut), fmt.Sprint(e.BreakSeconds / 60),
			hrs(e.WorkedSeconds), e.Status, csvSafe(e.Note), e.EditedBy, csvSafe(e.EditReason)})
	}
	_ = w.Write(nil)
	_ = w.Write([]string{"Person", "Username", "Days", "Hours", "Normal hours", "Overtime hours", "Hourly rate", "Pay"})
	for _, pp := range rep.People {
		_ = w.Write([]string{pp.FullName, pp.Username, fmt.Sprint(pp.DaysWorked), hrs(pp.WorkedSeconds), hrs(pp.NormalSeconds),
			hrs(pp.OvertimeSeconds), centsStr(pp.HourlyRateCents), centsStr(pp.PayCents)})
	}
	w.Flush()
	return b.String(), w.Error()
}

// csvSafe stops spreadsheet formula injection from free-text fields.
func csvSafe(v string) string {
	if v != "" && strings.ContainsRune("=+-@\t\r", rune(v[0])) {
		return "'" + v
	}
	return v
}

func centsStr(c int64) string {
	sign := ""
	if c < 0 {
		sign = "-"
		c = -c
	}
	return fmt.Sprintf("%s%d.%02d", sign, c/100, c%100)
}

// ---- team sync -------------------------------------------------------------

type timecardEvent struct {
	ClientUUID   string `json:"clientUuid"`
	UserName     string `json:"userName"`
	ClockIn      string `json:"clockIn"`
	ClockOut     string `json:"clockOut"`
	BreakSeconds int64  `json:"breakSeconds"`
	BreakStarted string `json:"breakStarted"`
	Note         string `json:"note"`
	Status       string `json:"status"`
	EditedBy     string `json:"editedBy"`
	EditReason   string `json:"editReason"`
	DeviceID     string `json:"deviceId"`
	UpdatedAt    string `json:"updatedAt"`
}

// EmitTimeEntry queues the entry's whole current state.
func (s *Service) EmitTimeEntry(id int64) {
	var ev timecardEvent
	err := s.db.QueryRow(s.db.Rebind(`SELECT COALESCE(client_uuid,''), COALESCE(username,''), clock_in, COALESCE(clock_out,''), break_seconds,
		COALESCE(break_started,''), COALESCE(note,''), COALESCE(status,'active'), COALESCE(edited_by_name,''), COALESCE(edit_reason,''),
		COALESCE(device_id,''), COALESCE(updated_at,'') FROM time_entries WHERE id = ?`), id).
		Scan(&ev.ClientUUID, &ev.UserName, &ev.ClockIn, &ev.ClockOut, &ev.BreakSeconds, &ev.BreakStarted, &ev.Note, &ev.Status,
			&ev.EditedBy, &ev.EditReason, &ev.DeviceID, &ev.UpdatedAt)
	if err != nil || ev.ClientUUID == "" {
		return
	}
	s.Emit("timecard", "upsert", ev)
}

func (s *Service) applyTimecard(payload json.RawMessage) error {
	var ev timecardEvent
	if err := json.Unmarshal(payload, &ev); err != nil {
		return err
	}
	if ev.ClientUUID == "" || ev.ClockIn == "" {
		return fmt.Errorf("timecard event missing identity")
	}
	if _, err := parseClock(ev.ClockIn); err != nil {
		return err
	}
	if ev.Status != "void" {
		ev.Status = "active"
	}
	var id int64
	var updated string
	err := s.db.QueryRow(s.db.Rebind(`SELECT id, COALESCE(updated_at,'') FROM time_entries WHERE client_uuid = ?`), ev.ClientUUID).Scan(&id, &updated)
	if err == nil {
		if ev.UpdatedAt <= updated {
			return nil // ours is as new or newer
		}
		_, err = s.db.Exec(s.db.Rebind(`UPDATE time_entries SET clock_in = ?, clock_out = ?, break_seconds = ?, break_started = ?, note = ?, status = ?,
			edited_by_name = ?, edit_reason = ?, updated_at = ? WHERE id = ?`),
			ev.ClockIn, ev.ClockOut, ev.BreakSeconds, ev.BreakStarted, truncStr(ev.Note, 200), ev.Status, ev.EditedBy, truncStr(ev.EditReason, 200), ev.UpdatedAt, id)
		return err
	}
	if err != sql.ErrNoRows {
		return err
	}
	var uid int64
	_ = s.db.QueryRow(s.db.Rebind(`SELECT id FROM users WHERE username = ?`), ev.UserName).Scan(&uid)
	// uid 0 = the person isn't on this till: the entry is still kept (the
	// owner's report shows it by username)
	_, err = s.db.Exec(s.db.Rebind(`INSERT INTO time_entries (client_uuid, user_id, username, clock_in, clock_out, break_seconds, break_started, note,
		device_id, status, edited_by_name, edit_reason, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`),
		ev.ClientUUID, uid, ev.UserName, ev.ClockIn, ev.ClockOut, ev.BreakSeconds, ev.BreakStarted, truncStr(ev.Note, 200),
		ev.DeviceID, ev.Status, ev.EditedBy, truncStr(ev.EditReason, 200), ev.UpdatedAt)
	if isUniqueViolation(err) {
		return nil
	}
	return err
}

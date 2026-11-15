package services

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestPunchCycle(t *testing.T) {
	f := newMoneyTill(t)
	p := f.principal()
	if _, err := f.s.Punch(p.ID, p.Username, "out"); !errors.Is(err, ErrNotClockedIn) {
		t.Fatalf("out before in: %v", err)
	}
	e, err := f.s.Punch(p.ID, p.Username, "in")
	if err != nil || !e.Open {
		t.Fatalf("in: %v %+v", err, e)
	}
	if _, err := f.s.Punch(p.ID, p.Username, "in"); !errors.Is(err, ErrAlreadyClockedIn) {
		t.Fatalf("double in: %v", err)
	}
	if _, err := f.s.Punch(p.ID, p.Username, "resume"); err == nil {
		t.Fatal("resume without a break must fail")
	}
	if e, err = f.s.Punch(p.ID, p.Username, "break"); err != nil || !e.OnBreak {
		t.Fatalf("break: %v", err)
	}
	// clocking out while on a break closes the break
	if e, err = f.s.Punch(p.ID, p.Username, "out"); err != nil || e.Open || e.OnBreak {
		t.Fatalf("out: %v %+v", err, e)
	}
	if st, _ := f.s.ClockStatus(p.ID); st != nil {
		t.Fatal("still open after clock out")
	}
	// the entry went to the sync outbox
	var n int
	f.s.db.QueryRow(`SELECT COUNT(*) FROM sync_outbox WHERE entity = 'timecard'`).Scan(&n)
	if n < 3 {
		t.Fatalf("timecard events = %d", n)
	}
}

// Overtime: 6 days x 10h = 60h in one week, 52 normal + 8 overtime at 1.5x.
func TestTimecardOvertimeAndPay(t *testing.T) {
	f := newMoneyTill(t)
	p := f.principal()
	if err := f.s.SetHourlyRate(p.ID, 20000, p); err != nil { // KES 200/h
		t.Fatal(err)
	}
	// Monday 2 Nov 2026 .. Saturday 7 Nov, 08:00-18:30 Nairobi, 30 min break
	for d := 0; d < 6; d++ {
		day := time.Date(2026, 11, 2+d, 8, 0, 0, 0, eastAfrica)
		_, err := f.s.AddTimeEntry(p.ID, TimeEntryEdit{
			ClockIn: stampOf(day), ClockOut: stampOf(day.Add(10*time.Hour + 30*time.Minute)), BreakMinutes: 30, Reason: "paper sheet",
		}, p)
		if err != nil {
			t.Fatalf("day %d: %v", d, err)
		}
	}
	rep, err := f.s.Timecards("2026-11-02", "2026-11-08", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.People) != 1 {
		t.Fatalf("people = %d", len(rep.People))
	}
	pp := rep.People[0]
	if pp.WorkedSeconds != 60*3600 || pp.NormalSeconds != 52*3600 || pp.OvertimeSeconds != 8*3600 || pp.DaysWorked != 6 {
		t.Fatalf("hours: %+v", pp)
	}
	// 52 x 200 + 8 x 300 = 10,400 + 2,400 = KES 12,800
	if pp.PayCents != 1_280_000 {
		t.Fatalf("pay = %d", pp.PayCents)
	}
	csv, err := f.s.TimecardsCSV("2026-11-02", "2026-11-08")
	if err != nil || !strings.Contains(csv, "12800.00") {
		t.Fatalf("csv: %v\n%s", err, csv)
	}
}

func TestTimeEntryEditsAreGuarded(t *testing.T) {
	f := newMoneyTill(t)
	p := f.principal()
	day := time.Date(2026, 11, 3, 8, 0, 0, 0, eastAfrica)
	e, err := f.s.AddTimeEntry(p.ID, TimeEntryEdit{ClockIn: stampOf(day), ClockOut: stampOf(day.Add(8 * time.Hour)), Reason: "missed"}, p)
	if err != nil {
		t.Fatal(err)
	}
	cases := map[string]TimeEntryEdit{
		"no reason":   {ClockIn: stampOf(day.Add(9 * time.Hour)), ClockOut: stampOf(day.Add(10 * time.Hour))},
		"overlap":     {ClockIn: stampOf(day.Add(7 * time.Hour)), ClockOut: stampOf(day.Add(9 * time.Hour)), Reason: "x"},
		"out<in":      {ClockIn: stampOf(day.Add(12 * time.Hour)), ClockOut: stampOf(day.Add(11 * time.Hour)), Reason: "x"},
		"long break":  {ClockIn: stampOf(day.Add(9 * time.Hour)), ClockOut: stampOf(day.Add(10 * time.Hour)), BreakMinutes: 90, Reason: "x"},
		"future":      {ClockIn: stampOf(time.Now().Add(48 * time.Hour)), Reason: "x"},
		"over 24 hrs": {ClockIn: stampOf(day.Add(-30 * time.Hour)), ClockOut: stampOf(day.Add(-1 * time.Hour)), Reason: "x"},
	}
	for name, c := range cases {
		if _, err := f.s.AddTimeEntry(p.ID, c, p); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	// edit keeps who and why; void takes it out of totals
	if _, err := f.s.EditTimeEntry(e.ID, TimeEntryEdit{ClockIn: stampOf(day), ClockOut: stampOf(day.Add(9 * time.Hour)), Reason: "left late"}, p); err != nil {
		t.Fatal(err)
	}
	got, _ := f.s.getTimeEntry(e.ID)
	if got.WorkedSeconds != 9*3600 || got.EditedBy != "cash1" || got.EditReason != "left late" {
		t.Fatalf("after edit: %+v", got)
	}
	if err := f.s.VoidTimeEntry(e.ID, "", p); err == nil {
		t.Fatal("void without reason accepted")
	}
	if err := f.s.VoidTimeEntry(e.ID, "duplicate", p); err != nil {
		t.Fatal(err)
	}
	rep, _ := f.s.Timecards("2026-11-03", "2026-11-03", 0)
	if rep.TotalSeconds != 0 {
		t.Fatalf("void still counted: %d", rep.TotalSeconds)
	}
}

// An entry from another till applies once, last write wins, and staff
// unknown on this till still show by username.
func TestApplyTimecardEvent(t *testing.T) {
	f := newMoneyTill(t)
	ev := timecardEvent{ClientUUID: "tc-remote-1", UserName: "amina", ClockIn: "2026-11-03T05:00:00.000Z",
		ClockOut: "2026-11-03T13:00:00.000Z", Status: "active", UpdatedAt: "2026-11-03T13:00:00.000Z"}
	raw, _ := json.Marshal(ev)
	for i := 0; i < 2; i++ {
		if err := f.s.applyTimecard(raw); err != nil {
			t.Fatal(err)
		}
	}
	older := ev
	older.ClockOut = "2026-11-03T20:00:00.000Z"
	older.UpdatedAt = "2026-11-03T12:00:00.000Z"
	raw, _ = json.Marshal(older)
	_ = f.s.applyTimecard(raw)
	rep, _ := f.s.Timecards("2026-11-03", "2026-11-03", 0)
	if len(rep.People) != 1 || rep.People[0].Username != "amina" || rep.People[0].WorkedSeconds != 8*3600 {
		t.Fatalf("report: %+v", rep.People)
	}
	newer := ev
	newer.Status = "void"
	newer.UpdatedAt = "2026-11-04T08:00:00.000Z"
	raw, _ = json.Marshal(newer)
	_ = f.s.applyTimecard(raw)
	rep, _ = f.s.Timecards("2026-11-03", "2026-11-03", 0)
	if rep.TotalSeconds != 0 {
		t.Fatal("newer void not applied")
	}
}

func TestPayRounding(t *testing.T) {
	// 1 second at KES 100/h = 2.78 cents -> 3
	if got := payFor(1, 0, 10000, 1.5); got != 3 {
		t.Fatalf("got %d", got)
	}
	if got := payFor(3600, 3600, 10000, 1.5); got != 25000 {
		t.Fatalf("got %d", got)
	}
	if payFor(3600, 0, 0, 1.5) != 0 {
		t.Fatal("no rate must be no pay")
	}
}

func TestPunchToggle(t *testing.T) {
	f := newMoneyTill(t)
	p := f.principal()
	e, err := f.s.Punch(p.ID, p.Username, "toggle")
	if err != nil || !e.Open {
		t.Fatalf("toggle in: %v", err)
	}
	e, err = f.s.Punch(p.ID, p.Username, "toggle")
	if err != nil || e.Open {
		t.Fatalf("toggle out: %v", err)
	}
}

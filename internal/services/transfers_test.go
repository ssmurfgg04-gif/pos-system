package services

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	"posapp/internal/auth"
	"posapp/internal/models"
)

// fakeTransferCloud follows db/deploy-v1120-transfers.sql.
type fakeTransferCloud struct {
	mu      sync.Mutex
	rows    []*cloudTransfer
	down    bool
	seq     int
	stores  map[string]string // team -> name (one business)
	devices map[string]string // device -> team
}

func (f *fakeTransferCloud) rpcFor(device string) transferRPCFunc {
	return func(fn string, args map[string]any, out any) error {
		f.mu.Lock()
		defer f.mu.Unlock()
		if f.down {
			return errors.New("network down")
		}
		me := f.devices[device]
		raw, _ := json.Marshal(args)
		var in struct {
			ClientRef string          `json:"p_client_ref"`
			ToTeam    string          `json:"p_to_team"`
			Lines     []TransferLine  `json:"p_lines"`
			Note      string          `json:"p_note"`
			User      string          `json:"p_user"`
			ID        string          `json:"p_id"`
			Received  []TransferQty   `json:"p_received"`
			Extra     json.RawMessage `json:"-"`
		}
		_ = json.Unmarshal(raw, &in)
		find := func(ref string) *cloudTransfer {
			for _, r := range f.rows {
				if r.FromTeam == me && r.ClientRef == ref {
					return r
				}
			}
			return nil
		}
		var res any
		switch fn {
		case "sync_list_stores":
			stores := []map[string]any{}
			for code, name := range f.stores {
				stores = append(stores, map[string]any{"team_code": code, "name": name})
			}
			res = map[string]any{"ok": true, "stores": stores}
		case "transfer_send":
			if t := find(in.ClientRef); t != nil {
				res = map[string]any{"ok": true, "existing": true, "transfer": t}
				break
			}
			if _, ok := f.stores[in.ToTeam]; !ok || in.ToTeam == me {
				res = map[string]any{"ok": false, "final": true, "error": "that shop is not part of your business"}
				break
			}
			f.seq++
			t := &cloudTransfer{ID: fmt.Sprintf("uuid-%d", f.seq), FromTeam: me, ToTeam: in.ToTeam, FromName: f.stores[me], ToName: f.stores[in.ToTeam],
				ClientRef: in.ClientRef, Lines: in.Lines, Note: in.Note, Status: "sent", SentBy: in.User, SentAt: nowStamp()}
			f.rows = append(f.rows, t)
			res = map[string]any{"ok": true, "transfer": t}
		case "transfer_list":
			list := []*cloudTransfer{}
			for _, r := range f.rows {
				if (r.FromTeam == me || r.ToTeam == me) && len(r.Lines) > 0 {
					list = append(list, r)
				}
			}
			res = map[string]any{"ok": true, "transfers": list}
		case "transfer_receive":
			var t *cloudTransfer
			for _, r := range f.rows {
				if r.ID == in.ID && r.ToTeam == me {
					t = r
				}
			}
			switch {
			case t == nil:
				res = map[string]any{"ok": false, "final": true, "error": "transfer not found"}
			case t.Status == "received" && t.ReceivedDevice == device:
				res = map[string]any{"ok": true, "existing": true, "transfer": t}
			case t.Status == "received":
				res = map[string]any{"ok": false, "final": true, "error": "already received on another till"}
			case t.Status == "cancelled":
				res = map[string]any{"ok": false, "final": true, "error": "the sending shop cancelled this transfer"}
			default:
				t.Status, t.ReceivedDevice, t.ReceivedLines, t.ReceivedBy = "received", device, in.Received, in.User
				res = map[string]any{"ok": true, "transfer": t}
			}
		case "transfer_cancel":
			t := find(in.ClientRef)
			if t == nil {
				f.seq++
				t = &cloudTransfer{ID: fmt.Sprintf("uuid-%d", f.seq), FromTeam: me, ToTeam: in.ToTeam, ClientRef: in.ClientRef,
					Lines: []TransferLine{}, Status: "cancelled", CancelledDevice: device}
				f.rows = append(f.rows, t)
			}
			if t.Status == "received" {
				res = map[string]any{"ok": false, "final": true, "error": "already received"}
				break
			}
			if t.Status == "sent" {
				t.Status, t.CancelledDevice = "cancelled", device
			}
			res = map[string]any{"ok": true, "transfer": t}
		default:
			return fmt.Errorf("unknown rpc %s", fn)
		}
		b, _ := json.Marshal(res)
		return json.Unmarshal(b, out)
	}
}

type shopTill struct {
	s *Service
	p *auth.Principal
}

func newShopTill(t *testing.T, cloud *fakeTransferCloud, team, device string) *shopTill {
	f := newMoneyTill(t)
	_ = f.s.settings.Set("sync_team_code", team)
	_ = f.s.settings.Set("device_id", device)
	cloud.devices[device] = team
	f.s.transferRPCForTest = cloud.rpcFor(device)
	return &shopTill{s: f.s, p: f.principal()}
}

func stockOf(t *testing.T, s *Service, sku string) int64 {
	t.Helper()
	var q int64
	if err := s.db.QueryRow(`SELECT stock_qty_milli FROM products WHERE sku = ?`, sku).Scan(&q); err != nil {
		return -1
	}
	return q
}

func sendReq(s *Service, to string, sku string, qty int64) SendTransferRequest {
	var id int64
	s.db.QueryRow(`SELECT id FROM products WHERE sku = ?`, sku).Scan(&id)
	r := SendTransferRequest{ToTeam: to, Note: "restock"}
	r.Lines = append(r.Lines, struct {
		ProductID int64 `json:"productId"`
		QtyMilli  int64 `json:"qtyMilli"`
	}{id, qty})
	return r
}

func newTransferCloud() *fakeTransferCloud {
	return &fakeTransferCloud{stores: map[string]string{"TEAM-A": "Kiambu Rd", "TEAM-B": "Thika Rd"}, devices: map[string]string{}}
}

func TestTransferSendReceivePartial(t *testing.T) {
	cloud := newTransferCloud()
	a := newShopTill(t, cloud, "TEAM-A", "dev-a1")
	b := newShopTill(t, cloud, "TEAM-B", "dev-b1")
	b2 := newShopTill(t, cloud, "TEAM-B", "dev-b2")
	// shop B has never stocked SKU-M1's sibling: remove it there so the
	// receive must create it
	b.s.db.Exec(`UPDATE products SET sku = 'OTHER-B' WHERE sku = 'SKU-M1'`)

	if _, err := a.s.SendTransfer(sendReq(a.s, "TEAM-B", "SKU-M1", 60000), a.p); !errors.Is(err, ErrInsufficientStock) {
		t.Fatalf("sending more than the shelf: %v", err)
	}
	if _, err := a.s.SendTransfer(sendReq(a.s, "TEAM-A", "SKU-M1", 1000), a.p); err == nil {
		t.Fatal("a shop sent to itself")
	}
	tr, err := a.s.SendTransfer(sendReq(a.s, "TEAM-B", "SKU-M1", 10000), a.p)
	if err != nil {
		t.Fatal(err)
	}
	if tr.Status != "sent" || stockOf(t, a.s, "SKU-M1") != 40000 {
		t.Fatalf("after send: %s, stock %d", tr.Status, stockOf(t, a.s, "SKU-M1"))
	}
	list, _ := b.s.ListTransfers()
	if !list.Online || len(list.Transfers) != 1 || list.Transfers[0].Direction != "in" {
		t.Fatalf("B sees: %+v", list)
	}
	id := list.Transfers[0].ID
	got, err := b.s.ReceiveTransfer(id, []TransferQty{{SKU: "SKU-M1", QtyMilli: 9000}}, b.p)
	if err != nil {
		t.Fatal(err)
	}
	if got.ShortMilli != 1000 || stockOf(t, b.s, "SKU-M1") != 9000 {
		t.Fatalf("received: short %d stock %d", got.ShortMilli, stockOf(t, b.s, "SKU-M1"))
	}
	// receiving again on the same till adds nothing; another till is refused
	if _, err := b.s.ReceiveTransfer(id, nil, b.p); err != nil {
		t.Fatal(err)
	}
	if stockOf(t, b.s, "SKU-M1") != 9000 {
		t.Fatal("double receive added stock")
	}
	if _, err := b2.s.ReceiveTransfer(id, nil, b2.p); err == nil {
		t.Fatal("second till received the same transfer")
	}
	// too late to cancel
	if _, err := a.s.CancelTransfer(tr.ClientRef, "TEAM-B", a.p); err == nil {
		t.Fatal("cancelled a received transfer")
	}
	// ledger kinds + KRA movement codes
	var kinds []string
	rows, _ := a.s.db.Query(`SELECT kind FROM stock_movements WHERE reference = ?`, tr.ClientRef)
	for rows.Next() {
		var k string
		rows.Scan(&k)
		kinds = append(kinds, k)
	}
	rows.Close()
	if strings.Join(kinds, ",") != models.MoveTransferOut || sarTypeFor(models.MoveTransferOut, -1) != "13" || sarTypeFor(models.MoveTransferIn, 1) != "04" {
		t.Fatalf("ledger kinds %v", kinds)
	}
}

func TestTransferOfflineQueueAndCancel(t *testing.T) {
	cloud := newTransferCloud()
	a := newShopTill(t, cloud, "TEAM-A", "dev-a1")
	b := newShopTill(t, cloud, "TEAM-B", "dev-b1")
	cloud.down = true
	tr, err := a.s.SendTransfer(sendReq(a.s, "TEAM-B", "SKU-M1", 5000), a.p)
	if err != nil {
		t.Fatal(err)
	}
	if tr.Status != "queued" || stockOf(t, a.s, "SKU-M1") != 45000 {
		t.Fatalf("offline send: %s %d", tr.Status, stockOf(t, a.s, "SKU-M1"))
	}
	if _, err := a.s.CancelTransfer(tr.ClientRef, "TEAM-B", a.p); err == nil {
		t.Fatal("cancel must need the cloud")
	}
	cloud.down = false
	a.s.PushTransfers()
	if lt := a.s.localTransfer(tr.ClientRef); lt.Status != "sent" {
		t.Fatalf("after push: %s", lt.Status)
	}
	if _, err := a.s.CancelTransfer(tr.ClientRef, "TEAM-B", a.p); err != nil {
		t.Fatal(err)
	}
	if stockOf(t, a.s, "SKU-M1") != 50000 {
		t.Fatalf("cancel did not restore: %d", stockOf(t, a.s, "SKU-M1"))
	}
	// a second cancel / a reconcile restores nothing more
	_, _ = a.s.CancelTransfer(tr.ClientRef, "TEAM-B", a.p)
	_, _ = a.s.ListTransfers()
	if stockOf(t, a.s, "SKU-M1") != 50000 {
		t.Fatal("restored twice")
	}
	list, _ := b.s.ListTransfers()
	if _, err := b.s.ReceiveTransfer(list.Transfers[0].ID, nil, b.p); err == nil {
		t.Fatal("received a cancelled transfer")
	}
}

// The send's answer was lost and the owner cancelled: the cancel leaves a
// tombstone, and the late send can never deliver.
func TestTransferCancelBeforeSendArrives(t *testing.T) {
	cloud := newTransferCloud()
	a := newShopTill(t, cloud, "TEAM-A", "dev-a1")
	cloud.down = true
	tr, _ := a.s.SendTransfer(sendReq(a.s, "TEAM-A-not", "SKU-M1", 5000), a.p)
	if tr == nil {
		// unknown shop while offline is queued; online the cloud refuses
		t.Fatal("offline send to unchecked shop should queue")
	}
	cloud.down = false
	a.s.PushTransfers() // cloud refuses for good: goods come back
	if lt := a.s.localTransfer(tr.ClientRef); lt.Status != "rejected" || stockOf(t, a.s, "SKU-M1") != 50000 {
		t.Fatalf("refused transfer: %s %d", lt.Status, stockOf(t, a.s, "SKU-M1"))
	}

	cloud.down = true
	tr2, _ := a.s.SendTransfer(sendReq(a.s, "TEAM-B", "SKU-M1", 3000), a.p)
	cloud.down = false
	if _, err := a.s.CancelTransfer(tr2.ClientRef, "TEAM-B", a.p); err != nil {
		t.Fatal(err)
	}
	a.s.PushTransfers() // the late send finds the tombstone
	if stockOf(t, a.s, "SKU-M1") != 50000 {
		t.Fatalf("stock after tombstone: %d", stockOf(t, a.s, "SKU-M1"))
	}
	for _, r := range cloud.rows {
		if r.ClientRef == tr2.ClientRef && r.Status != "cancelled" {
			t.Fatal("late send revived a cancelled transfer")
		}
	}
}

// A crash between the cloud claim and the local commit is finished by the
// next list on that till.
func TestTransferReceiveCrashRecovery(t *testing.T) {
	cloud := newTransferCloud()
	a := newShopTill(t, cloud, "TEAM-A", "dev-a1")
	b := newShopTill(t, cloud, "TEAM-B", "dev-b1")
	if _, err := a.s.SendTransfer(sendReq(a.s, "TEAM-B", "SKU-M1", 2000), a.p); err != nil {
		t.Fatal(err)
	}
	// claim directly in the cloud as b would, without the local apply
	var ans transferAnswer
	_ = cloud.rpcFor("dev-b1")("transfer_receive", map[string]any{"p_id": cloud.rows[0].ID, "p_received": []TransferQty{{SKU: "SKU-M1", QtyMilli: 2000}}}, &ans)
	if stockOf(t, b.s, "SKU-M1") != 50000 {
		t.Fatal("precondition")
	}
	_, _ = b.s.ListTransfers()
	_, _ = b.s.ListTransfers()
	if stockOf(t, b.s, "SKU-M1") != 52000 {
		t.Fatalf("recovery: %d", stockOf(t, b.s, "SKU-M1"))
	}
}

func TestTransferNotLinked(t *testing.T) {
	f := newMoneyTill(t)
	if _, err := f.s.SendTransfer(SendTransferRequest{ToTeam: "X"}, f.principal()); !errors.Is(err, ErrTransfersOffline) {
		t.Fatalf("unlinked till: %v", err)
	}
	l, _ := f.s.ListTransfers()
	if l.Online || l.Error == "" {
		t.Fatal("unlinked till must say why")
	}
}

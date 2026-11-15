package services

// accounting.go — the books for QuickBooks Online and Xero (v1.12.0).
//
// The till produces ONE balanced journal entry per day, the way POS
// systems hand off to an accountant (a daily sales summary, not every
// receipt):
//
//   Sales (paid sales and tab sales, by the day they were rung up)
//     Dr  each way it was paid: Cash, M-Pesa, Card (Paystack), Accounts
//         receivable (tab), Store credit (prepaid credit spent)
//     Cr  Sales (excluding VAT and gift cards)
//     Cr  VAT output
//     Cr  Gift cards (a gift card sold is owed to the holder until spent)
//   Returns:            Dr Sales + Dr VAT   Cr Cash / Store credit
//   Cost of goods:      Dr Cost of sales    Cr Inventory  (less goods returned)
//   Write-offs:         Dr Stock write-offs Cr Inventory
//   Expenses:           Dr Expenses         Cr Cash / M-Pesa / Bank
//   Tab payments:       Dr Tab receipts     Cr Accounts receivable
//   Credit top-ups:     Dr Tab receipts     Cr Store credit
//   Gift card -> credit Dr Gift cards       Cr Store credit
//   Tab write-offs:     Dr Bad debts        Cr Accounts receivable
//   Opening tab debt:   Dr Accounts receivable  Cr Opening balance equity
//
// Every entry balances to the cent; any cent difference between what the
// sales came to and what was recorded as paid goes to Rounding, shown in
// the preview so the owner sees it. Account names (QuickBooks matches by
// name) and codes (Xero matches by code) are editable in Settings.
//
// A sale voided later disappears from its day: export days that are closed.

import (
	"encoding/csv"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"
)

type AccountRef struct {
	Name string `json:"name"` // QuickBooks: the account's exact name
	Code string `json:"code"` // Xero: the account code
}

var accountingKeys = []struct {
	Key, Label string
	Default    AccountRef
}{
	{"cash", "Cash in the till", AccountRef{"Cash on hand", "CASH"}},
	{"mpesa", "M-Pesa", AccountRef{"M-Pesa", "MPESA"}},
	{"card", "Card / Paystack", AccountRef{"Paystack clearing", "CARD"}},
	{"bank", "Bank (expenses paid by bank)", AccountRef{"Bank", "090"}},
	{"tab_receipts", "Where tab payments and credit top-ups go", AccountRef{"Cash on hand", "CASH"}},
	{"receivable", "Customer tabs (accounts receivable)", AccountRef{"Accounts Receivable (A/R)", "610"}},
	{"store_credit", "Store credit owed to customers", AccountRef{"Customer deposits", "CREDIT"}},
	{"gift_cards", "Gift cards not yet spent", AccountRef{"Gift cards outstanding", "GIFT"}},
	{"sales", "Sales", AccountRef{"Sales", "200"}},
	{"vat", "VAT output", AccountRef{"VAT Payable", "820"}},
	{"cogs", "Cost of goods sold", AccountRef{"Cost of Goods Sold", "310"}},
	{"inventory", "Inventory", AccountRef{"Inventory Asset", "630"}},
	{"writeoffs", "Stock written off", AccountRef{"Stock write-offs", "STOCKLOSS"}},
	{"expenses", "Expenses", AccountRef{"Office expenses", "429"}},
	{"bad_debts", "Tab write-offs (bad debts)", AccountRef{"Bad debts", "BADDEBT"}},
	{"opening", "Opening balances", AccountRef{"Opening Balance Equity", "OPENING"}},
	{"rounding", "Rounding", AccountRef{"Rounding", "ROUND"}},
}

type AccountingAccount struct {
	Key   string `json:"key"`
	Label string `json:"label"`
	AccountRef
}

// AccountingAccounts returns the account map (saved values over defaults).
func (s *Service) AccountingAccounts() []AccountingAccount {
	saved := map[string]AccountRef{}
	_ = json.Unmarshal([]byte(s.settings.Get("accounting_accounts")), &saved)
	out := make([]AccountingAccount, 0, len(accountingKeys))
	for _, k := range accountingKeys {
		ref := k.Default
		if v, ok := saved[k.Key]; ok {
			if strings.TrimSpace(v.Name) != "" {
				ref.Name = strings.TrimSpace(v.Name)
			}
			if strings.TrimSpace(v.Code) != "" {
				ref.Code = strings.TrimSpace(v.Code)
			}
		}
		out = append(out, AccountingAccount{Key: k.Key, Label: k.Label, AccountRef: ref})
	}
	return out
}

// SaveAccountingAccounts stores the account map (unknown keys ignored).
func (s *Service) SaveAccountingAccounts(in map[string]AccountRef) error {
	clean := map[string]AccountRef{}
	for _, k := range accountingKeys {
		if v, ok := in[k.Key]; ok {
			v.Name, v.Code = strings.TrimSpace(v.Name), strings.TrimSpace(v.Code)
			if len(v.Name) > 100 || len(v.Code) > 10 || strings.ContainsAny(v.Name+v.Code, "\r\n\"") {
				return fmt.Errorf("%w: %s: names up to 100 characters, codes up to 10, no quotes", ErrInvalidInput, k.Label)
			}
			clean[k.Key] = v
		}
	}
	raw, _ := json.Marshal(clean)
	return s.settings.Set("accounting_accounts", string(raw))
}

type JournalLine struct {
	Account     string `json:"account"` // key
	Name        string `json:"name"`
	Code        string `json:"code"`
	DebitCents  int64  `json:"debitCents"`
	CreditCents int64  `json:"creditCents"`
	Memo        string `json:"memo"`
}

type JournalDay struct {
	Date          string        `json:"date"`
	Number        string        `json:"number"`
	Lines         []JournalLine `json:"lines"`
	DebitCents    int64         `json:"debitCents"`
	CreditCents   int64         `json:"creditCents"`
	RoundingCents int64         `json:"roundingCents"`
}

type Journal struct {
	From string       `json:"from"`
	To   string       `json:"to"`
	Days []JournalDay `json:"days"`
}

const maxJournalDays = 400

type jb struct {
	acc   map[string]AccountRef
	lines []JournalLine
}

func (b *jb) dr(key string, cents int64, memo string) {
	if cents == 0 {
		return
	}
	if cents < 0 {
		b.cr(key, -cents, memo)
		return
	}
	a := b.acc[key]
	b.lines = append(b.lines, JournalLine{Account: key, Name: a.Name, Code: a.Code, DebitCents: cents, Memo: memo})
}

func (b *jb) cr(key string, cents int64, memo string) {
	if cents == 0 {
		return
	}
	if cents < 0 {
		b.dr(key, -cents, memo)
		return
	}
	a := b.acc[key]
	b.lines = append(b.lines, JournalLine{Account: key, Name: a.Name, Code: a.Code, CreditCents: cents, Memo: memo})
}

// Journal builds the daily entries for [from, to] (YYYY-MM-DD, Nairobi days).
func (s *Service) Journal(from, to string) (*Journal, error) {
	if !validDay(from) || !validDay(to) {
		return nil, fmt.Errorf("%w: dates must be YYYY-MM-DD", ErrInvalidInput)
	}
	if to < from {
		from, to = to, from
	}
	start, _ := time.ParseInLocation("2006-01-02", from, eastAfrica)
	end, _ := time.ParseInLocation("2006-01-02", to, eastAfrica)
	if end.Sub(start) > maxJournalDays*24*time.Hour {
		return nil, fmt.Errorf("%w: export at most %d days at a time", ErrInvalidInput, maxJournalDays)
	}
	acc := map[string]AccountRef{}
	for _, a := range s.AccountingAccounts() {
		acc[a.Key] = a.AccountRef
	}
	out := &Journal{From: from, To: to, Days: []JournalDay{}}
	for d := start; !d.After(end); d = d.AddDate(0, 0, 1) {
		day, err := s.journalDay(d.Format("2006-01-02"), acc)
		if err != nil {
			return nil, err
		}
		if len(day.Lines) > 0 {
			out.Days = append(out.Days, *day)
		}
	}
	return out, nil
}

// salesCond picks the day's sales: paid, or charged to a tab (the goods
// left; the debt is in receivables until the tab is paid).
const salesCond = `o.created_at >= ? AND o.created_at < ? AND (o.status = 'PAID'
	OR (o.status = 'PENDING' AND EXISTS (SELECT 1 FROM payments tp WHERE tp.order_id = o.id AND tp.method = 'account' AND tp.status != 'VOIDED')))`

func (s *Service) journalDay(date string, acc map[string]AccountRef) (*JournalDay, error) {
	from, to := dayBounds(date)
	b := &jb{acc: acc}
	q1 := func(q string, args ...any) int64 {
		var v any
		if err := s.db.QueryRow(s.db.Rebind(q), args...).Scan(&v); err != nil {
			return 0
		}
		return asInt64(v)
	}

	// ---- sales ----
	var total, tax, count int64
	_ = s.db.QueryRow(s.db.Rebind(`SELECT COALESCE(SUM(o.total_cents),0), COALESCE(SUM(o.tax_cents),0), COUNT(*) FROM orders o WHERE `+salesCond), from, to).
		Scan(&total, &tax, &count)
	gift := q1(`SELECT COALESCE(SUM(CASE WHEN COALESCE(oi.paid_cents,0) > 0 THEN oi.paid_cents ELSE oi.line_total_cents END),0)
		FROM order_items oi JOIN orders o ON o.id = oi.order_id LEFT JOIN products p ON p.id = oi.product_id
		WHERE `+salesCond+` AND (COALESCE(oi.tax_type,'') = 'X' OR COALESCE(p.is_gift_card,0) = 1)`, from, to)
	if count > 0 {
		memo := fmt.Sprintf("%d sales", count)
		tenders := map[string]int64{}
		rows, err := s.db.Query(s.db.Rebind(`SELECT p.method, COALESCE(SUM(p.amount_cents),0) FROM payments p JOIN orders o ON o.id = p.order_id
			WHERE `+salesCond+` AND (p.status = 'COMPLETED' OR (p.method = 'account' AND p.status != 'VOIDED')) GROUP BY p.method`), from, to)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var m string
			var v any
			if rows.Scan(&m, &v) == nil {
				tenders[m] += asInt64(v)
			}
		}
		rows.Close()
		var paid int64
		methods := make([]string, 0, len(tenders))
		for m := range tenders {
			methods = append(methods, m)
		}
		sort.Strings(methods)
		for _, m := range methods {
			v := tenders[m]
			paid += v
			switch m {
			case "mpesa":
				b.dr("mpesa", v, "Sales paid by M-Pesa")
			case "paystack":
				b.dr("card", v, "Sales paid by card")
			case "account":
				b.dr("receivable", v, "Sales on customer tabs")
			case "credit":
				b.dr("store_credit", v, "Sales paid from store credit")
			default:
				b.dr("cash", v, "Sales paid in cash")
			}
		}
		b.cr("sales", total-tax-gift, memo)
		b.cr("vat", tax, "VAT on sales")
		b.cr("gift_cards", gift, "Gift cards sold")
		// a sale recorded as paid with a different total than its legs
		b.dr("rounding", total-paid, "Sales total vs payments recorded")
	}

	// ---- returns ----
	rrows, err := s.db.Query(s.db.Rebind(`SELECT rt.total_cents, COALESCE(rt.method,'CASH'), o.total_cents, o.tax_cents
		FROM returns rt JOIN orders o ON o.id = rt.order_id WHERE rt.created_at >= ? AND rt.created_at < ?`), from, to)
	if err != nil {
		return nil, err
	}
	var retCash, retCredit, retVat, retTotal int64
	for rrows.Next() {
		var rt, ot, otax int64
		var m string
		if rrows.Scan(&rt, &m, &ot, &otax) != nil {
			continue
		}
		v := int64(0)
		if ot > 0 {
			v = (2*rt*otax + ot) / (2 * ot) // the return's share of the sale's VAT, half up
		}
		retVat += v
		retTotal += rt
		if strings.EqualFold(m, "STORE_CREDIT") {
			retCredit += rt
		} else {
			retCash += rt
		}
	}
	rrows.Close()
	b.dr("sales", retTotal-retVat, "Returns")
	b.dr("vat", retVat, "VAT on returns")
	b.cr("cash", retCash, "Refunds in cash")
	b.cr("store_credit", retCredit, "Refunds to store credit")

	// ---- cost of goods ----
	cogs := q1(`SELECT COALESCE(SUM(oi.qty_milli * CASE WHEN oi.cost_cents > 0 THEN oi.cost_cents ELSE COALESCE(p.cost_cents,0) END) / 1000, 0)
		FROM order_items oi JOIN orders o ON o.id = oi.order_id LEFT JOIN products p ON p.id = oi.product_id
		WHERE `+salesCond+` AND COALESCE(oi.tax_type,'') != 'X' AND COALESCE(p.is_gift_card,0) = 0`, from, to)
	back := q1(`SELECT COALESCE(SUM(ri.qty_milli * (CASE WHEN COALESCE(oi.cost_cents,0) > 0 THEN oi.cost_cents ELSE COALESCE(p.cost_cents,0) END) / 1000),0)
		FROM return_items ri JOIN returns rt ON rt.id = ri.return_id
		LEFT JOIN order_items oi ON oi.id = ri.order_item_id LEFT JOIN products p ON p.id = ri.product_id
		WHERE rt.created_at >= ? AND rt.created_at < ?`, from, to)
	b.dr("cogs", cogs, "Cost of goods sold")
	b.cr("inventory", cogs, "Cost of goods sold")
	b.dr("inventory", back, "Returned goods back in stock")
	b.cr("cogs", back, "Returned goods back in stock")
	writeoff := q1(`SELECT COALESCE(SUM(-m.delta_milli * COALESCE(p.cost_cents,0)) / 1000, 0)
		FROM stock_movements m LEFT JOIN products p ON p.id = m.product_id
		WHERE m.kind = 'DISPOSAL' AND m.created_at >= ? AND m.created_at < ?`, from, to)
	b.dr("writeoffs", writeoff, "Stock written off")
	b.cr("inventory", writeoff, "Stock written off")

	// ---- expenses ----
	erows, err := s.db.Query(s.db.Rebind(`SELECT COALESCE(t.name,'Expense'), COALESCE(e.method,'CASH'), COALESCE(SUM(e.amount_cents),0)
		FROM expenses e LEFT JOIN expense_types t ON t.id = e.expense_type_id WHERE e.spent_on = ? GROUP BY t.name, e.method ORDER BY t.name, e.method`), date)
	if err != nil {
		return nil, err
	}
	for erows.Next() {
		var name, m string
		var v any
		if erows.Scan(&name, &m, &v) != nil {
			continue
		}
		amt := asInt64(v)
		b.dr("expenses", amt, name)
		switch strings.ToUpper(m) {
		case "MPESA":
			b.cr("mpesa", amt, name+" paid by M-Pesa")
		case "BANK":
			b.cr("bank", amt, name+" paid from the bank")
		default:
			b.cr("cash", amt, name+" paid in cash")
		}
	}
	erows.Close()

	// ---- customer accounts (tabs, store credit) ----
	lrows, err := s.db.Query(s.db.Rebind(`SELECT kind, amount_cents, COALESCE(order_id,0), COALESCE(note,'') FROM customer_ledger
		WHERE created_at >= ? AND created_at < ? AND kind IN ('payment','credit_topup','adjustment','opening')`), from, to)
	if err != nil {
		return nil, err
	}
	var tabPaid, topups, giftToCredit, writeOffs, opening int64
	for lrows.Next() {
		var kind, note string
		var amt, orderID int64
		if lrows.Scan(&kind, &amt, &orderID, &note) != nil {
			continue
		}
		switch kind {
		case "payment":
			tabPaid += -amt // payments are stored negative
		case "credit_topup":
			if orderID != 0 {
				continue // a voided sale's credit given back: no money moved
			}
			if strings.HasPrefix(note, "gift card ") {
				giftToCredit += amt
			} else {
				topups += amt
			}
		case "adjustment":
			if orderID != 0 {
				continue // void reversal of a tab sale that is no longer a sale
			}
			writeOffs += -amt // a negative adjustment forgives debt
		case "opening":
			opening += amt
		}
	}
	lrows.Close()
	b.dr("tab_receipts", tabPaid, "Customer tab payments")
	b.cr("receivable", tabPaid, "Customer tab payments")
	b.dr("tab_receipts", topups, "Store credit top-ups")
	b.cr("store_credit", topups, "Store credit top-ups")
	b.dr("gift_cards", giftToCredit, "Gift cards turned into store credit")
	b.cr("store_credit", giftToCredit, "Gift cards turned into store credit")
	b.dr("bad_debts", writeOffs, "Tab corrections / write-offs")
	b.cr("receivable", writeOffs, "Tab corrections / write-offs")
	b.dr("receivable", opening, "Opening tab balances")
	b.cr("opening", opening, "Opening tab balances")

	day := &JournalDay{Date: date, Number: "POS-" + strings.ReplaceAll(date, "-", ""), Lines: b.lines}
	if day.Lines == nil {
		day.Lines = []JournalLine{}
	}
	for _, l := range day.Lines {
		day.DebitCents += l.DebitCents
		day.CreditCents += l.CreditCents
		if l.Account == "rounding" {
			day.RoundingCents += l.DebitCents - l.CreditCents
		}
	}
	if day.DebitCents != day.CreditCents {
		// cannot happen (every line above is paired) — refuse rather than
		// hand the accountant an entry their software rejects
		return nil, fmt.Errorf("journal for %s does not balance (%d vs %d)", date, day.DebitCents, day.CreditCents)
	}
	return day, nil
}

// JournalCSV renders the journal for import: format quickbooks | xero | detail.
func (s *Service) JournalCSV(from, to, format string) (string, string, error) {
	j, err := s.Journal(from, to)
	if err != nil {
		return "", "", err
	}
	dateFmt := "02/01/2006"
	if s.settings.Get("accounting_date_format") == "yyyy-mm-dd" {
		dateFmt = "2006-01-02"
	} else if s.settings.Get("accounting_date_format") == "mm/dd/yyyy" {
		dateFmt = "01/02/2006"
	}
	when := func(d string) string {
		t, _ := time.Parse("2006-01-02", d)
		return t.Format(dateFmt)
	}
	money := func(c int64) string {
		if c == 0 {
			return ""
		}
		return centsStr(c)
	}
	var b strings.Builder
	w := csv.NewWriter(&b)
	name := fmt.Sprintf("ledgerpos-%s-%s-%s.csv", format, from, to)
	switch format {
	case "quickbooks":
		// QuickBooks Online: Settings → Import data → Journal entries.
		// Rows sharing a Journal No. are one entry; accounts match by name.
		_ = w.Write([]string{"Journal No", "Journal Date", "Account Name", "Debits", "Credits", "Description", "Name"})
		for _, d := range j.Days {
			for _, l := range d.Lines {
				_ = w.Write([]string{d.Number, when(d.Date), l.Name, money(l.DebitCents), money(l.CreditCents), csvSafe(l.Memo), ""})
			}
		}
	case "xero":
		// Xero: Accounting → Manual journals → Import. Debits positive,
		// credits negative; rows with the same Narration + Date are one
		// journal; accounts match by code.
		taxRate := strings.TrimSpace(s.settings.Get("accounting_xero_tax_rate"))
		if taxRate == "" {
			taxRate = "Tax Exempt"
		}
		_ = w.Write([]string{"*Narration", "*Date", "Description", "*AccountCode", "*TaxRate", "*Amount",
			"TrackingName1", "TrackingOption1", "TrackingName2", "TrackingOption2"})
		for _, d := range j.Days {
			for _, l := range d.Lines {
				amt := centsStr(l.DebitCents - l.CreditCents)
				_ = w.Write([]string{"LedgerPOS daily sales " + d.Number, when(d.Date), csvSafe(l.Memo), l.Code, taxRate, amt, "", "", "", ""})
			}
		}
	case "detail":
		_ = w.Write([]string{"Date", "Entry", "Account key", "Account name", "Account code", "Debit", "Credit", "Memo"})
		for _, d := range j.Days {
			for _, l := range d.Lines {
				_ = w.Write([]string{d.Date, d.Number, l.Account, l.Name, l.Code, money(l.DebitCents), money(l.CreditCents), csvSafe(l.Memo)})
			}
		}
	default:
		return "", "", fmt.Errorf("%w: format must be quickbooks, xero or detail", ErrInvalidInput)
	}
	w.Flush()
	return b.String(), name, w.Error()
}

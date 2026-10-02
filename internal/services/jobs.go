package services

// jobs.go — the connected job lifecycle (P3) and WhatsApp customer contact
// (P4). A sale can spawn fulfilment jobs; every job move, attachment and
// WhatsApp contact lands on the order's activity timeline so anyone in the
// shop can answer "where is my order?" from one place.

import (
	"fmt"
	"net/url"
	"strings"

	"posapp/internal/auth"
	"posapp/internal/models"
)

// AssigneeOption is one choice in the job-assignment dropdown.
type AssigneeOption struct {
	ID       int64  `json:"id"`
	Name     string `json:"name"`
	RoleName string `json:"roleName"`
}

// ListAssignees returns active staff for the assignment dropdown —
// available to everyone holding orders.assign (no users.manage needed).
func (s *Service) ListAssignees() []AssigneeOption {
	rows, err := s.db.Query(`
		SELECT u.id, COALESCE(NULLIF(u.full_name,''), u.username), COALESCE(r.name,'')
		FROM users u LEFT JOIN roles r ON r.id = u.role_id
		WHERE u.is_active = 1
		ORDER BY r.name, u.id`)
	if err != nil {
		return []AssigneeOption{}
	}
	defer rows.Close()
	out := []AssigneeOption{}
	for rows.Next() {
		var a AssigneeOption
		if err := rows.Scan(&a.ID, &a.Name, &a.RoleName); err == nil {
			out = append(out, a)
		}
	}
	return out
}

// AddOrderEvent appends one line to an order's activity timeline
// (best-effort: the order may not exist locally on a synced device).
func (s *Service) AddOrderEvent(orderID int64, kind, message string, p *auth.Principal) {
	if orderID <= 0 || strings.TrimSpace(message) == "" {
		return
	}
	uid, uname := int64(0), ""
	if p != nil {
		uid, uname = p.ID, p.Username
	}
	s.db.Exec(s.db.Rebind(`
		INSERT INTO order_events (order_id, kind, message, user_id, username, created_at)
		VALUES (?, ?, ?, ?, ?, ?)`), orderID, kind, truncStr(message, 400), uid, uname, nowStamp())
}

// GetOrderEvents returns an order's activity timeline, newest last.
func (s *Service) GetOrderEvents(orderID int64) []models.OrderEvent {
	rows, err := s.db.Query(s.db.Rebind(`
		SELECT id, order_id, kind, message, user_id, username, created_at
		FROM order_events WHERE order_id = ? ORDER BY id ASC LIMIT 500`), orderID)
	if err != nil {
		return []models.OrderEvent{}
	}
	defer rows.Close()
	out := []models.OrderEvent{}
	for rows.Next() {
		var e models.OrderEvent
		if err := rows.Scan(&e.ID, &e.OrderID, &e.Kind, &e.Message, &e.UserID, &e.Username, &e.CreatedAt); err == nil {
			out = append(out, e)
		}
	}
	return out
}

// CreateJobFromOrder turns a paid sale into a tracked design job, carrying
// the order reference, customer and notes across. One order can have
// several jobs (e.g. a print run + an artwork setup).
func (s *Service) CreateJobFromOrder(p *auth.Principal, orderID int64, in DesignJobInput) (*models.DesignJob, error) {
	order, err := s.GetOrder(orderID)
	if err != nil {
		return nil, ErrNotFound
	}
	if in.Title == "" {
		in.Title = "Job for " + order.Number
	}
	in.OrderID = orderID
	if in.CustomerName == "" {
		in.CustomerName = order.CustomerName
	}
	if in.ProductName == "" && len(order.Items) > 0 {
		names := make([]string, 0, len(order.Items))
		for _, it := range order.Items {
			names = append(names, it.Name)
		}
		in.ProductName = strings.Join(names, ", ")
	}
	job, err := s.CreateDesignJob(p, in)
	if err != nil {
		return nil, err
	}
	// Mark the order as taken by the fulfilment team.
	s.db.Exec(s.db.Rebind(`UPDATE orders SET assigned_to_id = ? WHERE id = ?`),
		job.AssigneeID, orderID)
	s.AddOrderEvent(orderID, models.OrderEventJob,
		fmt.Sprintf("Job #%d created and assigned to %s", job.ID, job.AssigneeName), p)
	return job, nil
}

// ---- WhatsApp customer contact (P4) ----

// whatsappReadyMessage renders the prefilled message a staff member
// reviews before sending. The store name and pickup note come from
// settings — owners can tune the wording per shop.
func (s *Service) whatsappReadyMessage(order *models.Order, jobTitle string) string {
	store := s.settings.Get("store_name")
	if store == "" {
		store = s.settings.Get("app_name")
		if store == "" {
			store = "our shop"
		}
	}
	pickup := s.settings.Get("whatsapp_ready_note")
	msg := fmt.Sprintf("Hello! Your order %s at %s is ready", order.Number, store)
	if jobTitle != "" {
		msg += fmt.Sprintf(" (%s)", jobTitle)
	}
	msg += "."
	if pickup != "" {
		msg += " " + pickup
	}
	return msg
}

// NotifyReady prepares the "Contact customer on WhatsApp" action for a
// job's order: resolves the customer's phone (CRM record first, then the
// payment leg's phone), builds the wa.me deep link with the prefilled
// message, and logs the ATTEMPT on the timeline. Staff always review and
// press send — the app never auto-sends and never claims delivery.
func (s *Service) NotifyReady(p *auth.Principal, jobID, orderID int64) (*models.WhatsAppContact, error) {
	var job *models.DesignJob
	if jobID > 0 {
		j, err := s.GetDesignJob(jobID)
		if err != nil {
			return nil, ErrNotFound
		}
		job = j
		orderID = job.OrderID
	}
	if orderID <= 0 {
		return nil, fmt.Errorf("this job is not linked to a sale — add the customer's phone and try again")
	}
	order, err := s.GetOrder(orderID)
	if err != nil {
		return nil, ErrNotFound
	}
	phone := ""
	if order.CustomerID != 0 {
		s.db.QueryRow(s.db.Rebind(`SELECT phone FROM customers WHERE id = ?`), order.CustomerID).Scan(&phone)
	}
	if phone == "" {
		// Fall back to the payment leg (M-Pesa STK captured a phone).
		s.db.QueryRow(s.db.Rebind(`SELECT COALESCE(phone,'') FROM payments WHERE order_id = ? AND phone != '' ORDER BY id DESC LIMIT 1`), orderID).Scan(&phone)
	}
	if phone == "" && order.CustomerName != "" {
		s.db.QueryRow(s.db.Rebind(`SELECT phone FROM customers WHERE name = ? AND phone != '' ORDER BY id DESC LIMIT 1`), order.CustomerName).Scan(&phone)
	}
	phone = normalizeKenyanPhone(phone)
	if phone == "" {
		return nil, fmt.Errorf("no phone number on this order — record the customer's phone first")
	}
	msg := s.whatsappReadyMessage(order, jobTitle(job))
	link := fmt.Sprintf("https://wa.me/%s?text=%s", phone, url.QueryEscape(msg))
	s.db.Exec(s.db.Rebind(`UPDATE orders SET notified_at = ? WHERE id = ?`), nowStamp(), orderID)
	kind := models.OrderEventWhatsApp
	name := ""
	if job != nil {
		name = job.Title
	}
	s.AddOrderEvent(orderID, kind,
		fmt.Sprintf("WhatsApp prepared for %s — staff review-and-send%s", phone, jobSuffix(name)), p)
	s.Audit(p.ID, p.Username, "WHATSAPP_READY_SENT", "order", order.Number, phone)
	return &models.WhatsAppContact{
		OrderID:   orderID,
		Phone:     phone,
		Message:   msg,
		URL:       link,
		OrderRef:  order.Number,
		NotifiedAt: nowStamp(),
	}, nil
}

func jobTitle(j *models.DesignJob) string {
	if j == nil {
		return ""
	}
	return j.Title
}

func jobSuffix(jobName string) string {
	if jobName == "" {
		return ""
	}
	return " (" + jobName + ")"
}

// normalizeKenyanPhone coerces common local shapes (07…, 7…, 2547…,
// +2547…) into the wa.me-friendly 2547XXXXXXXX string. Empty in, empty out.
func normalizeKenyanPhone(in string) string {
	p := strings.Map(func(r rune) rune {
		if r >= '0' && r <= '9' {
			return r
		}
		return -1
	}, strings.TrimSpace(in))
	switch {
	case len(p) == 10 && strings.HasPrefix(p, "0"):
		return "254" + p[1:]
	case len(p) == 9 && (strings.HasPrefix(p, "7") || strings.HasPrefix(p, "1")):
		return "254" + p
	case len(p) == 12 && strings.HasPrefix(p, "254"):
		return p
	case len(p) == 13 && strings.HasPrefix(p, "254"):
		return strings.TrimPrefix(p, "2540")
	default:
		if len(p) >= 9 {
			return p // international numbers pass through as-is
		}
		return ""
	}
}

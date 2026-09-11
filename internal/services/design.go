package services

import (
	"database/sql"
	"errors"
	"fmt"
	"time"

	"posapp/internal/auth"
	"posapp/internal/models"
)

var validDesignStatus = map[string]bool{
	models.DesignQueue: true, models.DesignInProgress: true,
	models.DesignReady: true, models.DesignDelivered: true,
}

// DesignJobInput creates/updates design board entries (the designer role's
// workspace: t-shirt artwork, branding runs, custom merch).
type DesignJobInput struct {
	Title        string `json:"title" binding:"required"`
	ProductName  string `json:"productName"`
	CustomerName string `json:"customerName"`
	Notes        string `json:"notes"`
	Status       string `json:"status"`
	AssigneeID   int64  `json:"assigneeId"`
}

func (s *Service) CreateDesignJob(p *auth.Principal, in DesignJobInput) (*models.DesignJob, error) {
	status := in.Status
	if status == "" {
		status = models.DesignQueue
	}
	if !validDesignStatus[status] {
		return nil, fmt.Errorf("invalid design status %q", status)
	}
	var assignee any
	if in.AssigneeID != 0 {
		assignee = in.AssigneeID
	}
	res, err := s.db.Exec(s.db.Rebind(`
		INSERT INTO design_jobs (title, product_name, customer_name, notes, status, assignee_id, created_by)
		VALUES (?, ?, ?, ?, ?, ?, ?)`),
		in.Title, in.ProductName, in.CustomerName, in.Notes, status, assignee, p.Username)
	if err != nil {
		return nil, err
	}
	id, _ := res.LastInsertId()
	s.Audit(p.ID, p.Username, "DESIGN_CREATED", "design_job", fmt.Sprint(id), in.Title)
	job, err := s.GetDesignJob(id)
	if err == nil {
		s.broadcast(EventDesignUpdate, job)
	}
	return job, err
}

func (s *Service) UpdateDesignJob(p *auth.Principal, id int64, in DesignJobInput) (*models.DesignJob, error) {
	current, err := s.GetDesignJob(id)
	if err != nil {
		return nil, err
	}
	status := in.Status
	if status == "" {
		status = current.Status
	}
	if !validDesignStatus[status] {
		return nil, fmt.Errorf("invalid design status %q", status)
	}
	var assignee any
	if in.AssigneeID != 0 {
		assignee = in.AssigneeID
	} else if current.AssigneeID != 0 {
		assignee = current.AssigneeID
	}
	_, err = s.db.Exec(s.db.Rebind(`
		UPDATE design_jobs SET title = ?, product_name = ?, customer_name = ?, notes = ?, status = ?, assignee_id = ?, updated_at = ?
		WHERE id = ?`), in.Title, in.ProductName, in.CustomerName, in.Notes, status, assignee,
		time.Now().UTC().Format(time.RFC3339), id)
	if err != nil {
		return nil, err
	}
	s.Audit(p.ID, p.Username, "DESIGN_UPDATED", "design_job", fmt.Sprint(id), status)
	job, err := s.GetDesignJob(id)
	if err == nil {
		s.broadcast(EventDesignUpdate, job)
	}
	return job, err
}

// MoveDesignJob transitions status only (kanban drag).
func (s *Service) MoveDesignJob(p *auth.Principal, id int64, status string) (*models.DesignJob, error) {
	if !validDesignStatus[status] {
		return nil, fmt.Errorf("invalid design status %q", status)
	}
	res, err := s.db.Exec(s.db.Rebind(`UPDATE design_jobs SET status = ?, updated_at = ? WHERE id = ?`),
		status, time.Now().UTC().Format(time.RFC3339), id)
	if err != nil {
		return nil, err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return nil, ErrNotFound
	}
	s.Audit(p.ID, p.Username, "DESIGN_MOVED", "design_job", fmt.Sprint(id), status)
	job, err := s.GetDesignJob(id)
	if err == nil {
		s.broadcast(EventDesignUpdate, job)
	}
	return job, err
}

func (s *Service) GetDesignJob(id int64) (*models.DesignJob, error) {
	var j models.DesignJob
	var createdRaw, updatedRaw string
	var assigneeID sql.NullInt64
	err := s.db.QueryRow(s.db.Rebind(`
		SELECT d.id, d.title, COALESCE(d.product_name,''), COALESCE(d.customer_name,''), COALESCE(d.notes,''),
			d.status, d.assignee_id, COALESCE(u.full_name, u.username, ''), COALESCE(d.created_by,''),
			d.created_at, d.updated_at
		FROM design_jobs d LEFT JOIN users u ON u.id = d.assignee_id
		WHERE d.id = ?`), id).
		Scan(&j.ID, &j.Title, &j.ProductName, &j.CustomerName, &j.Notes, &j.Status, &assigneeID,
			&j.AssigneeName, &j.CreatedBy, &createdRaw, &updatedRaw)
	if err != nil {
		return nil, ErrNotFound
	}
	if assigneeID.Valid {
		j.AssigneeID = assigneeID.Int64
	}
	j.CreatedAt = normTime(createdRaw)
	j.UpdatedAt = normTime(updatedRaw)
	return &j, nil
}

func (s *Service) ListDesignJobs(status string) []models.DesignJob {
	q := `
		SELECT d.id, d.title, COALESCE(d.product_name,''), COALESCE(d.customer_name,''), COALESCE(d.notes,''),
			d.status, d.assignee_id, COALESCE(u.full_name, u.username, ''), COALESCE(d.created_by,''),
			d.created_at, d.updated_at
		FROM design_jobs d LEFT JOIN users u ON u.id = d.assignee_id`
	var args []any
	if status != "" {
		q += ` WHERE d.status = ?`
		args = append(args, status)
	}
	q += ` ORDER BY d.id DESC LIMIT 200`
	rows, err := s.db.Query(s.db.Rebind(q), args...)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []models.DesignJob
	for rows.Next() {
		var j models.DesignJob
		var assigneeID sql.NullInt64
		var createdRaw, updatedRaw string
		if err := rows.Scan(&j.ID, &j.Title, &j.ProductName, &j.CustomerName, &j.Notes, &j.Status,
			&assigneeID, &j.AssigneeName, &j.CreatedBy, &createdRaw, &updatedRaw); err != nil {
			return out
		}
		if assigneeID.Valid {
			j.AssigneeID = assigneeID.Int64
		}
		j.CreatedAt = normTime(createdRaw)
		j.UpdatedAt = normTime(updatedRaw)
		out = append(out, j)
	}
	return out
}

var _ = errors.New

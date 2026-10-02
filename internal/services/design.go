package services

import (
        "database/sql"
        "fmt"
        "strings"

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
        Deadline     string `json:"deadline"`
        Priority     string `json:"priority"`
        OrderID      int64  `json:"orderId"`
}

// canDelegate reports whether the principal may see the whole board and
// (re)assign work: owners, front desk, branding and cyber hold
// orders.assign. Designers do not — they see only their own jobs.
func canDelegate(p *auth.Principal) bool { return p.Can("orders.assign") }

// assertJobAccess enforces the designer scope server-side: without
// orders.assign a user may only touch jobs assigned to them. The UI hides
// the buttons; this is the part that actually matters.
func assertJobAccess(p *auth.Principal, job *models.DesignJob) error {
        if canDelegate(p) {
                return nil
        }
        if job.AssigneeID != p.ID {
                return fmt.Errorf("this job is assigned to someone else")
        }
        return nil
}

func (s *Service) CreateDesignJob(p *auth.Principal, in DesignJobInput) (*models.DesignJob, error) {
        if !canDelegate(p) {
                return nil, fmt.Errorf("only the front desk and owners can create jobs — designers work the jobs assigned to them")
        }
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
        assignedAt := ""
        if in.AssigneeID != 0 {
                assignedAt = nowStamp()
        }
        res, err := s.db.Exec(s.db.Rebind(`
                INSERT INTO design_jobs (title, product_name, customer_name, notes, status, assignee_id, created_by, order_id, deadline_at, priority, assigned_at)
                VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`),
                in.Title, in.ProductName, in.CustomerName, in.Notes, status, assignee, p.Username, in.OrderID,
                in.Deadline, normPriority(in.Priority), assignedAt)
        if err != nil {
                return nil, err
        }
        id, _ := res.LastInsertId()
        s.Audit(p.ID, p.Username, "DESIGN_CREATED", "design_job", fmt.Sprint(id), in.Title)
        job, err := s.GetDesignJob(id)
        if err == nil {
                s.broadcast(EventDesignUpdate, job)
                s.EmitDesign(job.Title, job.ProductName, job.CustomerName, job.Notes, job.Status)
                if job.OrderID != 0 {
                        s.AddOrderEvent(job.OrderID, models.OrderEventJob, "Job created: "+in.Title, p)
                }
        }
        return job, err
}

func (s *Service) UpdateDesignJob(p *auth.Principal, id int64, in DesignJobInput) (*models.DesignJob, error) {
        current, err := s.GetDesignJob(id)
        if err != nil {
                return nil, err
        }
        if err := assertJobAccess(p, current); err != nil {
                return nil, err
        }
        status := in.Status
        if status == "" {
                status = current.Status
        }
        if !validDesignStatus[status] {
                return nil, fmt.Errorf("invalid design status %q", status)
        }
        // Reassignment is delegation: only orders.assign holders may change the
        // assignee. Everyone else silently keeps the current one.
        newAssignee := current.AssigneeID
        if canDelegate(p) {
                if in.AssigneeID != 0 {
                        newAssignee = in.AssigneeID
                }
        }
        var assignee any
        if newAssignee != 0 {
                assignee = newAssignee
        }
        assignedAt := current.AssignedAt
        if newAssignee != 0 && newAssignee != current.AssigneeID {
                assignedAt = nowStamp()
        }
        _, err = s.db.Exec(s.db.Rebind(`
                UPDATE design_jobs SET title = ?, product_name = ?, customer_name = ?, notes = ?, status = ?, assignee_id = ?, updated_at = ?,
                        deadline_at = ?, priority = ?, assigned_at = ?
                WHERE id = ?`), in.Title, in.ProductName, in.CustomerName, in.Notes, status, assignee,
                nowStamp(), in.Deadline, normPriority(in.Priority), assignedAt, id)
        if err != nil {
                return nil, err
        }
        s.Audit(p.ID, p.Username, "DESIGN_UPDATED", "design_job", fmt.Sprint(id), status)
        job, err := s.GetDesignJob(id)
        if err == nil {
                s.broadcast(EventDesignUpdate, job)
                s.EmitDesign(job.Title, job.ProductName, job.CustomerName, job.Notes, job.Status)
                if job.OrderID != 0 && job.Status != current.Status {
                        s.AddOrderEvent(job.OrderID, models.OrderEventJob, "Job "+job.Status+": "+in.Title, p)
                }
        }
        return job, err
}

// MoveDesignJob transitions status only (kanban drag). Designers can move
// THEIR jobs through the board — that is their progress reporting — but
// nobody may touch another person's job without orders.assign.
func (s *Service) MoveDesignJob(p *auth.Principal, id int64, status string) (*models.DesignJob, error) {
        if !validDesignStatus[status] {
                return nil, fmt.Errorf("invalid design status %q", status)
        }
        current, err := s.GetDesignJob(id)
        if err != nil {
                return nil, err
        }
        if err := assertJobAccess(p, current); err != nil {
                return nil, err
        }
        now := nowStamp()
        res, err := s.db.Exec(s.db.Rebind(`UPDATE design_jobs SET status = ?, updated_at = ?,
                ready_at = CASE WHEN ? = 'ready' AND ready_at = '' THEN ? ELSE ready_at END,
                collected_at = CASE WHEN ? = 'delivered' AND collected_at = '' THEN ? ELSE collected_at END
                WHERE id = ?`), status, now, status, now, status, now, id)
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
                s.EmitDesign(job.Title, job.ProductName, job.CustomerName, job.Notes, job.Status)
                if job.OrderID != 0 && job.Status != current.Status {
                        s.AddOrderEvent(job.OrderID, models.OrderEventJob, "Job "+job.Status+": "+job.Title, p)
                }
        }
        return job, err
}

// normPriority clamps the free-text priority to the three known levels.
func normPriority(p string) string {
        switch p {
        case "low", "high":
                return p
        default:
                return "normal"
        }
}

const designJobSelect = `
                SELECT d.id, d.title, COALESCE(d.product_name,''), COALESCE(d.customer_name,''), COALESCE(d.notes,''),
                        d.status, d.assignee_id, COALESCE(u.full_name, u.username, ''), COALESCE(d.created_by,''),
                        d.created_at, d.updated_at, COALESCE(d.order_id,0), COALESCE(d.deadline_at,''), COALESCE(d.priority,'normal'),
                        COALESCE(d.assigned_at,''), COALESCE(d.ready_at,''), COALESCE(d.collected_at,'')
                FROM design_jobs d LEFT JOIN users u ON u.id = d.assignee_id`

const designJobColumns = `
                d.id, d.title, COALESCE(d.product_name,''), COALESCE(d.customer_name,''), COALESCE(d.notes,''),
                d.status, d.assignee_id, COALESCE(u.full_name, u.username, ''), COALESCE(d.created_by,''),
                d.created_at, d.updated_at, COALESCE(d.order_id,0), COALESCE(d.deadline_at,''), COALESCE(d.priority,'normal'),
                COALESCE(d.assigned_at,''), COALESCE(d.ready_at,''), COALESCE(d.collected_at,'')`

func scanDesignJob(scan func(...any) error) (models.DesignJob, error) {
        var j models.DesignJob
        var assigneeID sql.NullInt64
        var createdRaw, updatedRaw string
        err := scan(&j.ID, &j.Title, &j.ProductName, &j.CustomerName, &j.Notes, &j.Status,
                &assigneeID, &j.AssigneeName, &j.CreatedBy, &createdRaw, &updatedRaw,
                &j.OrderID, &j.Deadline, &j.Priority, &j.AssignedAt, &j.ReadyAt, &j.CollectedAt)
        if err != nil {
                return j, err
        }
        if assigneeID.Valid {
                j.AssigneeID = assigneeID.Int64
        }
        j.CreatedAt = normTime(createdRaw)
        j.UpdatedAt = normTime(updatedRaw)
        return j, nil
}

func (s *Service) GetDesignJob(id int64) (*models.DesignJob, error) {
        j, err := scanDesignJob(func(dest ...any) error {
                return s.db.QueryRow(s.db.Rebind(designJobSelect+` WHERE d.id = ?`), id).
                        Scan(dest...)
        })
        if err != nil {
                return nil, ErrNotFound
        }
        return &j, nil
}

// ListDesignJobs returns the board. Scoping is server-side: principals
// without orders.assign (designers) only ever receive jobs assigned to
// them — the API response itself is filtered, not just the UI. An orderID
// filter serves the order-detail view ("jobs for this sale").
func (s *Service) ListDesignJobs(p *auth.Principal, status string, orderID int64) []models.DesignJob {
        q := `SELECT ` + designJobColumns[1:] + ` FROM design_jobs d LEFT JOIN users u ON u.id = d.assignee_id`
        var where []string
        var args []any
        if status != "" {
                where = append(where, `d.status = ?`)
                args = append(args, status)
        }
        if orderID > 0 {
                where = append(where, `d.order_id = ?`)
                args = append(args, orderID)
        }
        if p != nil && !canDelegate(p) {
                where = append(where, `d.assignee_id = ?`)
                args = append(args, p.ID)
        }
        if len(where) > 0 {
                q += ` WHERE ` + strings.Join(where, ` AND `)
        }
        q += ` ORDER BY d.id DESC LIMIT 200`
        rows, err := s.db.Query(s.db.Rebind(q), args...)
        if err != nil {
                return nil
        }
        defer rows.Close()
        var out []models.DesignJob
        for rows.Next() {
                j, err := scanDesignJob(rows.Scan)
                if err != nil {
                        return out
                }
                out = append(out, j)
        }
        return out
}

// ---- Design job attachments (share files with the team) ----

// designFileMaxBytes caps one attachment (5 MB — artwork proofs, vector
// exports, mockups; SQLite BLOBs stay comfortably small at this size).
const designFileMaxBytes = 5 << 20

// AddDesignFile stores one attachment for a design job. Designers may
// attach deliverables to THEIR jobs; delegation holders anywhere.
func (s *Service) AddDesignFile(jobID int64, filename, mime string, data []byte, p *auth.Principal) (*models.DesignFile, error) {
        job, err := s.GetDesignJob(jobID)
        if err != nil {
                return nil, ErrNotFound
        }
        if err := assertJobAccess(p, job); err != nil {
                return nil, err
        }
        if len(data) == 0 {
                return nil, fmt.Errorf("empty file")
        }
        if len(data) > designFileMaxBytes {
                return nil, fmt.Errorf("file too large (5 MB max)")
        }
        filename = truncStr(strings.TrimSpace(filename), 200)
        if filename == "" {
                filename = "attachment"
        }
        res, err := s.db.Exec(s.db.Rebind(`
                INSERT INTO design_files (job_id, filename, mime, size, data, uploaded_by, uploaded_by_name)
                VALUES (?, ?, ?, ?, ?, ?, ?)`),
                jobID, filename, truncStr(mime, 120), len(data), data, p.ID, p.Username)
        if err != nil {
                return nil, err
        }
        id, _ := res.LastInsertId()
        s.Audit(p.ID, p.Username, "DESIGN_FILE_ADDED", "design_file", fmt.Sprint(id), filename+" -> job "+fmt.Sprint(jobID))
        if job.OrderID != 0 {
                s.AddOrderEvent(job.OrderID, models.OrderEventJob, "Attachment added to "+job.Title+": "+filename, p)
        }
        fresh, _ := s.GetDesignJob(jobID)
        if fresh != nil {
                s.broadcast(EventDesignUpdate, fresh)
        }
        return s.GetDesignFile(jobID, id)
}

// GetDesignFile loads one attachment's metadata.
func (s *Service) GetDesignFile(jobID, fileID int64) (*models.DesignFile, error) {
        var f models.DesignFile
        err := s.db.QueryRow(s.db.Rebind(`
                SELECT id, job_id, filename, COALESCE(mime,''), size, COALESCE(uploaded_by,0), COALESCE(uploaded_by_name,''), created_at
                FROM design_files WHERE id = ? AND job_id = ?`), fileID, jobID).
                Scan(&f.ID, &f.JobID, &f.Filename, &f.Mime, &f.Size, &f.UploadedBy, &f.UploadedByName, &f.CreatedAt)
        if err != nil {
                return nil, ErrNotFound
        }
        return &f, nil
}

// ListDesignFiles returns a job's attachments (metadata only).
func (s *Service) ListDesignFiles(jobID int64) ([]models.DesignFile, error) {
        rows, err := s.db.Query(s.db.Rebind(`
                SELECT id, job_id, filename, COALESCE(mime,''), size, COALESCE(uploaded_by,0), COALESCE(uploaded_by_name,''), created_at
                FROM design_files WHERE job_id = ? ORDER BY id DESC`), jobID)
        if err != nil {
                return nil, err
        }
        defer rows.Close()
        out := []models.DesignFile{}
        for rows.Next() {
                var f models.DesignFile
                if err := rows.Scan(&f.ID, &f.JobID, &f.Filename, &f.Mime, &f.Size, &f.UploadedBy, &f.UploadedByName, &f.CreatedAt); err != nil {
                        return nil, err
                }
                out = append(out, f)
        }
        return out, rows.Err()
}

// ReadDesignFile returns the stored bytes for download.
func (s *Service) ReadDesignFile(jobID, fileID int64) (*models.DesignFile, []byte, error) {
        f, err := s.GetDesignFile(jobID, fileID)
        if err != nil {
                return nil, nil, err
        }
        var data []byte
        if err := s.db.QueryRow(`SELECT data FROM design_files WHERE id = ?`, fileID).Scan(&data); err != nil {
                return nil, nil, err
        }
        return f, data, nil
}

// DeleteDesignFile removes one attachment. Uploaders can remove their own
// uploads; only delegation holders may remove anyone else's.
func (s *Service) DeleteDesignFile(jobID, fileID int64, p *auth.Principal) error {
        job, err := s.GetDesignJob(jobID)
        if err != nil {
                return ErrNotFound
        }
        f, err := s.GetDesignFile(jobID, fileID)
        if err != nil {
                return err
        }
        if !canDelegate(p) && f.UploadedBy != p.ID {
                return fmt.Errorf("you can only remove files you uploaded")
        }
        _ = job
        res, err := s.db.Exec(s.db.Rebind(`DELETE FROM design_files WHERE id = ? AND job_id = ?`), fileID, jobID)
        if err != nil {
                return err
        }
        if n, _ := res.RowsAffected(); n != 1 {
                return ErrNotFound
        }
        s.Audit(p.ID, p.Username, "DESIGN_FILE_DELETED", "design_file", fmt.Sprint(fileID), "")
        fresh, _ := s.GetDesignJob(jobID)
        if fresh != nil {
                s.broadcast(EventDesignUpdate, fresh)
        }
        return nil
}

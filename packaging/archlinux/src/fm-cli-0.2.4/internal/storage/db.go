package storage

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"fm-cli/internal/model"

	_ "modernc.org/sqlite"
)

// DB wraps the SQLite database for local email storage
type DB struct {
	db *sql.DB
}

// PendingAction represents an action to sync when online
type PendingAction struct {
	ID        int64
	Type      string // "send_draft", "save_draft", "delete", "move", "set_flags"
	EmailID   string
	Data      string // JSON encoded action data
	CreatedAt time.Time
}

// Open opens or creates the local database
func Open() (*DB, error) {
	// Get user config directory
	configDir, err := os.UserConfigDir()
	if err != nil {
		return nil, fmt.Errorf("failed to get config dir: %w", err)
	}

	dbDir := filepath.Join(configDir, "fm-cli")
	if err := os.MkdirAll(dbDir, 0700); err != nil {
		return nil, fmt.Errorf("failed to create db dir: %w", err)
	}

	dbPath := filepath.Join(dbDir, "emails.db")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, fmt.Errorf("failed to open database: %w", err)
	}

	storage := &DB{db: db}
	if err := storage.migrate(); err != nil {
		db.Close()
		return nil, fmt.Errorf("failed to migrate database: %w", err)
	}

	return storage, nil
}

// Close closes the database
func (d *DB) Close() error {
	return d.db.Close()
}

// migrate creates or updates the database schema
func (d *DB) migrate() error {
	schema := `
	CREATE TABLE IF NOT EXISTS config (
		key TEXT PRIMARY KEY,
		value TEXT
	);

	CREATE TABLE IF NOT EXISTS mailboxes (
		id TEXT PRIMARY KEY,
		name TEXT NOT NULL,
		role TEXT,
		parent_id TEXT,
		sort_order INTEGER,
		unread_count INTEGER DEFAULT 0,
		total_count INTEGER DEFAULT 0,
		updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
	);

	CREATE TABLE IF NOT EXISTS emails (
		id TEXT PRIMARY KEY,
		thread_id TEXT,
		subject TEXT,
		from_addr TEXT,
		to_addr TEXT,
		cc_addr TEXT,
		bcc_addr TEXT,
		reply_to TEXT,
		preview TEXT,
		body_text TEXT,
		body_html TEXT,
		date DATETIME,
		is_unread BOOLEAN DEFAULT 0,
		is_flagged BOOLEAN DEFAULT 0,
		is_draft BOOLEAN DEFAULT 0,
		mailbox_ids TEXT, -- JSON array
		keywords TEXT,    -- JSON array
		updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
	);

	CREATE TABLE IF NOT EXISTS email_mailboxes (
		email_id TEXT,
		mailbox_id TEXT,
		PRIMARY KEY (email_id, mailbox_id),
		FOREIGN KEY (email_id) REFERENCES emails(id) ON DELETE CASCADE,
		FOREIGN KEY (mailbox_id) REFERENCES mailboxes(id) ON DELETE CASCADE
	);

	CREATE TABLE IF NOT EXISTS pending_actions (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		type TEXT NOT NULL,
		email_id TEXT,
		data TEXT,
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP
	);

	CREATE TABLE IF NOT EXISTS local_drafts (
		id TEXT PRIMARY KEY,
		from_addr TEXT,
		to_addr TEXT,
		subject TEXT,
		body TEXT,
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
	);

	CREATE INDEX IF NOT EXISTS idx_emails_thread ON emails(thread_id);
	CREATE INDEX IF NOT EXISTS idx_emails_date ON emails(date);
	CREATE INDEX IF NOT EXISTS idx_email_mailboxes_mailbox ON email_mailboxes(mailbox_id);
	`

	_, err := d.db.Exec(schema)
	return err
}

// GetConfig retrieves a config value
func (d *DB) GetConfig(key string) (string, error) {
	var value string
	err := d.db.QueryRow("SELECT value FROM config WHERE key = ?", key).Scan(&value)
	if err == sql.ErrNoRows {
		return "", nil
	}
	return value, err
}

// SetConfig sets a config value
func (d *DB) SetConfig(key, value string) error {
	_, err := d.db.Exec(
		"INSERT OR REPLACE INTO config (key, value) VALUES (?, ?)",
		key, value,
	)
	return err
}

// SaveMailboxes saves mailboxes to local storage
func (d *DB) SaveMailboxes(mailboxes []model.Mailbox) error {
	tx, err := d.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	stmt, err := tx.Prepare(`
		INSERT OR REPLACE INTO mailboxes (id, name, role, parent_id, sort_order, unread_count, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, CURRENT_TIMESTAMP)
	`)
	if err != nil {
		return err
	}
	defer stmt.Close()

	for _, mb := range mailboxes {
		_, err := stmt.Exec(mb.ID, mb.Name, mb.Role, mb.ParentID, mb.SortOrder, mb.UnreadCount)
		if err != nil {
			return err
		}
	}

	return tx.Commit()
}

// GetMailboxes retrieves all mailboxes from local storage
func (d *DB) GetMailboxes() ([]model.Mailbox, error) {
	rows, err := d.db.Query(`
		SELECT id, name, role, parent_id, sort_order, unread_count
		FROM mailboxes
		ORDER BY sort_order, name
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var mailboxes []model.Mailbox
	for rows.Next() {
		var mb model.Mailbox
		var parentID sql.NullString
		err := rows.Scan(&mb.ID, &mb.Name, &mb.Role, &parentID, &mb.SortOrder, &mb.UnreadCount)
		if err != nil {
			return nil, err
		}
		if parentID.Valid {
			mb.ParentID = parentID.String
		}
		mailboxes = append(mailboxes, mb)
	}

	return mailboxes, rows.Err()
}

// SaveEmails saves emails to local storage
func (d *DB) SaveEmails(emails []model.Email) error {
	tx, err := d.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	stmt, err := tx.Prepare(`
		INSERT OR REPLACE INTO emails 
		(id, thread_id, subject, from_addr, to_addr, cc_addr, bcc_addr, reply_to, 
		 preview, body_text, date, is_unread, is_flagged, is_draft, mailbox_ids, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, CURRENT_TIMESTAMP)
	`)
	if err != nil {
		return err
	}
	defer stmt.Close()

	mbStmt, err := tx.Prepare(`
		INSERT OR REPLACE INTO email_mailboxes (email_id, mailbox_id)
		VALUES (?, ?)
	`)
	if err != nil {
		return err
	}
	defer mbStmt.Close()

	for _, e := range emails {
		mailboxIDs, _ := json.Marshal(e.MailboxIDs)
		_, err := stmt.Exec(
			e.ID, e.ThreadID, e.Subject, e.From, e.To, e.Cc, e.Bcc, e.ReplyTo,
			e.Preview, e.Body, e.Date, e.IsUnread, e.IsFlagged, e.IsDraft, string(mailboxIDs),
		)
		if err != nil {
			return err
		}

		// Update email_mailboxes junction table
		for _, mbID := range e.MailboxIDs {
			_, err := mbStmt.Exec(e.ID, mbID)
			if err != nil {
				return err
			}
		}
	}

	return tx.Commit()
}

// GetEmails retrieves emails for a mailbox from local storage
func (d *DB) GetEmails(mailboxID string, offset, limit int) ([]model.Email, error) {
	rows, err := d.db.Query(`
		SELECT e.id, e.thread_id, e.subject, e.from_addr, e.to_addr, e.cc_addr, 
		       e.bcc_addr, e.reply_to, e.preview, e.date, e.is_unread, e.is_flagged, 
		       e.is_draft, e.mailbox_ids
		FROM emails e
		JOIN email_mailboxes em ON e.id = em.email_id
		WHERE em.mailbox_id = ?
		ORDER BY e.date DESC
		LIMIT ? OFFSET ?
	`, mailboxID, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var emails []model.Email
	for rows.Next() {
		var e model.Email
		var mailboxIDsJSON string
		err := rows.Scan(
			&e.ID, &e.ThreadID, &e.Subject, &e.From, &e.To, &e.Cc,
			&e.Bcc, &e.ReplyTo, &e.Preview, &e.Date, &e.IsUnread, &e.IsFlagged,
			&e.IsDraft, &mailboxIDsJSON,
		)
		if err != nil {
			return nil, err
		}
		json.Unmarshal([]byte(mailboxIDsJSON), &e.MailboxIDs)
		emails = append(emails, e)
	}

	return emails, rows.Err()
}

// GetEmailBody retrieves the body of an email, falling back to preview if body not available
func (d *DB) GetEmailBody(emailID string) (string, error) {
	var body sql.NullString
	var preview sql.NullString
	err := d.db.QueryRow("SELECT body_text, preview FROM emails WHERE id = ?", emailID).Scan(&body, &preview)
	if err != nil {
		return "", err
	}
	if body.Valid && body.String != "" {
		return body.String, nil
	}
	// Fall back to preview if body not available
	if preview.Valid && preview.String != "" {
		return "[Full email body not cached - showing preview]\n\n" + preview.String, nil
	}
	return "[Email body not available offline]", nil
}

// SaveEmailBody saves the body of an email
func (d *DB) SaveEmailBody(emailID, body string) error {
	_, err := d.db.Exec("UPDATE emails SET body_text = ? WHERE id = ?", body, emailID)
	return err
}

// SaveEmailHTMLBody saves the HTML body of an email
func (d *DB) SaveEmailHTMLBody(emailID, htmlBody string) error {
	_, err := d.db.Exec("UPDATE emails SET body_html = ? WHERE id = ?", htmlBody, emailID)
	return err
}

// GetEmailHTMLBody retrieves the HTML body of an email
func (d *DB) GetEmailHTMLBody(emailID string) (string, error) {
	var htmlBody sql.NullString
	err := d.db.QueryRow("SELECT body_html FROM emails WHERE id = ?", emailID).Scan(&htmlBody)
	if err != nil {
		return "", err
	}
	if !htmlBody.Valid || htmlBody.String == "" {
		return "", nil
	}
	return htmlBody.String, nil
}

// AddPendingAction adds an action to sync later
func (d *DB) AddPendingAction(actionType, emailID, data string) error {
	_, err := d.db.Exec(
		"INSERT INTO pending_actions (type, email_id, data) VALUES (?, ?, ?)",
		actionType, emailID, data,
	)
	return err
}

// GetPendingActions retrieves all pending actions
func (d *DB) GetPendingActions() ([]PendingAction, error) {
	rows, err := d.db.Query(`
		SELECT id, type, email_id, data, created_at
		FROM pending_actions
		ORDER BY created_at ASC
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var actions []PendingAction
	for rows.Next() {
		var a PendingAction
		var emailID sql.NullString
		err := rows.Scan(&a.ID, &a.Type, &emailID, &a.Data, &a.CreatedAt)
		if err != nil {
			return nil, err
		}
		if emailID.Valid {
			a.EmailID = emailID.String
		}
		actions = append(actions, a)
	}

	return actions, rows.Err()
}

// RemovePendingAction removes a synced action
func (d *DB) RemovePendingAction(id int64) error {
	_, err := d.db.Exec("DELETE FROM pending_actions WHERE id = ?", id)
	return err
}

// SaveLocalDraft saves a draft locally (for offline use)
func (d *DB) SaveLocalDraft(id, from, to, subject, body string) error {
	_, err := d.db.Exec(`
		INSERT OR REPLACE INTO local_drafts (id, from_addr, to_addr, subject, body, updated_at)
		VALUES (?, ?, ?, ?, ?, CURRENT_TIMESTAMP)
	`, id, from, to, subject, body)
	return err
}

// GetLocalDrafts retrieves all local drafts
func (d *DB) GetLocalDrafts() ([]model.Email, error) {
	rows, err := d.db.Query(`
		SELECT id, from_addr, to_addr, subject, body, created_at
		FROM local_drafts
		ORDER BY updated_at DESC
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var drafts []model.Email
	for rows.Next() {
		var e model.Email
		var createdAt string
		err := rows.Scan(&e.ID, &e.From, &e.To, &e.Subject, &e.Body, &createdAt)
		if err != nil {
			return nil, err
		}
		e.IsDraft = true
		e.Date = createdAt
		drafts = append(drafts, e)
	}

	return drafts, rows.Err()
}

// DeleteLocalDraft removes a local draft
func (d *DB) DeleteLocalDraft(id string) error {
	_, err := d.db.Exec("DELETE FROM local_drafts WHERE id = ?", id)
	return err
}

// DeleteEmail removes an email from local storage
func (d *DB) DeleteEmail(emailID string) error {
	tx, err := d.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	_, err = tx.Exec("DELETE FROM email_mailboxes WHERE email_id = ?", emailID)
	if err != nil {
		return err
	}
	_, err = tx.Exec("DELETE FROM emails WHERE id = ?", emailID)
	if err != nil {
		return err
	}

	return tx.Commit()
}

// UpdateEmailFlags updates the flags of an email locally
func (d *DB) UpdateEmailFlags(emailID string, isUnread, isFlagged bool) error {
	_, err := d.db.Exec(
		"UPDATE emails SET is_unread = ?, is_flagged = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?",
		isUnread, isFlagged, emailID,
	)
	return err
}

// MoveEmail updates the mailbox of an email locally
func (d *DB) MoveEmail(emailID, fromMailboxID, toMailboxID string) error {
	tx, err := d.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	_, err = tx.Exec("DELETE FROM email_mailboxes WHERE email_id = ? AND mailbox_id = ?", emailID, fromMailboxID)
	if err != nil {
		return err
	}
	_, err = tx.Exec("INSERT OR REPLACE INTO email_mailboxes (email_id, mailbox_id) VALUES (?, ?)", emailID, toMailboxID)
	if err != nil {
		return err
	}

	return tx.Commit()
}

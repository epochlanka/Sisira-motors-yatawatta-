package repository

import (
	"context"

	"garagems/internal/models"
)

const userSelect = `SELECT u.id, u.username, u.full_name, u.password_hash, u.role_id, r.name, u.active, u.must_change_password, u.created_at, u.updated_at
	FROM users u JOIN roles r ON r.id = u.role_id`

func scanUser(sc interface{ Scan(...any) error }) (models.User, error) {
	var u models.User
	err := sc.Scan(&u.ID, &u.Username, &u.FullName, &u.PasswordHash, &u.RoleID, &u.RoleName, &u.Active, &u.MustChangePassword, &u.CreatedAt, &u.UpdatedAt)
	return u, err
}

func GetUser(ctx context.Context, q DBTX, id int64) (models.User, error) {
	u, err := scanUser(q.QueryRowContext(ctx, userSelect+" WHERE u.id = ?", id))
	return u, notFound(err)
}

func GetUserByUsername(ctx context.Context, q DBTX, username string) (models.User, error) {
	u, err := scanUser(q.QueryRowContext(ctx, userSelect+" WHERE u.username = ?", username))
	return u, notFound(err)
}

func ListUsers(ctx context.Context, q DBTX, activeOnly bool) ([]models.User, error) {
	query := userSelect
	if activeOnly {
		query += " WHERE u.active = 1"
	}
	rows, err := q.QueryContext(ctx, query+" ORDER BY u.active DESC, u.full_name COLLATE NOCASE")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []models.User
	for rows.Next() {
		u, err := scanUser(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

func CountUsers(ctx context.Context, q DBTX) (int, error) {
	return count(ctx, q, "SELECT COUNT(*) FROM users")
}

func CreateUser(ctx context.Context, q DBTX, u models.User) (int64, error) {
	now := models.NowUTC()
	res, err := q.ExecContext(ctx, `INSERT INTO users(username, full_name, password_hash, role_id, active, must_change_password, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, u.Username, u.FullName, u.PasswordHash, u.RoleID, u.Active, u.MustChangePassword, now, now)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func UpdateUser(ctx context.Context, q DBTX, u models.User) error {
	_, err := q.ExecContext(ctx, `UPDATE users SET full_name = ?, role_id = ?, active = ?, updated_at = ? WHERE id = ?`,
		u.FullName, u.RoleID, u.Active, models.NowUTC(), u.ID)
	return err
}

func SetPassword(ctx context.Context, q DBTX, id int64, hash string, mustChange bool) error {
	_, err := q.ExecContext(ctx, "UPDATE users SET password_hash = ?, must_change_password = ?, updated_at = ? WHERE id = ?", hash, mustChange, models.NowUTC(), id)
	return err
}

// CountActiveAdmins counts active users whose role holds users.manage.
func CountActiveAdmins(ctx context.Context, q DBTX, excludeUserID int64) (int, error) {
	return count(ctx, q, `SELECT COUNT(*) FROM users u JOIN role_permissions rp ON rp.role_id = u.role_id AND rp.permission = 'users.manage'
		WHERE u.active = 1 AND u.id <> ?`, excludeUserID)
}

// ---------- roles ----------

func ListRoles(ctx context.Context, q DBTX) ([]models.Role, error) {
	rows, err := q.QueryContext(ctx, `SELECT r.id, r.name, r.description, r.is_system,
		(SELECT COUNT(*) FROM users u WHERE u.role_id = r.id AND u.active = 1) FROM roles r ORDER BY r.id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []models.Role
	for rows.Next() {
		var r models.Role
		if err := rows.Scan(&r.ID, &r.Name, &r.Description, &r.IsSystem, &r.UserCount); err != nil {
			return nil, err
		}
		r.Permissions = map[string]bool{}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	rows.Close()
	for i := range out {
		p, err := RolePermissions(ctx, q, out[i].ID)
		if err != nil {
			return nil, err
		}
		out[i].Permissions = p
	}
	return out, nil
}

func GetRole(ctx context.Context, q DBTX, id int64) (models.Role, error) {
	var r models.Role
	err := q.QueryRowContext(ctx, "SELECT id, name, description, is_system FROM roles WHERE id = ?", id).Scan(&r.ID, &r.Name, &r.Description, &r.IsSystem)
	if err != nil {
		return r, notFound(err)
	}
	r.Permissions, err = RolePermissions(ctx, q, id)
	return r, err
}

func RolePermissions(ctx context.Context, q DBTX, roleID int64) (map[string]bool, error) {
	rows, err := q.QueryContext(ctx, "SELECT permission FROM role_permissions WHERE role_id = ?", roleID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	m := map[string]bool{}
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			return nil, err
		}
		m[p] = true
	}
	return m, rows.Err()
}

func SetRolePermissions(ctx context.Context, q DBTX, roleID int64, perms []string) error {
	if _, err := q.ExecContext(ctx, "DELETE FROM role_permissions WHERE role_id = ?", roleID); err != nil {
		return err
	}
	for _, p := range perms {
		if _, err := q.ExecContext(ctx, "INSERT INTO role_permissions(role_id, permission) VALUES (?, ?)", roleID, p); err != nil {
			return err
		}
	}
	return nil
}

func CreateRole(ctx context.Context, q DBTX, name, desc string) (int64, error) {
	res, err := q.ExecContext(ctx, "INSERT INTO roles(name, description, is_system) VALUES (?, ?, 0)", name, desc)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// ---------- sessions ----------

type SessionRow struct {
	TokenHash string
	UserID    int64
	CSRF      string
	Flash     string
	ExpiresAt string
}

func CreateSession(ctx context.Context, q DBTX, tokenHash string, userID int64, csrf, ip, expires string) error {
	_, err := q.ExecContext(ctx, `INSERT INTO sessions(token_hash, user_id, csrf_token, ip, created_at, expires_at) VALUES (?, ?, ?, ?, ?, ?)`,
		tokenHash, userID, csrf, ip, models.NowUTC(), expires)
	return err
}

func GetSession(ctx context.Context, q DBTX, tokenHash string) (SessionRow, error) {
	var s SessionRow
	err := q.QueryRowContext(ctx, `SELECT token_hash, user_id, csrf_token, flash, expires_at FROM sessions WHERE token_hash = ? AND expires_at > ?`,
		tokenHash, models.NowUTC()).Scan(&s.TokenHash, &s.UserID, &s.CSRF, &s.Flash, &s.ExpiresAt)
	return s, notFound(err)
}

func ExtendSession(ctx context.Context, q DBTX, tokenHash, expires string) error {
	_, err := q.ExecContext(ctx, "UPDATE sessions SET expires_at = ? WHERE token_hash = ?", expires, tokenHash)
	return err
}

func SetFlash(ctx context.Context, q DBTX, tokenHash, flash string) error {
	_, err := q.ExecContext(ctx, "UPDATE sessions SET flash = ? WHERE token_hash = ?", flash, tokenHash)
	return err
}

func DeleteSession(ctx context.Context, q DBTX, tokenHash string) error {
	_, err := q.ExecContext(ctx, "DELETE FROM sessions WHERE token_hash = ?", tokenHash)
	return err
}

func DeleteUserSessions(ctx context.Context, q DBTX, userID int64, exceptHash string) error {
	_, err := q.ExecContext(ctx, "DELETE FROM sessions WHERE user_id = ? AND token_hash <> ?", userID, exceptHash)
	return err
}

func PurgeExpiredSessions(ctx context.Context, q DBTX) error {
	_, err := q.ExecContext(ctx, "DELETE FROM sessions WHERE expires_at <= ?", models.NowUTC())
	return err
}

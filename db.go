package main

import (
	"database/sql"
	"log"
	"os"
	"time"

	_ "github.com/lib/pq"
)

var db *sql.DB

type User struct {
	ID             int
	Username       string
	PasswordHash   string
	CreatedAt      time.Time
	FailedAttempts int
	LockedUntil    sql.NullTime
	TokenBalance   int `json:"token_balance"`
}

type PurchaseRecord struct {
	ID          int       `json:"id"`
	UserID      int       `json:"user_id"`
	Amount      int       `json:"amount"`      // 购买的 token 数量
	Price       float64   `json:"price"`       // 价格（演示用，实际没收钱）
	CreatedAt   time.Time `json:"created_at"`
}

type ChatUsage struct {
	ID        int       `json:"id"`
	UserID    int       `json:"user_id"`
	Model     string    `json:"model"`
	PromptTokens    int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens     int `json:"total_tokens"`
	CreatedAt time.Time `json:"created_at"`
}

type TokenStats struct {
	TotalConversations int `json:"total_conversations"`
	TotalPromptTokens  int `json:"total_prompt_tokens"`
	TotalCompletionTokens int `json:"total_completion_tokens"`
	TotalTokens        int `json:"total_tokens"`
	TodayConversations int `json:"today_conversations"`
	TodayTokens        int `json:"today_tokens"`
}

func initDB() {
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		log.Fatal("DATABASE_URL is not set")
	}

	var err error
	db, err = sql.Open("postgres", dsn)
	if err != nil {
		log.Fatalf("Failed to open database: %v", err)
	}

	db.SetMaxOpenConns(10)
	db.SetMaxIdleConns(5)
	db.SetConnMaxLifetime(5 * time.Minute)

	if err = db.Ping(); err != nil {
		log.Fatalf("Failed to ping database: %v", err)
	}

	if err = createTables(); err != nil {
		log.Fatalf("Failed to create tables: %v", err)
	}

	log.Println("Database connected")
}

func createTables() error {
	query := `
	-- 用户 token 余额字段
	ALTER TABLE users ADD COLUMN IF NOT EXISTS token_balance INTEGER DEFAULT 10000;

	-- 购买记录表
	CREATE TABLE IF NOT EXISTS purchase_records (
		id SERIAL PRIMARY KEY,
		user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
		amount INTEGER NOT NULL,
		price DECIMAL(10,2) DEFAULT 0,
		created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
	);
	CREATE INDEX IF NOT EXISTS idx_purchase_user_id ON purchase_records(user_id);

	-- 对话使用记录表
	CREATE TABLE IF NOT EXISTS chat_usage (
		id SERIAL PRIMARY KEY,
		user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
		model VARCHAR(100),
		prompt_tokens INTEGER DEFAULT 0,
		completion_tokens INTEGER DEFAULT 0,
		total_tokens INTEGER DEFAULT 0,
		created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
	);
	CREATE INDEX IF NOT EXISTS idx_chat_usage_user_id ON chat_usage(user_id);
	CREATE INDEX IF NOT EXISTS idx_chat_usage_created_at ON chat_usage(created_at);
	`
	_, err := db.Exec(query)
	return err
}

func saveChatUsage(userID int, model string, promptTokens, completionTokens, totalTokens int) error {
	_, err := db.Exec(
		`INSERT INTO chat_usage (user_id, model, prompt_tokens, completion_tokens, total_tokens)
		 VALUES ($1, $2, $3, $4, $5)`,
		userID, model, promptTokens, completionTokens, totalTokens,
	)
	return err
}

func getUserTokenStats(userID int) (*TokenStats, error) {
	stats := &TokenStats{}

	// 总体统计
	err := db.QueryRow(
		`SELECT COUNT(*), COALESCE(SUM(prompt_tokens), 0), COALESCE(SUM(completion_tokens), 0), COALESCE(SUM(total_tokens), 0)
		 FROM chat_usage WHERE user_id = $1`,
		userID,
	).Scan(&stats.TotalConversations, &stats.TotalPromptTokens, &stats.TotalCompletionTokens, &stats.TotalTokens)
	if err != nil {
		return nil, err
	}

	// 今日统计
	err = db.QueryRow(
		`SELECT COUNT(*), COALESCE(SUM(total_tokens), 0)
		 FROM chat_usage WHERE user_id = $1 AND DATE(created_at) = CURRENT_DATE`,
		userID,
	).Scan(&stats.TodayConversations, &stats.TodayTokens)
	if err != nil {
		return nil, err
	}

	return stats, nil
}

func getRecentUsage(userID int, limit int) ([]ChatUsage, error) {
	rows, err := db.Query(
		`SELECT id, user_id, model, prompt_tokens, completion_tokens, total_tokens, created_at
		 FROM chat_usage WHERE user_id = $1 ORDER BY created_at DESC LIMIT $2`,
		userID, limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var usages []ChatUsage
	for rows.Next() {
		var u ChatUsage
		if err := rows.Scan(&u.ID, &u.UserID, &u.Model, &u.PromptTokens, &u.CompletionTokens, &u.TotalTokens, &u.CreatedAt); err != nil {
			return nil, err
		}
		usages = append(usages, u)
	}
	return usages, rows.Err()
}

// Token 余额相关函数
func getUserTokenBalance(userID int) (int, error) {
	var balance int
	err := db.QueryRow(`SELECT COALESCE(token_balance, 0) FROM users WHERE id = $1`, userID).Scan(&balance)
	return balance, err
}

func deductTokens(userID int, amount int) error {
	_, err := db.Exec(
		`UPDATE users SET token_balance = token_balance - $1 WHERE id = $2 AND token_balance >= $1`,
		amount, userID,
	)
	return err
}

func addTokens(userID int, amount int) error {
	_, err := db.Exec(
		`UPDATE users SET token_balance = token_balance + $1 WHERE id = $2`,
		amount, userID,
	)
	return err
}

func createPurchaseRecord(userID int, amount int, price float64) error {
	_, err := db.Exec(
		`INSERT INTO purchase_records (user_id, amount, price) VALUES ($1, $2, $3)`,
		userID, amount, price,
	)
	return err
}

func getPurchaseHistory(userID int, limit int) ([]PurchaseRecord, error) {
	rows, err := db.Query(
		`SELECT id, user_id, amount, price, created_at FROM purchase_records
		 WHERE user_id = $1 ORDER BY created_at DESC LIMIT $2`,
		userID, limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var records []PurchaseRecord
	for rows.Next() {
		var r PurchaseRecord
		if err := rows.Scan(&r.ID, &r.UserID, &r.Amount, &r.Price, &r.CreatedAt); err != nil {
			return nil, err
		}
		records = append(records, r)
	}
	return records, rows.Err()
}

func getUserByUsername(username string) (*User, error) {
	u := &User{}
	err := db.QueryRow(
		`SELECT id, username, password_hash, created_at, failed_attempts, locked_until, COALESCE(token_balance, 0)
		 FROM users WHERE username = $1`, username,
	).Scan(&u.ID, &u.Username, &u.PasswordHash, &u.CreatedAt, &u.FailedAttempts, &u.LockedUntil, &u.TokenBalance)
	if err != nil {
		return nil, err
	}
	return u, nil
}

func createUser(username, passwordHash string) error {
	_, err := db.Exec(
		`INSERT INTO users (username, password_hash) VALUES ($1, $2)`,
		username, passwordHash,
	)
	return err
}

func incrementFailedAttempts(userID int) error {
	_, err := db.Exec(
		`UPDATE users SET failed_attempts = failed_attempts + 1,
		 locked_until = CASE WHEN failed_attempts + 1 >= 5
		   THEN NOW() + INTERVAL '15 minutes' ELSE locked_until END
		 WHERE id = $1`, userID,
	)
	return err
}

func resetFailedAttempts(userID int) error {
	_, err := db.Exec(
		`UPDATE users SET failed_attempts = 0, locked_until = NULL WHERE id = $1`, userID,
	)
	return err
}

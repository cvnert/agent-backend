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

	log.Println("Database connected")
}

func getUserByUsername(username string) (*User, error) {
	u := &User{}
	err := db.QueryRow(
		`SELECT id, username, password_hash, created_at, failed_attempts, locked_until
		 FROM users WHERE username = $1`, username,
	).Scan(&u.ID, &u.Username, &u.PasswordHash, &u.CreatedAt, &u.FailedAttempts, &u.LockedUntil)
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

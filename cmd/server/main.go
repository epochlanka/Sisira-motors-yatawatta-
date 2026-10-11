// Command server runs the garage management web application.
//
//	server [serve]        start the web server (default)
//	server init           create/migrate the database and exit
//	server create-admin   create an administrator account
//	server backup -out F  write a consistent copy of the database to F
//	server seed-demo      load optional demo data into an empty database
package main

import (
	"bufio"
	"context"
	"database/sql"
	"errors"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"
	_ "time/tzdata" // embed timezone database so TZ names work on every OS

	"golang.org/x/term"

	"garagems/internal/config"
	"garagems/internal/database"
	"garagems/internal/handlers"
	"garagems/internal/models"
	"garagems/internal/repository"
)

func main() {
	cmd := "serve"
	args := os.Args[1:]
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		cmd, args = args[0], args[1:]
	}
	fs := flag.NewFlagSet(cmd, flag.ExitOnError)
	cfgPath := fs.String("config", "config.json", "path to the JSON configuration file")
	username := fs.String("username", "admin", "create-admin: username")
	fullName := fs.String("name", "Administrator", "create-admin: full name")
	out := fs.String("out", "", "backup: output file path")
	fs.Parse(args)

	cfg, err := config.Load(*cfgPath, *cfgPath != "config.json")
	if err != nil {
		log.Fatalf("configuration: %v", err)
	}
	models.SetLocation(cfg.Location)

	db, err := openDB(cfg)
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()

	switch cmd {
	case "serve":
		serve(cfg, db)
	case "init":
		fmt.Printf("Database ready at %s\n", cfg.DBPath)
	case "create-admin":
		if err := createAdmin(db, *username, *fullName); err != nil {
			log.Fatal(err)
		}
	case "backup":
		if *out == "" {
			log.Fatal("backup: -out is required")
		}
		if _, err := os.Stat(*out); err == nil {
			log.Fatalf("backup: %s already exists; choose a new file name", *out)
		}
		if _, err := db.Exec("VACUUM INTO ?", *out); err != nil {
			log.Fatalf("backup: %v", err)
		}
		fmt.Printf("Backup written to %s\n", *out)
	case "seed-demo":
		if err := seedDemo(db); err != nil {
			log.Fatal(err)
		}
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q (use serve, init, create-admin, backup, seed-demo)\n", cmd)
		os.Exit(2)
	}
}

// openDB opens the database and applies pending migrations and default reference data.
// It never drops or recreates existing data.
func openDB(cfg config.Config) (*sql.DB, error) {
	db, err := database.Open(cfg.DBPath)
	if err != nil {
		return nil, err
	}
	ctx := context.Background()
	if err := database.Migrate(ctx, db); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate: %w", err)
	}
	if err := database.SeedDefaults(ctx, db, cfg); err != nil {
		db.Close()
		return nil, fmt.Errorf("seed defaults: %w", err)
	}
	return db, nil
}

func serve(cfg config.Config, db *sql.DB) {
	app, err := handlers.New(db, cfg)
	if err != nil {
		log.Fatal(err)
	}
	if n, _ := repository.CountUsers(context.Background(), db); n == 0 {
		log.Printf("No users exist yet. Create the first administrator with:  %s create-admin -username admin", os.Args[0])
	}
	if !cfg.IsLoopback() {
		log.Printf("WARNING: listening on %s (reachable from the network). Use a trusted LAN, a firewall and preferably TLS.", cfg.ListenAddr)
		if !cfg.CookieSecure {
			log.Printf("WARNING: cookie_secure is false; session cookies are sent over plain HTTP on the network.")
		}
	}
	srv := &http.Server{
		Addr:              cfg.ListenAddr,
		Handler:           app.Routes(),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       120 * time.Second,
	}
	go func() {
		scheme := "http"
		if cfg.TLSCertFile != "" {
			scheme = "https"
		}
		log.Printf("Garage manager listening on %s://%s (database %s)", scheme, cfg.ListenAddr, cfg.DBPath)
		var err error
		if cfg.TLSCertFile != "" {
			err = srv.ListenAndServeTLS(cfg.TLSCertFile, cfg.TLSKeyFile)
		} else {
			err = srv.ListenAndServe()
		}
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatal(err)
		}
	}()
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	<-stop
	log.Print("Shutting down...")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	srv.Shutdown(ctx)
}

func readPassword(prompt string) (string, error) {
	fmt.Print(prompt)
	if term.IsTerminal(int(os.Stdin.Fd())) {
		b, err := term.ReadPassword(int(os.Stdin.Fd()))
		fmt.Println()
		return string(b), err
	}
	line, err := bufio.NewReader(os.Stdin).ReadString('\n')
	return strings.TrimRight(line, "\r\n"), err
}

func createAdmin(db *sql.DB, username, fullName string) error {
	ctx := context.Background()
	if _, err := repository.GetUserByUsername(ctx, db, username); err == nil {
		return fmt.Errorf("user %q already exists", username)
	}
	var roleID int64
	if err := db.QueryRow("SELECT id FROM roles WHERE is_system = 1 ORDER BY id LIMIT 1").Scan(&roleID); err != nil {
		return fmt.Errorf("administrator role not found: %w", err)
	}
	pw := os.Getenv("GARAGE_ADMIN_PASSWORD")
	if pw == "" {
		var err error
		if pw, err = readPassword("New password (min 8 characters): "); err != nil {
			return err
		}
		confirm, err := readPassword("Repeat password: ")
		if err != nil {
			return err
		}
		if pw != confirm {
			return errors.New("passwords do not match")
		}
	}
	if msg := handlers.ValidatePassword(pw, username); msg != "" {
		return errors.New(msg)
	}
	hash, err := handlers.HashPassword(pw)
	if err != nil {
		return err
	}
	id, err := repository.CreateUser(ctx, db, models.User{Username: username, FullName: fullName, PasswordHash: hash, RoleID: roleID, Active: true})
	if err != nil {
		return err
	}
	repository.Audit(ctx, db, models.Actor{Username: "cli"}, "user.create", "user", id, username+" (administrator, created from command line)")
	fmt.Printf("Administrator %q created.\n", username)
	return nil
}

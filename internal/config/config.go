// Package config loads server configuration from a JSON file with environment overrides.
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// InitialBusiness values are copied into the settings table the first time the
// database is created. After that, business details are edited in Settings.
type InitialBusiness struct {
	Name             string `json:"name"`
	Address          string `json:"address"`
	Phone            string `json:"phone"`
	Email            string `json:"email"`
	CurrencyCode     string `json:"currency_code"`
	CurrencySymbol   string `json:"currency_symbol"`
	CurrencyDecimals int    `json:"currency_decimals"`
	InvoicePrefix    string `json:"invoice_prefix"`
	InvoiceFooter    string `json:"invoice_footer"`
}

type Config struct {
	ListenAddr   string          `json:"listen_addr"`
	DBPath       string          `json:"db_path"`
	Timezone     string          `json:"timezone"`
	SessionHours int             `json:"session_hours"`
	CookieSecure bool            `json:"cookie_secure"`
	TLSCertFile  string          `json:"tls_cert_file"`
	TLSKeyFile   string          `json:"tls_key_file"`
	Business     InitialBusiness `json:"initial_business"`

	Location *time.Location `json:"-"`
}

func Default() Config {
	return Config{
		ListenAddr:   "127.0.0.1:8080",
		DBPath:       "data/garage.db",
		Timezone:     "Local",
		SessionHours: 12,
		Business: InitialBusiness{
			Name:             "My Garage",
			CurrencyCode:     "USD",
			CurrencySymbol:   "$",
			CurrencyDecimals: 2,
			InvoicePrefix:    "INV-",
			InvoiceFooter:    "Thank you for your business.",
		},
	}
}

// Load reads path (if it exists) over the defaults, then applies environment overrides:
// GARAGE_LISTEN_ADDR, GARAGE_DB_PATH, GARAGE_TIMEZONE, GARAGE_COOKIE_SECURE,
// GARAGE_TLS_CERT_FILE, GARAGE_TLS_KEY_FILE.
func Load(path string, mustExist bool) (Config, error) {
	cfg := Default()
	if path != "" {
		b, err := os.ReadFile(path)
		switch {
		case err == nil:
			dec := json.NewDecoder(strings.NewReader(string(b)))
			dec.DisallowUnknownFields()
			if err := dec.Decode(&cfg); err != nil {
				return cfg, fmt.Errorf("config %s: %w", path, err)
			}
			// A relative db_path is relative to the config file, not the current
			// directory, so every command uses the same database wherever it is run.
			if !filepath.IsAbs(cfg.DBPath) {
				if abs, err := filepath.Abs(filepath.Join(filepath.Dir(path), cfg.DBPath)); err == nil {
					cfg.DBPath = abs
				}
			}
		case errors.Is(err, os.ErrNotExist) && !mustExist:
		default:
			return cfg, fmt.Errorf("config %s: %w", path, err)
		}
	}
	if v := os.Getenv("GARAGE_LISTEN_ADDR"); v != "" {
		cfg.ListenAddr = v
	}
	if v := os.Getenv("GARAGE_DB_PATH"); v != "" {
		cfg.DBPath = v
	}
	if v := os.Getenv("GARAGE_TIMEZONE"); v != "" {
		cfg.Timezone = v
	}
	if v := os.Getenv("GARAGE_COOKIE_SECURE"); v != "" {
		b, err := strconv.ParseBool(v)
		if err != nil {
			return cfg, fmt.Errorf("GARAGE_COOKIE_SECURE: %w", err)
		}
		cfg.CookieSecure = b
	}
	if v := os.Getenv("GARAGE_TLS_CERT_FILE"); v != "" {
		cfg.TLSCertFile = v
	}
	if v := os.Getenv("GARAGE_TLS_KEY_FILE"); v != "" {
		cfg.TLSKeyFile = v
	}
	return cfg, cfg.validate()
}

func (c *Config) validate() error {
	if _, _, err := net.SplitHostPort(c.ListenAddr); err != nil {
		return fmt.Errorf("listen_addr %q: %w", c.ListenAddr, err)
	}
	if c.DBPath == "" {
		return errors.New("db_path is required")
	}
	if c.SessionHours <= 0 || c.SessionHours > 24*30 {
		return errors.New("session_hours must be between 1 and 720")
	}
	if (c.TLSCertFile == "") != (c.TLSKeyFile == "") {
		return errors.New("tls_cert_file and tls_key_file must be set together")
	}
	if c.TLSCertFile != "" {
		c.CookieSecure = true
	}
	loc, err := time.LoadLocation(c.Timezone)
	if err != nil {
		return fmt.Errorf("timezone %q: %w", c.Timezone, err)
	}
	c.Location = loc
	if c.Business.CurrencyDecimals < 0 || c.Business.CurrencyDecimals > 3 {
		return errors.New("initial_business.currency_decimals must be 0-3")
	}
	return nil
}

// IsLoopback reports whether the server only listens on the local machine.
func (c *Config) IsLoopback() bool {
	host, _, _ := net.SplitHostPort(c.ListenAddr)
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

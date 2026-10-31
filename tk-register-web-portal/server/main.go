package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/go-redis/redis/v8"
	mysqlDriver "github.com/go-sql-driver/mysql"
	"github.com/jackc/pgx/v5"
	"goapi-tk-register/server/internal/core"
)

func env(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
func validateDatabase(driver, dsn string) error {
	if dsn == "" {
		return errors.New("TK_REGISTER_SQL_DSN is required")
	}
	switch driver {
	case "mysql":
		cfg, e := mysqlDriver.ParseDSN(dsn)
		if e != nil {
			return errors.New("invalid TK database configuration")
		}
		if cfg.DBName != "tk_register" {
			return errors.New("TK database must be named tk_register")
		}
	case "postgres", "postgresql":
		cfg, e := pgx.ParseConfig(dsn)
		if e != nil {
			return errors.New("invalid TK database configuration")
		}
		if cfg.Database != "tk_register" {
			return errors.New("TK database must be named tk_register")
		}
	case "sqlite":
		if dsn == os.Getenv("SQL_DSN") || dsn == os.Getenv("SQLITE_PATH") {
			return errors.New("TK SQLite database must be separate from GoAPI")
		}
	default:
		return errors.New("unsupported TK database driver")
	}
	return nil
}
func validateOrigin(value string) (string, bool, error) {
	u, e := url.Parse(value)
	if e != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return "", false, errors.New("TK_REGISTER_PUBLIC_ORIGIN must be a website origin")
	}
	if u.Scheme != "https" {
		if u.Scheme != "http" || (u.Hostname() != "localhost" && u.Hostname() != "127.0.0.1" && u.Hostname() != "::1") {
			return "", false, errors.New("TK public origin must use HTTPS")
		}
	}
	return u.Scheme + "://" + u.Host, u.Scheme == "https", nil
}
func run() error {
	driver := strings.ToLower(env("TK_REGISTER_SQL_DRIVER", "mysql"))
	dsn := os.Getenv("TK_REGISTER_SQL_DSN")
	if e := validateDatabase(driver, dsn); e != nil {
		return e
	}
	origin, secure, e := validateOrigin(os.Getenv("TK_REGISTER_PUBLIC_ORIGIN"))
	if e != nil {
		return e
	}
	admin := core.NormalizeEmail(os.Getenv("TK_REGISTER_ADMIN_EMAIL"))
	if !validEmail(admin) {
		return errors.New("a valid TK_REGISTER_ADMIN_EMAIL is required")
	}
	port, e := strconv.Atoi(env("TK_REGISTER_PORT", "3002"))
	if e != nil || port < 1 || port > 65535 {
		return errors.New("invalid TK_REGISTER_PORT")
	}
	key := os.Getenv("TK_REGISTER_ENCRYPTION_KEY")
	store, e := core.Open(driver, dsn, key)
	if e != nil {
		return e
	}
	defer store.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	sourceDriver := env("TK_REGISTER_GOAPI_SQL_DRIVER", "mysql")
	sourceDSN := env("TK_REGISTER_GOAPI_SQL_DSN", os.Getenv("SQL_DSN"))
	e = store.ImportGoAPISMTP(ctx, sourceDriver, sourceDSN)
	cancel()
	if e != nil {
		return e
	}
	opts, e := redis.ParseURL(env("TK_REGISTER_REDIS_URL", os.Getenv("REDIS_CONN_STRING")))
	if e != nil {
		return errors.New("a valid TK Register Redis URL is required")
	}
	opts.DialTimeout = 5 * time.Second
	opts.ReadTimeout = 3 * time.Second
	opts.WriteTimeout = 3 * time.Second
	redisClient := redis.NewClient(opts)
	defer redisClient.Close()
	ctx, cancel = context.WithTimeout(context.Background(), 5*time.Second)
	e = redisClient.Ping(ctx).Err()
	cancel()
	if e != nil {
		return errors.New("TK Register could not connect to Redis")
	}
	app := &app{store: store, redis: redisClient, mail: &core.Mailer{Store: store}, secret: key, adminEmail: admin, origin: origin, secure: secure, assets: env("TK_REGISTER_ASSETS", "/opt/tk-register-web-portal")}
	listener, e := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
	if e != nil {
		return fmt.Errorf("TK Register could not listen on port %d", port)
	}
	srv := &http.Server{Handler: app.Handler(), ReadHeaderTimeout: 10 * time.Second, ReadTimeout: 20 * time.Second, IdleTimeout: 90 * time.Second, MaxHeaderBytes: 1 << 20}
	workerCtx, stopWorker := context.WithCancel(context.Background())
	workerDone := make(chan struct{})
	go func() { defer close(workerDone); app.runAccountSync(workerCtx) }()
	defer func() { stopWorker(); <-workerDone }()
	done := make(chan error, 1)
	go func() { done <- srv.Serve(listener) }()
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, syscall.SIGINT, syscall.SIGTERM)
	defer signal.Stop(signals)
	log.Printf("TK Register ready on 127.0.0.1:%d", port)
	select {
	case <-signals:
	case e = <-done:
		if !errors.Is(e, http.ErrServerClosed) {
			return e
		}
	}
	stopWorker()
	shutdown, stop := context.WithTimeout(context.Background(), 20*time.Second)
	defer stop()
	return srv.Shutdown(shutdown)
}
func main() {
	if err := run(); err != nil {
		log.Printf("TK Register startup failed: %s", err)
		os.Exit(1)
	}
}

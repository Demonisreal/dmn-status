// dmn-status prueft Dienste und zeigt ihren Zustand an. Aufruf ohne Argumente zeigt die Befehle.
package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/Demonisreal/dmn-status/internal/alert"
	"github.com/Demonisreal/dmn-status/internal/auth"
	"github.com/Demonisreal/dmn-status/internal/check"
	"github.com/Demonisreal/dmn-status/internal/config"
	"github.com/Demonisreal/dmn-status/internal/monitor"
	"github.com/Demonisreal/dmn-status/internal/store"
	"github.com/Demonisreal/dmn-status/internal/web"
)

// wird beim build per -ldflags "-X main.version=..." gesetzt
var version = "dev"

const usage = `dmn-status serve
dmn-status admin set-password <name>   (passwort ueber stdin)
dmn-status healthcheck
dmn-status backup <datei>
dmn-status version`

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)))

	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, usage)
		os.Exit(2)
	}
	var err error
	switch args := os.Args[2:]; os.Args[1] {
	case "serve":
		err = serve()
	case "admin":
		if len(args) != 2 || args[0] != "set-password" {
			fmt.Fprintln(os.Stderr, usage)
			os.Exit(2)
		}
		err = withStore(func(ctx context.Context, st *store.Store) error {
			return setPassword(ctx, st, args[1], os.Stdin)
		})
		if err == nil {
			fmt.Fprintln(os.Stderr, "passwort gesetzt, alle sitzungen abgemeldet")
		}
	case "healthcheck":
		err = healthcheck()
	case "backup":
		if len(args) != 1 {
			fmt.Fprintln(os.Stderr, usage)
			os.Exit(2)
		}
		err = withStore(func(ctx context.Context, st *store.Store) error {
			return st.Backup(ctx, args[0])
		})
	case "version":
		fmt.Println(version)
	default:
		fmt.Fprintln(os.Stderr, usage)
		os.Exit(2)
	}
	if err != nil {
		slog.Error("befehl fehlgeschlagen", "cmd", os.Args[1], "err", err)
		os.Exit(1)
	}
}

func withStore(fn func(context.Context, *store.Store) error) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	st, err := store.Open(ctx, cfg.DBPath, time.Now)
	if err != nil {
		return err
	}
	return errors.Join(fn(ctx, st), st.Close())
}

func serve() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	st, err := store.Open(ctx, cfg.DBPath, time.Now)
	if err != nil {
		return err
	}
	defer st.Close()

	if err := bootstrap(ctx, st, cfg.AdminUser, cfg.AdminPassword); err != nil {
		return err
	}

	var mailer *alert.Mailer
	if cfg.MailEnabled() {
		mailer = &alert.Mailer{
			Host:    cfg.SMTPHost,
			Port:    cfg.SMTPPort,
			User:    cfg.SMTPUser,
			Pass:    cfg.SMTPPass,
			From:    cfg.MailFrom,
			To:      cfg.MailTo,
			BaseURL: cfg.BaseURL,
			Now:     time.Now,
		}
	} else {
		slog.Warn("mailversand nicht eingerichtet, SMTP_HOST und MAIL_TO fehlen")
	}

	checker := check.Checker{PrivateAllow: cfg.PrivateAllow, Block: selfAddrs(ctx, cfg.BaseURL)}
	mgr := monitor.New(st, checker, mailer)
	if err := mgr.Start(ctx); err != nil {
		return err
	}
	mgr.Maintain(ctx, cfg.RetentionDays)

	srv := &http.Server{
		Addr: cfg.Addr,
		Handler: web.New(web.Deps{
			Store:   st,
			Monitor: mgr,
			Mailer:  mailer,
			Config:  cfg,
			Version: version,
			Now:     time.Now,
		}),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    64 << 10,
		ErrorLog:          slog.NewLogLogger(slog.Default().Handler(), slog.LevelWarn),
	}
	errc := make(chan error, 1)
	go func() { errc <- srv.ListenAndServe() }()
	slog.Info("gestartet", "addr", cfg.Addr, "version", version)

	select {
	case err = <-errc:
	case <-ctx.Done():
		slog.Info("wird beendet")
	}
	stop()

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if serr := srv.Shutdown(shutdownCtx); serr != nil {
		slog.Error("http shutdown", "err", serr)
	}
	mgr.Wait()
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

// selfAddrs loest den Host aus BASE_URL einmal beim Start auf. Ein Check auf diese Adressen
// liefe ueber den eigenen Proxy zurueck. Ohne Aufloesung laeuft der Dienst ohne Sperre weiter.
func selfAddrs(ctx context.Context, baseURL string) []netip.Addr {
	if baseURL == "" {
		slog.Warn("BASE_URL fehlt, eigene adresse wird fuer checks nicht gesperrt")
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	u, err := url.Parse(baseURL)
	var ips []netip.Addr
	if err == nil {
		ips, err = net.DefaultResolver.LookupNetIP(ctx, "ip", u.Hostname())
	}
	if err != nil {
		slog.Warn("BASE_URL nicht aufloesbar, eigene adresse wird fuer checks nicht gesperrt", "err", err)
		return nil
	}
	cgnat := netip.MustParsePrefix("100.64.0.0/10")
	var out []netip.Addr
	for _, ip := range ips {
		ip = ip.WithZone("").Unmap()
		// bei split-dns kaeme hier eine interne adresse, die gehoert eher zu PRIVATE_ALLOW.
		// block wird vor PRIVATE_ALLOW geprueft und wuerde den eintrag sonst sperren.
		if !ip.IsPrivate() && !ip.IsLoopback() && !cgnat.Contains(ip) {
			out = append(out, ip)
		}
	}
	if len(out) > 0 {
		slog.Info("eigene adressen fuer checks gesperrt", "ips", out)
	}
	return out
}

// bootstrap legt den Admin aus ADMIN_USER/ADMIN_PASSWORD an, solange noch keiner existiert.
// Danach wird die Variable ignoriert, ein Passwortwechsel geht nur ueber set-password.
func bootstrap(ctx context.Context, st *store.Store, user, password string) error {
	_, _, err := st.Admin(ctx)
	switch {
	case errors.Is(err, store.ErrNotFound) && user == "":
		slog.Warn("kein admin angelegt: dmn-status admin set-password <name>")
		return nil
	case errors.Is(err, store.ErrNotFound):
		if err := st.SetAdmin(ctx, user, auth.Hash(password)); err != nil {
			return err
		}
		slog.Warn("admin aus der umgebung angelegt, ADMIN_PASSWORD aus .env entfernen")
		return nil
	case err != nil:
		return err
	}
	if password != "" {
		slog.Warn("admin existiert schon, ADMIN_PASSWORD wird ignoriert und gehoert aus .env entfernt")
	}
	return nil
}

func setPassword(ctx context.Context, st *store.Store, name string, in io.Reader) error {
	if name == "" || utf8.RuneCountInString(name) > 60 || strings.ContainsFunc(name, unicode.IsSpace) ||
		strings.ContainsFunc(name, unicode.IsControl) {
		return errors.New("name: bis 60 zeichen, keine leerzeichen")
	}
	line, err := bufio.NewReader(in).ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return err
	}
	// nur das zeilenende abschneiden, leerzeichen am rand gehoeren zum passwort
	password := strings.TrimSuffix(strings.TrimSuffix(line, "\n"), "\r")
	if utf8.RuneCountInString(password) < 12 {
		return errors.New("passwort: mindestens 12 zeichen")
	}
	return st.SetAdmin(ctx, name, auth.Hash(password))
}

func healthcheck() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	_, port, err := net.SplitHostPort(cfg.Addr)
	if err != nil {
		return err
	}
	return probe("http://" + net.JoinHostPort("127.0.0.1", port) + "/healthz")
}

func probe(url string) error {
	client := &http.Client{Timeout: 3 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		return err
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("status %d", resp.StatusCode)
	}
	return nil
}

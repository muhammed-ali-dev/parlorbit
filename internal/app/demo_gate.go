package app

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	_ "embed"
	"encoding/base64"
	"errors"
	"html/template"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

//go:embed demo_gate.html
var demoGateHTML string
var demoGateTemplate = template.Must(template.New("demo-gate").Parse(demoGateHTML))

const demoCookieName = "roomcade_demo"
const demoSessionDuration = 12 * time.Hour

func safeDemoNext(value string) string {
	parsed, err := url.Parse(value)
	if err != nil || !strings.HasPrefix(value, "/") || strings.HasPrefix(value, "//") || strings.ContainsAny(value, "\\\r\n") || parsed.IsAbs() || parsed.Host != "" {
		return "/"
	}
	if parsed.Path == "/demo/unlock" || parsed.Path == "/demo/logout" {
		return "/"
	}
	return value
}
func demoSignature(password, payload string) string {
	key := sha256.Sum256([]byte("roomcade-demo-v1:" + password))
	mac := hmac.New(sha256.New, key[:])
	mac.Write([]byte(payload))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}
func validDemoCookie(password, value string, now time.Time) bool {
	parts := strings.Split(value, ".")
	if len(parts) != 3 {
		return false
	}
	expiry, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil || expiry <= now.Unix() || expiry > now.Add(demoSessionDuration).Unix() {
		return false
	}
	payload := parts[0] + "." + parts[1]
	return hmac.Equal([]byte(parts[2]), []byte(demoSignature(password, payload)))
}
func (a *App) demoEntry(w http.ResponseWriter, r *http.Request, status int, message, next string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("X-Robots-Tag", "noindex, nofollow")
	w.WriteHeader(status)
	_ = demoGateTemplate.Execute(w, struct{ Error, Next string }{message, safeDemoNext(next)})
}
func (a *App) demoLoginAllowed(r *http.Request, now time.Time) bool {
	ip, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		ip = r.RemoteAddr
	}
	// Never trust client-supplied forwarding headers for a spending/access guard.
	minute := now.UTC().Format("2006-01-02T15:04")
	buckets := []string{"ip:" + hashToken(ip) + ":" + minute, "minute:" + minute, "hour:" + now.UTC().Format("2006-01-02T15")}
	caps := []int{5, 20, 60}
	tx, err := a.store.db.BeginTx(r.Context(), nil)
	if err != nil {
		return false
	}
	defer tx.Rollback()
	for i, key := range buckets {
		count := 0
		// Insert first to acquire the write lock before checking concurrent counters.
		if _, err = tx.ExecContext(r.Context(), "INSERT OR IGNORE INTO demo_login_attempts(bucket,attempts,created_at) VALUES(?,0,?)", key, now.Unix()); err != nil {
			return false
		}
		if err = tx.QueryRowContext(r.Context(), "SELECT attempts FROM demo_login_attempts WHERE bucket=?", key).Scan(&count); err != nil || count >= caps[i] {
			return false
		}
	}
	for _, key := range buckets {
		if _, err = tx.ExecContext(r.Context(), "UPDATE demo_login_attempts SET attempts=attempts+1 WHERE bucket=?", key); err != nil {
			return false
		}
	}
	if _, err = tx.ExecContext(r.Context(), "DELETE FROM demo_login_attempts WHERE created_at<?", now.Add(-24*time.Hour).Unix()); err != nil {
		return false
	}
	return tx.Commit() == nil
}
func (a *App) demoUnlock(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		a.demoEntry(w, r, 200, "", r.URL.Query().Get("next"))
		return
	}
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "GET, POST")
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	if !a.originAllowed(r) {
		writeError(w, r, 403, "origin_rejected", "This request did not come from the demo.", nil)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 2048)
	if err := r.ParseForm(); err != nil {
		a.demoEntry(w, r, 400, "Please enter the demo password.", "/")
		return
	}
	next := safeDemoNext(r.PostForm.Get("next"))
	if !a.demoLoginAllowed(r, time.Now()) {
		w.Header().Set("Retry-After", "60")
		a.demoEntry(w, r, 429, "Too many attempts. Please try again later.", next)
		return
	}
	supplied := sha256.Sum256([]byte(r.PostForm.Get("password")))
	expected := sha256.Sum256([]byte(a.cfg.DemoPassword))
	if subtle.ConstantTimeCompare(supplied[:], expected[:]) != 1 {
		a.demoEntry(w, r, 401, "That password doesn’t match. Try again.", next)
		return
	}
	nonce, err := randomToken(24)
	if err != nil {
		a.demoEntry(w, r, 503, "Couldn’t open the demo. Try again later.", next)
		return
	}
	expires := time.Now().Add(demoSessionDuration)
	payload := strconv.FormatInt(expires.Unix(), 10) + "." + nonce
	http.SetCookie(w, &http.Cookie{Name: demoCookieName, Value: payload + "." + demoSignature(a.cfg.DemoPassword, payload), Path: "/", HttpOnly: true, Secure: a.cfg.SecureCookies, SameSite: http.SameSiteStrictMode, MaxAge: int(demoSessionDuration.Seconds()), Expires: expires})
	http.Redirect(w, r, next, http.StatusSeeOther)
}
func (a *App) demoGate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if a.cfg.DemoPassword == "" {
			next.ServeHTTP(w, r)
			return
		}
		switch r.URL.Path {
		case "/healthz", "/readyz", "/metrics", "/api/v1/livekit/webhook":
			next.ServeHTTP(w, r)
			return
		case "/battle/nunito.woff2", "/battle/fredoka.woff2":
			if r.Method == http.MethodGet {
				a.handleStatic(w, r)
				return
			}
		case "/demo/unlock":
			a.demoUnlock(w, r)
			return
		}
		if cookie, err := r.Cookie(demoCookieName); err == nil && validDemoCookie(a.cfg.DemoPassword, cookie.Value, time.Now()) {
			next.ServeHTTP(w, r)
			return
		}
		if r.Method == http.MethodGet && !strings.HasPrefix(r.URL.Path, "/api/") && (strings.Contains(r.Header.Get("Accept"), "text/html") || !strings.Contains(r.URL.Path, ".")) {
			a.demoEntry(w, r, 200, "", r.URL.RequestURI())
			return
		}
		writeError(w, r, http.StatusUnauthorized, "demo_password_required", "Enter the demo password first.", nil)
	})
}
func validateDemoConfig(cfg Config) error {
	if cfg.DemoPasswordRequired && cfg.DemoPassword == "" {
		return errors.New("DEMO_PASSWORD is required for this deployment")
	}
	if cfg.DemoPassword != "" && (len(cfg.DemoPassword) < 12 || len(cfg.DemoPassword) > 256) {
		return errors.New("DEMO_PASSWORD must be 12 to 256 bytes long")
	}
	return nil
}

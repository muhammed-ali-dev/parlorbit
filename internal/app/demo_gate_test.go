package app

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const testDemoPassword = "test-only-demo-password"

func newGatedTestApp(t *testing.T) *App {
	t.Helper()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "index.html"), []byte("private application"), 0600); err != nil {
		t.Fatal(err)
	}
	a, err := New(Config{DemoPassword: testDemoPassword, DemoPasswordRequired: true, DatabasePath: filepath.Join(root, "demo.db"), StaticDir: root, AllowedOrigins: []string{"http://demo.test"}, SecureCookies: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { a.Close() })
	return a
}
func gateRequest(a *App, method, path, body string, cookie *http.Cookie) *httptest.ResponseRecorder {
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	if method == http.MethodPost {
		request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	if cookie != nil {
		request.AddCookie(cookie)
	}
	res := httptest.NewRecorder()
	a.Handler().ServeHTTP(res, request)
	return res
}
func TestDemoGateBlocksSiteAPIsAndDirectArena(t *testing.T) {
	a := newGatedTestApp(t)
	for _, path := range []string{"/", "/houses/example/rooms/test", "/battle/arena.html?houseId=h&roomId=r"} {
		request := httptest.NewRequest("GET", path, nil)
		request.Header.Set("Accept", "text/html")
		response := httptest.NewRecorder()
		a.Handler().ServeHTTP(response, request)
		if response.Code != 200 || !strings.Contains(response.Body.String(), "Due to current model costs, you need a password for this demo.") || strings.Contains(response.Body.String(), "private application") {
			t.Fatal("page bypass", path, response.Code)
		}
		if strings.Contains(response.Body.String(), testDemoPassword) {
			t.Fatal("password exposed")
		}
	}
	for _, path := range []string{"/api/v1/bootstrap", "/api/v1/battle/events?houseId=h&roomId=r", "/api/v1/realtime", "/assets/app.js"} {
		if r := gateRequest(a, "GET", path, "", nil); r.Code != 401 {
			t.Fatal("direct bypass", path, r.Code)
		}
	}
	if r := gateRequest(a, "POST", "/api/v1/battle/advance", "{}", nil); r.Code != 401 {
		t.Fatal("AI command bypass")
	}
	for _, path := range []string{"/healthz", "/readyz"} {
		if r := gateRequest(a, "GET", path, "", nil); r.Code != 200 {
			t.Fatal("health check blocked")
		}
	}
	if r := gateRequest(a, "GET", "/metrics", "", nil); r.Code != 401 {
		t.Fatal("metrics exposed")
	}
}
func TestDemoUnlockCookieAndSafeRedirect(t *testing.T) {
	a := newGatedTestApp(t)
	wrong := gateRequest(a, "POST", "/demo/unlock", url.Values{"password": {"wrong"}, "next": {"/"}}.Encode(), nil)
	if wrong.Code != 401 || len(wrong.Result().Cookies()) != 0 {
		t.Fatal("wrong password accepted")
	}
	response := gateRequest(a, "POST", "/demo/unlock", url.Values{"password": {testDemoPassword}, "next": {"/houses/demo?x=1"}}.Encode(), nil)
	if response.Code != 303 || response.Header().Get("Location") != "/houses/demo?x=1" {
		t.Fatal("unlock failed", response.Code)
	}
	cookies := response.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatal("missing session cookie")
	}
	cookie := cookies[0]
	if !cookie.HttpOnly || !cookie.Secure || cookie.SameSite != http.SameSiteStrictMode || cookie.MaxAge != 43200 || strings.Contains(cookie.Value, testDemoPassword) {
		t.Fatal("insecure session cookie")
	}
	if r := gateRequest(a, "GET", "/", "", cookie); !strings.Contains(r.Body.String(), "private application") {
		t.Fatal("valid cookie did not unlock")
	}
	// Access to the app does not replace normal House membership authorization.
	if r := gateRequest(a, "GET", "/api/v1/bootstrap", "", cookie); r.Code != 401 || !strings.Contains(r.Body.String(), "session_required") {
		t.Fatal("gate bypassed membership/session authorization")
	}
	for _, value := range []string{"https://evil.test", "//evil.test", "/\\evil.test", "/\r\nx"} {
		if safeDemoNext(value) != "/" {
			t.Fatal("open redirect allowed")
		}
	}
	forged := *cookie
	forged.Value += "forged"
	if r := gateRequest(a, "GET", "/", "", &forged); strings.Contains(r.Body.String(), "private application") {
		t.Fatal("forged cookie accepted")
	}
}
func TestDemoSessionExpiryAndRotation(t *testing.T) {
	now := time.Now()
	// Construct a real issued session via the unlock route.
	a := newGatedTestApp(t)
	r := gateRequest(a, "POST", "/demo/unlock", url.Values{"password": {testDemoPassword}}.Encode(), nil)
	value := r.Result().Cookies()[0].Value
	if !validDemoCookie(testDemoPassword, value, now) {
		t.Fatal("fresh session invalid")
	}
	if validDemoCookie(testDemoPassword, value, now.Add(13*time.Hour)) {
		t.Fatal("expired session valid")
	}
	if validDemoCookie("changed-demo-password", value, now) {
		t.Fatal("rotation retained old session")
	}
}
func TestDemoLoginThrottlePersistsAndRejectsCrossOrigin(t *testing.T) {
	a := newGatedTestApp(t)
	request := httptest.NewRequest("POST", "/demo/unlock", strings.NewReader("password=wrong"))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.Header.Set("Origin", "https://evil.test")
	r := httptest.NewRecorder()
	a.Handler().ServeHTTP(r, request)
	if r.Code != 403 {
		t.Fatal("cross-origin login accepted")
	}
	for i := 0; i < 5; i++ {
		r = gateRequest(a, "POST", "/demo/unlock", "password=wrong", nil)
		if r.Code != 401 {
			t.Fatal("unexpected throttle", r.Code)
		}
	}
	r = gateRequest(a, "POST", "/demo/unlock", url.Values{"password": {testDemoPassword}}.Encode(), nil)
	if r.Code != 429 || r.Header().Get("Retry-After") == "" {
		t.Fatal("brute-force limit not enforced")
	}
	cfg := a.cfg
	a.Close()
	reopened, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if response := gateRequest(reopened, "POST", "/demo/unlock", "password=wrong", nil); response.Code != 429 {
		t.Fatal("restart cleared login attempts")
	}

}
func TestDemoRequiredConfigFailsClosed(t *testing.T) {
	if _, err := New(Config{DemoPasswordRequired: true}); err == nil {
		t.Fatal("missing required password opened app")
	}
	if err := validateDemoConfig(Config{DemoPassword: "short"}); err == nil {
		t.Fatal("weak passcode accepted")
	}
}

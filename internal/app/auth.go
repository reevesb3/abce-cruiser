package app

// Credential handling. All three secrets live in the OS-native credential
// store via go-keyring — Windows Credential Manager on Windows, Keychain on
// macOS — under a fixed store key (not shown to the user):
//   entry "login"   -> "email\npassword"
//   entry "cookies" -> "keepCookie\nsessionCookie"
//   entry "profile" -> "fullName\nphone\nmobile"
// Nothing is ever written to disk in plaintext, so the binary and source stay
// free of personal data.

import (
	"bufio"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/zalando/go-keyring"
	"golang.org/x/term"
)

// keyringService is a stable credential-store namespace. It is intentionally
// NOT the app name: changing it would orphan already-stored credentials.
const keyringService = "SuperSaaSSniper"

var baseURL = mustParse("https://www.supersaas.com")

func mustParse(s string) *url.URL {
	u, err := url.Parse(s)
	if err != nil {
		panic(err)
	}
	return u
}

func keyringGetLines(entry string, n int) ([]string, bool) {
	v, err := keyring.Get(keyringService, entry)
	if err != nil {
		return nil, false
	}
	parts := strings.Split(v, "\n")
	for len(parts) < n {
		parts = append(parts, "")
	}
	return parts, true
}

func keyringSetLines(entry string, lines ...string) error {
	return keyring.Set(keyringService, entry, strings.Join(lines, "\n"))
}

// ----------------------------- HTTP session ---------------------------------

const userAgent = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/150.0.0.0 Safari/537.36 Edg/150.0.0.0"

func newClient() *http.Client {
	jar, _ := cookiejar.New(nil)
	return &http.Client{Jar: jar, Timeout: 30 * time.Second}
}

// fetch performs a request and returns (body, finalURL, error). finalURL is
// the post-redirect URL — used to detect a bounce to the login page.
func fetch(client *http.Client, method, target, body, referer string) (string, string, error) {
	var rdr io.Reader
	if body != "" {
		rdr = strings.NewReader(body)
	}
	req, err := http.NewRequest(method, target, rdr)
	if err != nil {
		return "", "", err
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,image/avif,image/webp,image/apng,*/*;q=0.8")
	req.Header.Set("Accept-Language", "en-US,en;q=0.9")
	req.Header.Set("Origin", "https://www.supersaas.com")
	if referer != "" {
		req.Header.Set("Referer", referer)
	}
	req.Header.Set("Upgrade-Insecure-Requests", "1")
	if method == http.MethodPost {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", "", err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", "", err
	}
	return string(b), resp.Request.URL.String(), nil
}

// A page "needs auth" when it is (or redirected to) the login form.
func isLoginPage(page, finalURL string) bool {
	return strings.Contains(page, `name="password"`) || strings.Contains(finalURL, "/schedule/login/")
}

// ----------------------------- cookie cache ---------------------------------

func loadCookiesIntoJar(client *http.Client) bool {
	lines, ok := keyringGetLines("cookies", 2)
	if !ok || lines[1] == "" {
		return false
	}
	var cs []*http.Cookie
	if lines[0] != "" {
		cs = append(cs, &http.Cookie{Name: "SS_31343_keep", Value: lines[0], Path: "/"})
	}
	cs = append(cs, &http.Cookie{Name: "_SS_s", Value: lines[1], Path: "/"})
	client.Jar.SetCookies(baseURL, cs)
	return true
}

func saveCookiesFromJar(client *http.Client) {
	var keep, sess string
	for _, c := range client.Jar.Cookies(baseURL) {
		switch {
		case c.Name == "_SS_s":
			sess = c.Value
		case strings.HasPrefix(c.Name, "SS_") && strings.HasSuffix(c.Name, "_keep"):
			keep = c.Value
		}
	}
	if sess == "" {
		return
	}
	if err := keyringSetLines("cookies", keep, sess); err != nil {
		logf("Note: could not cache session cookie: %v", err)
	}
}

// ----------------------------- profile cache --------------------------------

func loadProfile() (profileInfo, bool) {
	lines, ok := keyringGetLines("profile", 3)
	if !ok || lines[0] == "" {
		return profileInfo{}, false
	}
	return profileInfo{FullName: lines[0], Phone: lines[1], Mobile: lines[2]}, true
}

func saveProfile(p profileInfo) {
	if p.FullName == "" {
		return
	}
	if err := keyringSetLines("profile", p.FullName, p.Phone, p.Mobile); err != nil {
		logf("Note: could not cache profile: %v", err)
	}
}

// ----------------------------- login / reauth -------------------------------

// One-time interactive setup: the user types the password into a masked prompt;
// it goes straight into the OS credential store. Never shown, logged, or written
// to a file.
func saveLoginPrompt() {
	reader := bufio.NewReader(os.Stdin)
	fmt.Print("SuperSaaS login email: ")
	email, _ := reader.ReadString('\n')
	email = strings.TrimSpace(email)

	fmt.Print("SuperSaaS password: ")
	p1, err := term.ReadPassword(int(os.Stdin.Fd()))
	fmt.Println()
	if err != nil {
		fmt.Println("Could not read password (run from a real terminal):", err)
		return
	}
	fmt.Print("Re-enter password: ")
	p2, _ := term.ReadPassword(int(os.Stdin.Fd()))
	fmt.Println()
	if string(p1) != string(p2) {
		fmt.Println("Passwords did not match - nothing saved.")
		return
	}
	if err := keyringSetLines("login", email, string(p1)); err != nil {
		fmt.Println("Could not save to the OS credential store:", err)
		return
	}
	fmt.Printf("Saved login for [%s] to the OS credential store (service %q).\n", email, keyringService)
	fmt.Println("Cruiser will now auto-reauthenticate if the session cookie expires.")
}

// Re-login with the stored credential and refresh the client's cookies.
// Flow (verified against the live server): GET the login page FIRST — SuperSaaS
// binds the login POST to the pre-login session cookie issued there; a cold
// POST is rejected regardless of password. Then POST name+password (no CSRF).
func restoreSession(client *http.Client) bool {
	lines, ok := keyringGetLines("login", 2)
	if !ok || lines[0] == "" {
		logf("Reauth: no stored login (run: cruiser -save-login).")
		return false
	}
	if _, _, err := fetch(client, http.MethodGet, loginURL, "", ""); err != nil {
		logf("Reauth: could not load login page: %v", err)
		return false
	}
	body := "name=" + escapeData(lines[0]) + "&password=" + escapeData(lines[1]) + "&button="
	page, _, err := fetch(client, http.MethodPost, loginURL, body, loginURL)
	if err != nil {
		logf("Reauth: login request failed: %v", err)
		return false
	}
	if strings.Contains(page, `name="password"`) {
		logf("Reauth: login rejected (bad credentials?). Re-run -save-login.")
		return false
	}
	sessOK := false
	for _, c := range client.Jar.Cookies(baseURL) {
		if c.Name == "_SS_s" {
			sessOK = true
		}
	}
	if !sessOK {
		logf("Reauth: no session cookie returned - login may have failed.")
		return false
	}
	saveCookiesFromJar(client)
	logf("Reauth: session cookie refreshed successfully (cached).")
	return true
}

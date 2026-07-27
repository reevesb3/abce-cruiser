package app

// Cookie-cache tests exercise the jar<->value mapping only (cookiesToJar /
// cookiesFromJar). They deliberately avoid loadCookiesIntoJar /
// saveCookiesFromJar, which read and write the real OS credential store — the
// same live session cookie a running Cruiser relies on.

import (
	"net/http"
	"net/http/cookiejar"
	"testing"
)

func TestCookieJarRoundTrip(t *testing.T) {
	jar, _ := cookiejar.New(nil)
	cookiesToJar(jar, "keepval", "sessval")
	if keep, sess := cookiesFromJar(jar); keep != "keepval" || sess != "sessval" {
		t.Errorf("round-trip = keep=%q sess=%q, want keepval/sessval", keep, sess)
	}

	// No keep cookie: only the session cookie is stored; keep comes back blank.
	jarNoKeep, _ := cookiejar.New(nil)
	cookiesToJar(jarNoKeep, "", "onlysess")
	if keep, sess := cookiesFromJar(jarNoKeep); keep != "" || sess != "onlysess" {
		t.Errorf("no-keep round-trip = keep=%q sess=%q, want \"\"/onlysess", keep, sess)
	}
}

func TestCookiesFromJarIgnoresForeignCookies(t *testing.T) {
	jar, _ := cookiejar.New(nil)
	jar.SetCookies(baseURL, []*http.Cookie{
		{Name: "_SS_s", Value: "the-session", Path: "/"},
		{Name: "SS_31343_keep", Value: "the-keep", Path: "/"},
		{Name: "_ga", Value: "analytics-junk", Path: "/"}, // unrelated cookie
	})
	keep, sess := cookiesFromJar(jar)
	if keep != "the-keep" || sess != "the-session" {
		t.Errorf("extract = keep=%q sess=%q, want the-keep/the-session", keep, sess)
	}
}

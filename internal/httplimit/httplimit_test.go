package httplimit

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestReadLimitedAllowsExactlyAtTheLimit(t *testing.T) {
	data, err := ReadLimited(strings.NewReader("12345"), 5)
	if err != nil {
		t.Fatalf("ReadLimited: %v", err)
	}
	if string(data) != "12345" {
		t.Errorf("data = %q, want %q", data, "12345")
	}
}

func TestReadLimitedRejectsOneByteOverTheLimit(t *testing.T) {
	_, err := ReadLimited(strings.NewReader("123456"), 5)
	if err == nil {
		t.Fatal("ReadLimited succeeded for a 6-byte body with a 5-byte limit, want ErrFetchLimitExceeded")
	}
	if !errors.Is(err, ErrFetchLimitExceeded) {
		t.Errorf("error = %v, want it to wrap ErrFetchLimitExceeded", err)
	}
}

func TestClientRejectsTooManyRedirects(t *testing.T) {
	var target *httptest.Server
	hops := 0
	target = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hops++
		http.Redirect(w, r, target.URL+"/next", http.StatusFound)
	}))
	defer target.Close()

	c := Client(&http.Client{}, 2)
	_, err := c.Get(target.URL)
	if err == nil {
		t.Fatal("Get succeeded through an infinite redirect loop with maxRedirects=2, want an error")
	}
	if !errors.Is(err, ErrFetchLimitExceeded) {
		t.Errorf("error = %v, want it to wrap ErrFetchLimitExceeded", err)
	}
}

func TestClientAllowsRedirectsWithinTheLimit(t *testing.T) {
	final := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("ok"))
	}))
	defer final.Close()
	redirector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, final.URL, http.StatusFound)
	}))
	defer redirector.Close()

	c := Client(&http.Client{}, 5)
	resp, err := c.Get(redirector.URL)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200", resp.StatusCode)
	}
}

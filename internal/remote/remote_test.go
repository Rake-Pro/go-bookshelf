package remote_test

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/rake-pro/go-bookshelf/internal/remote"
)

// With no metadata provider configured there is no outbound network access at
// all, whatever the URL.
func TestDisabledByDefault(t *testing.T) {
	f := remote.New(false, false)
	if f.Enabled() {
		t.Fatal("fetcher reports enabled with no provider configured")
	}
	for _, raw := range []string{"https://example.com/cover.jpg", "http://example.com/x", "file:///etc/passwd"} {
		if _, _, err := f.Get(context.Background(), raw); !errors.Is(err, remote.ErrDisabled) {
			t.Errorf("Get(%q) = %v, want ErrDisabled", raw, err)
		}
	}
}

func TestRejectsNonHTTPSchemes(t *testing.T) {
	f := remote.New(true, false)
	for _, raw := range []string{
		"file:///etc/passwd",
		"ftp://example.com/x",
		"gopher://example.com:70/",
		"data:text/plain;base64,aGk=",
	} {
		if err := f.CheckURL(raw); !errors.Is(err, remote.ErrScheme) {
			t.Errorf("CheckURL(%q) = %v, want ErrScheme", raw, err)
		}
	}
}

func TestRejectsPrivateAndLoopbackLiterals(t *testing.T) {
	f := remote.New(true, false)
	for _, raw := range []string{
		"http://127.0.0.1/x",
		"http://[::1]/x",
		"http://10.1.2.3/x",
		"http://192.168.1.1/x",
		"http://172.16.9.9/x",
		"http://169.254.169.254/latest/meta-data/",
		"http://0.0.0.0/x",
		"http://100.64.1.1/x",
	} {
		if err := f.CheckURL(raw); !errors.Is(err, remote.ErrBlocked) {
			t.Errorf("CheckURL(%q) = %v, want ErrBlocked", raw, err)
		}
	}
	for _, raw := range []string{"https://example.com/cover.jpg", "http://192.0.2.10/x"} {
		if err := f.CheckURL(raw); err != nil {
			t.Errorf("CheckURL(%q) = %v, want nil", raw, err)
		}
	}
}

// The dial-time guard is what stops a hostname that resolves to a private
// address, which the URL check alone cannot see.
func TestDialGuardBlocksLoopbackServer(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("secret"))
	}))
	defer srv.Close()

	f := remote.New(true, false)
	if _, _, err := f.Get(context.Background(), srv.URL); err == nil {
		t.Fatal("expected the loopback test server to be refused")
	}
}

func TestAllowPrivateOptIn(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/jpeg")
		w.Write([]byte("jpegbytes"))
	}))
	defer srv.Close()

	f := remote.New(true, true)
	body, ct, err := f.Get(context.Background(), srv.URL)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if string(body) != "jpegbytes" || ct != "image/jpeg" {
		t.Errorf("body = %q, content-type = %q", body, ct)
	}
	if !f.AddressAllowed(net.ParseIP("127.0.0.1")) {
		t.Error("loopback should be allowed once explicitly opted in")
	}
}

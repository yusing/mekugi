package httptransport_test

import (
	"net/http"
	"net/http/cookiejar"
	"testing"
	"time"

	"github.com/yusing/mekugi/libraries/httptransport"
)

func TestClientPreservesSuppliedConfiguration(t *testing.T) {
	transport := &http.Transport{MaxIdleConns: 17}
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	redirectCalls := 0
	original := &http.Client{
		Transport: transport,
		Timeout:   3 * time.Second,
		Jar:       jar,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			redirectCalls++
			return http.ErrUseLastResponse
		},
	}
	client := httptransport.Client(original)
	if client == original || client.Transport != transport || client.Jar != jar || client.Timeout != original.Timeout {
		t.Fatalf("client configuration was not preserved in an independent client: %+v", client)
	}
	if err := client.CheckRedirect(nil, nil); err != http.ErrUseLastResponse || redirectCalls != 1 {
		t.Fatalf("redirect policy changed: calls=%d, error=%v", redirectCalls, err)
	}
	client.Timeout = time.Second
	client.Transport = nil
	client.Jar = nil
	client.CheckRedirect = nil
	if original.Timeout != 3*time.Second || original.Transport != transport || original.Jar != jar || original.CheckRedirect == nil {
		t.Fatal("changing the returned client mutated the supplied client")
	}
}

func TestClientDefaultTransportsAreIndependent(t *testing.T) {
	original := &http.Client{Timeout: time.Second}
	first := httptransport.Client(original).Transport.(*http.Transport)
	second := httptransport.Client(nil).Transport.(*http.Transport)
	standard := http.DefaultTransport.(*http.Transport)
	if first == second || first == standard || second == standard {
		t.Fatal("default clients share a transport")
	}
	if original.Transport != nil {
		t.Fatal("constructing defaults mutated the supplied client")
	}
	if first.DialContext == nil || second.DialContext == nil {
		t.Fatal("default transport has no dialer")
	}
	first.MaxIdleConns = second.MaxIdleConns + 1
	if first.MaxIdleConns == second.MaxIdleConns || second.MaxIdleConns != standard.MaxIdleConns {
		t.Fatal("changing a cloned transport changed another transport")
	}
}

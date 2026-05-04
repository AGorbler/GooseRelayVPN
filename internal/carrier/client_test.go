package carrier

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/kianmhz/GooseRelayVPN/internal/frame"
)

const testKeyHex = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

// echoServer decodes the incoming batch, echoes each frame's payload back
// (with the SYN bit cleared and seq reset per session), and returns it.
func echoServer(t *testing.T, aead *frame.Crypto) (*httptest.Server, *int) {
	t.Helper()
	var hits int
	var mu sync.Mutex
	rxSeqBySession := map[[frame.SessionIDLen]byte]uint64{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		hits++
		mu.Unlock()
		body, _ := io.ReadAll(r.Body)
		clientID, in, err := frame.DecodeBatch(aead, body)
		if err != nil {
			t.Errorf("server decode: %v", err)
			w.WriteHeader(500)
			return
		}
		var out []*frame.Frame
		mu.Lock()
		for _, f := range in {
			seq := rxSeqBySession[f.SessionID]
			rxSeqBySession[f.SessionID] = seq + 1
			out = append(out, &frame.Frame{
				SessionID: f.SessionID,
				Seq:       seq,
				Payload:   f.Payload,
			})
		}
		mu.Unlock()
		respBody, _ := frame.EncodeBatch(aead, clientID, out)
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write(respBody)
	}))
	return srv, &hits
}

func TestCarrier_RoundTripEcho(t *testing.T) {
	aead, err := frame.NewCryptoFromHexKey(testKeyHex)
	if err != nil {
		t.Fatalf("crypto: %v", err)
	}
	srv, _ := echoServer(t, aead)
	defer srv.Close()

	c, err := New(Config{
		ScriptURLs: []string{srv.URL},
		AESKeyHex:  testKeyHex,
	})
	if err != nil {
		t.Fatalf("new client: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan struct{})
	go func() {
		_ = c.Run(ctx)
		close(done)
	}()

	s := c.NewSession("example.com:80")
	s.EnqueueTx([]byte("hello"))

	// Read the echoed payload from the session's RxChan.
	select {
	case got := <-s.RxChan:
		if string(got) != "hello" {
			t.Fatalf("got %q want %q", got, "hello")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timeout waiting for echoed payload")
	}

	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run() did not return after cancel")
	}
}

func TestCarrier_UnknownSessionFramesDropped(t *testing.T) {
	aead, _ := frame.NewCryptoFromHexKey(testKeyHex)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Always reply with one frame for an unknown session ID.
		var unknown [frame.SessionIDLen]byte
		for i := range unknown {
			unknown[i] = 0xEE
		}
		var ghostClient [frame.ClientIDLen]byte
		body, _ := frame.EncodeBatch(aead, ghostClient, []*frame.Frame{
			{SessionID: unknown, Seq: 0, Payload: []byte("ghost")},
		})
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write(body)
	}))
	defer srv.Close()

	c, err := New(Config{ScriptURLs: []string{srv.URL}, AESKeyHex: testKeyHex})
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = c.Run(ctx) }()

	// Just let it run a couple of poll cycles. A panic / data race here is
	// the failure mode; the assertion is "doesn't crash."
	time.Sleep(200 * time.Millisecond)
}

func TestCarrier_PollOnceDropsNonBatchPayload(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("<!doctype html><html><body>quota exceeded</body></html>"))
	}))
	defer srv.Close()

	c, err := New(Config{ScriptURLs: []string{srv.URL}, AESKeyHex: testKeyHex})
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	c.httpClients = []*http.Client{srv.Client()}

	if didWork := c.pollOnce(context.Background()); didWork {
		t.Fatal("expected no work for non-batch relay payload")
	}
}

func TestIsLikelyNonBatchRelayPayload(t *testing.T) {
	tests := []struct {
		name string
		in   []byte
		want bool
	}{
		{name: "html", in: []byte("<html>oops</html>"), want: true},
		{name: "doctype", in: []byte("<!DOCTYPE html>"), want: true},
		{name: "json", in: []byte(`{"e":"quota"}`), want: true},
		{name: "http", in: []byte("HTTP/1.1 502 Bad Gateway"), want: true},
		{name: "base64ish", in: []byte("QUJDRA=="), want: false},
		{name: "empty", in: []byte(" \r\n\t "), want: false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := isLikelyNonBatchRelayPayload(tc.in); got != tc.want {
				t.Fatalf("got %v want %v", got, tc.want)
			}
		})
	}
}

func TestCarrier_FailsOverToHealthyScriptURLWithoutTxLoss(t *testing.T) {
	aead, err := frame.NewCryptoFromHexKey(testKeyHex)
	if err != nil {
		t.Fatalf("crypto: %v", err)
	}

	badSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte("quota"))
	}))
	defer badSrv.Close()

	goodSrv, _ := echoServer(t, aead)
	defer goodSrv.Close()

	c, err := New(Config{ScriptURLs: []string{badSrv.URL, goodSrv.URL}, AESKeyHex: testKeyHex})
	if err != nil {
		t.Fatalf("new client: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() {
		_ = c.Run(ctx)
		close(done)
	}()

	s := c.NewSession("example.com:80")
	s.EnqueueTx([]byte("hello-failover"))

	select {
	case got := <-s.RxChan:
		if string(got) != "hello-failover" {
			t.Fatalf("got %q want %q", got, "hello-failover")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timeout waiting for failover response")
	}

	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run() did not return after cancel")
	}
}

// TestCarrier_PureDownloadIdleCap is the regression test for issue #41
// (excessive upload during downloads in v1.4.1). Before the fix, the
// pure-download branch let numWorkers-1 workers each hold an idle long-poll
// concurrently. Every downstream chunk woke all of them; only one received
// the chunk while the rest re-POSTed empty bodies, multiplying upload
// bandwidth by the worker count. The cap in pure-download mode is now
// max(pureDownloadIdleCap, len(endpoints)): one poll per endpoint, with a
// floor of 2 for single-endpoint configs. This fixes both the upload
// amplification of #41 and the throughput collapse in multi-endpoint configs
// after initial SYNs completed (issue #73).
func TestCarrier_PureDownloadIdleCap(t *testing.T) {
	aead, err := frame.NewCryptoFromHexKey(testKeyHex)
	if err != nil {
		t.Fatalf("crypto: %v", err)
	}

	var (
		mu       sync.Mutex
		current  int
		peak     int
		totalReq int
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		current++
		totalReq++
		if current > peak {
			peak = current
		}
		mu.Unlock()
		// Hold the request long enough that any racing worker gets a chance
		// to attempt its own idle poll before this one returns. Long enough
		// that a thundering herd would be visible in the peak count.
		time.Sleep(400 * time.Millisecond)
		mu.Lock()
		current--
		mu.Unlock()

		// Empty batch response — keeps the client in pure-download mode.
		var clientID [frame.ClientIDLen]byte
		body, _ := frame.EncodeBatch(aead, clientID, nil)
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write(body)
	}))
	defer srv.Close()

	// Four distinct endpoints → numWorkers = workersPerEndpoint × 4 = 12.
	// Pre-fix idleCap in pure-download mode would have been 11. New cap is
	// max(pureDownloadIdleCap, len(endpoints)) = max(2, 4) = 4.
	urls := []string{
		srv.URL + "/a", srv.URL + "/b", srv.URL + "/c", srv.URL + "/d",
	}
	c, err := New(Config{ScriptURLs: urls, AESKeyHex: testKeyHex})
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	c.httpClients = []*http.Client{srv.Client()}

	if c.numWorkers <= pureDownloadIdleCap+1 {
		t.Fatalf("test setup: need numWorkers (%d) > pureDownloadIdleCap+1 (%d) "+
			"to actually exercise the cap", c.numWorkers, pureDownloadIdleCap+1)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() {
		_ = c.Run(ctx)
		close(done)
	}()

	// Let the workers spin for several poll cycles so the peak measurement is
	// stable. With 400ms hold + 10ms re-entry, ~1.5s covers ≥3 cycles.
	time.Sleep(1500 * time.Millisecond)
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run() did not return after cancel")
	}

	mu.Lock()
	gotPeak := peak
	gotTotal := totalReq
	mu.Unlock()

	wantCap := pureDownloadIdleCap
	if len(c.endpoints) > wantCap {
		wantCap = len(c.endpoints)
	}
	if gotPeak > wantCap {
		t.Fatalf("peak concurrent idle long-polls = %d, want ≤ %d "+
			"(numWorkers=%d, len(endpoints)=%d, totalReq=%d)",
			gotPeak, wantCap, c.numWorkers, len(c.endpoints), gotTotal)
	}
	if gotPeak == 0 {
		t.Fatal("no polls were issued; test did not exercise the cap")
	}
}

// TestCarrier_IdleBackoffSchedule guards the adaptive backoff curve so a
// future "tweak" cannot accidentally regress to a tight 10ms loop on idle
// workers (the upload-amplification half of issue #41).
func TestCarrier_IdleBackoffSchedule(t *testing.T) {
	cases := []struct {
		n    int
		want time.Duration
	}{
		{0, pollIdleSleep},
		{2, pollIdleSleep},
		{3, 50 * time.Millisecond},
		{9, 50 * time.Millisecond},
		{10, 250 * time.Millisecond},
		{29, 250 * time.Millisecond},
		{30, time.Second},
		{1000, time.Second},
	}
	for _, tc := range cases {
		if got := idleBackoff(tc.n); got != tc.want {
			t.Errorf("idleBackoff(%d) = %v, want %v", tc.n, got, tc.want)
		}
	}
}

package idempotency

import (
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
)

func TestDurableClaimsPreventReplayAfterRestart(t *testing.T) {
	path := "file:" + filepath.Join(t.TempDir(), "ledger.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	r, err := s.Claim("send-1", "hash")
	if err != nil || !r.Fresh {
		t.Fatal(r, err)
	}
	if err = s.Complete("send-1", 200, []byte(`{"success":true}`)); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Claim("uncertain", "hash"); err != nil {
		t.Fatal(err)
	}
	s.Close()
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	r, err = s.Claim("send-1", "hash")
	if err != nil || r.Fresh || r.Status != 200 || string(r.Body) != `{"success":true}` {
		t.Fatal(r, err)
	}
	r, err = s.Claim("uncertain", "hash")
	if err != nil || r.Fresh || r.State != "pending" {
		t.Fatal(r, err)
	}
	if _, err = s.Claim("send-1", "different"); err == nil {
		t.Fatal("accepted conflicting payload")
	}
}
func TestConcurrentClaimsHaveOneWinner(t *testing.T) {
	s, err := Open("file:" + filepath.Join(t.TempDir(), "ledger.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	var winners atomic.Int32
	var wg sync.WaitGroup
	for range 10 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r, err := s.Claim("one", "hash")
			if err != nil {
				t.Error(err)
			}
			if r.Fresh {
				winners.Add(1)
			}
		}()
	}
	wg.Wait()
	if winners.Load() != 1 {
		t.Fatalf("side effect would run %d times", winners.Load())
	}
}

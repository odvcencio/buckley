package model

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestClient_GetModelInfo_ConcurrentRefresh(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		generation := requests.Add(1)
		fmt.Fprintf(w, `{"data":[{"id":"test/model","name":"generation-%d","context_length":%d}]}`, generation, generation)
	}))
	t.Cleanup(server.Close)
	client := NewClient("test", server.URL)
	client.rateLimiter = nil
	t.Cleanup(func() { _ = client.Close() })
	if _, err := client.FetchCatalog(); err != nil {
		t.Fatal(err)
	}

	stop := make(chan struct{})
	var readers sync.WaitGroup
	var ready sync.WaitGroup
	var lookups atomic.Int64
	for range 4 {
		readers.Add(1)
		ready.Add(1)
		go func() {
			defer readers.Done()
			ready.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				info, err := client.GetModelInfo("test/model")
				if err != nil {
					t.Error(err)
					return
				}
				if info.Name != fmt.Sprintf("generation-%d", info.ContextLength) {
					t.Errorf("inconsistent model snapshot: %+v", info)
					return
				}
				lookups.Add(1)
			}
		}()
	}
	defer readers.Wait()
	defer close(stop)
	ready.Wait()
	for range 32 {
		if _, err := client.RefreshCatalog(); err != nil {
			t.Fatal(err)
		}
	}
	if lookups.Load() == 0 || requests.Load() != 33 {
		t.Fatalf("lookups=%d requests=%d", lookups.Load(), requests.Load())
	}
	t.Logf("refreshes=32 readers=4 successful_lookups=%d", lookups.Load())
}

func TestClient_FetchCatalog_WarmCacheDuringRefresh(t *testing.T) {
	refreshStarted := make(chan struct{})
	release := make(chan struct{})
	releaseRefresh := sync.OnceFunc(func() { close(release) })
	defer releaseRefresh()
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		generation := requests.Add(1)
		if generation == 2 {
			close(refreshStarted)
			<-release
		}
		fmt.Fprintf(w, `{"data":[{"id":"test/model","name":"generation-%d"}]}`, generation)
	}))
	t.Cleanup(server.Close)
	client := NewClient("test", server.URL)
	client.rateLimiter = nil
	t.Cleanup(func() { _ = client.Close() })
	first, err := client.FetchCatalog()
	if err != nil {
		t.Fatal(err)
	}
	refreshed := make(chan error, 1)
	go func() {
		_, err := client.RefreshCatalog()
		refreshed <- err
	}()
	select {
	case <-refreshStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("refresh did not start")
	}

	cached := make(chan error, 1)
	go func() {
		catalog, err := client.FetchCatalog()
		if err == nil && catalog != first {
			err = fmt.Errorf("cached snapshot changed before refresh finished")
		}
		if err == nil {
			info, lookupErr := client.GetModelInfo("test/model")
			err = lookupErr
			if err == nil && info.Name != "generation-1" {
				err = fmt.Errorf("lookup did not use the existing snapshot: %+v", info)
			}
		}
		cached <- err
	}()
	select {
	case err := <-cached:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("cached access waited for the blocked network refresh")
	}
	if requests.Load() != 2 {
		t.Fatalf("cached access issued another request: %d", requests.Load())
	}
	releaseRefresh()
	if err := <-refreshed; err != nil {
		t.Fatal(err)
	}
	info, err := client.GetModelInfo("test/model")
	if err != nil || info.Name != "generation-2" || first.Data[0].Name != "generation-1" {
		t.Fatalf("refresh did not replace the snapshot intact: old=%+v new=%+v err=%v", first, info, err)
	}
	t.Log("cached fetch and lookup completed before network release; old snapshot retained; new snapshot published")
}

func TestClient_GetModelInfo_ConcurrentColdFetch(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		fmt.Fprint(w, `{"data":[{"id":"test/model","name":"Test Model"}]}`)
	}))
	t.Cleanup(server.Close)
	client := NewClient("test", server.URL)
	t.Cleanup(func() { _ = client.Close() })
	start := make(chan struct{})
	var readers sync.WaitGroup
	for range 16 {
		readers.Add(1)
		go func() {
			defer readers.Done()
			<-start
			info, err := client.GetModelInfo("test/model")
			if err != nil || info.ID != "test/model" {
				t.Errorf("cold lookup: info=%+v err=%v", info, err)
			}
		}()
	}
	close(start)
	readers.Wait()
	if requests.Load() != 1 {
		t.Fatalf("concurrent cold lookups fetched %d times, want 1", requests.Load())
	}
	t.Log("cold_lookups=16 requests=1")
}

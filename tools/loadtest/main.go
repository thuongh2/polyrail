// Command loadtest sends N chat requests at concurrency C and prints throughput and latency.
//
//	go run ./tools/loadtest <url>/v1/chat/completions <api-key> [model]
package main

import (
	"bytes"
	"fmt"
	"net/http"
	"os"
	"sort"
	"sync"
	"time"
)

func main() {
	if len(os.Args) < 3 {
		fmt.Fprintln(os.Stderr, "usage: loadtest <url> <api-key> [model]")
		os.Exit(2)
	}
	url, key, model := os.Args[1], os.Args[2], "mock/fast"
	if len(os.Args) > 3 {
		model = os.Args[3]
	}
	n, c := 5000, 50
	body := []byte(`{"model":"` + model + `","messages":[{"role":"user","content":"ping"}]}`)
	var mu sync.Mutex
	var lat []time.Duration
	errs := 0
	jobs := make(chan int)
	var wg sync.WaitGroup
	start := time.Now()
	for i := 0; i < c; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range jobs {
				t := time.Now()
				req, _ := http.NewRequest("POST", url, bytes.NewReader(body))
				req.Header.Set("Authorization", "Bearer "+key)
				resp, err := http.DefaultClient.Do(req)
				d := time.Since(t)
				mu.Lock()
				if err != nil || resp.StatusCode != 200 {
					errs++
				} else {
					lat = append(lat, d)
				}
				mu.Unlock()
				if resp != nil {
					resp.Body.Close()
				}
			}
		}()
	}
	for i := 0; i < n; i++ {
		jobs <- i
	}
	close(jobs)
	wg.Wait()
	el := time.Since(start)
	if len(lat) == 0 {
		fmt.Printf("requests=%d errors=%d (no successful requests)\n", n, errs)
		return
	}
	sort.Slice(lat, func(i, j int) bool { return lat[i] < lat[j] })
	p := func(q float64) time.Duration { return lat[int(float64(len(lat)-1)*q)] }
	fmt.Printf("requests=%d errors=%d rps=%.0f p50=%v p95=%v p99=%v\n", n, errs, float64(n)/el.Seconds(), p(.5), p(.95), p(.99))
}

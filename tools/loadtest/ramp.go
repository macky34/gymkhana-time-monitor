package main

import (
	"context"
	"fmt"
	"math/rand"
	"sort"
	"sync"
	"time"
)

// stage は runRamp の1段階を表す。target 人のワーカーを duration の間維持する。
type stage struct {
	target   int
	duration time.Duration
}

type sample struct {
	latency time.Duration
	ok      bool
}

// runRamp は stages に従ってワーカー数を段階的に増減させながら factory(id)
// が返す作業関数を繰り返し呼び出し、その結果を集める。k6のramping-vus
// executorのGo版に相当する。各ワーカーIDは常に同じgoroutineが専有するため、
// factory が返すクロージャは排他制御なしに状態(Cookie, 登録済みフラグ等)を
// 持ってよい。
func runRamp(stages []stage, factory func(workerID int) func() (time.Duration, bool)) []sample {
	var mu sync.Mutex
	var samples []sample
	var wg sync.WaitGroup
	var cancels []context.CancelFunc
	nextID := 0

	spawn := func() {
		ctx, cancel := context.WithCancel(context.Background())
		cancels = append(cancels, cancel)
		id := nextID
		nextID++
		wg.Add(1)
		go func() {
			defer wg.Done()
			work := factory(id)
			for {
				select {
				case <-ctx.Done():
					return
				default:
				}
				lat, ok := work()
				mu.Lock()
				samples = append(samples, sample{lat, ok})
				mu.Unlock()
				select {
				case <-ctx.Done():
					return
				case <-time.After(time.Duration(150+rand.Intn(350)) * time.Millisecond):
				}
			}
		}()
	}

	for _, st := range stages {
		for len(cancels) < st.target {
			spawn()
		}
		for len(cancels) > st.target {
			cancels[len(cancels)-1]()
			cancels = cancels[:len(cancels)-1]
		}
		fmt.Printf("[stage] workers=%d duration=%s\n", st.target, st.duration)
		time.Sleep(st.duration)
	}

	for _, cancel := range cancels {
		cancel()
	}
	wg.Wait()
	return samples
}

func percentile(sorted []time.Duration, p int) time.Duration {
	if len(sorted) == 0 {
		return 0
	}
	idx := p * len(sorted) / 100
	if idx >= len(sorted) {
		idx = len(sorted) - 1
	}
	return sorted[idx]
}

func summarize(name string, samples []sample) {
	fmt.Printf("=== %s ===\n", name)
	if len(samples) == 0 {
		fmt.Println("サンプルなし")
		return
	}
	var latencies []time.Duration
	failed := 0
	for _, s := range samples {
		if s.ok {
			latencies = append(latencies, s.latency)
		} else {
			failed++
		}
	}
	fmt.Printf("total=%d success=%d failed=%d error_rate=%.2f%%\n",
		len(samples), len(latencies), failed, 100*float64(failed)/float64(len(samples)))
	if len(latencies) > 0 {
		sort.Slice(latencies, func(i, j int) bool { return latencies[i] < latencies[j] })
		fmt.Printf("latency: p50=%s p95=%s p99=%s max=%s\n",
			percentile(latencies, 50), percentile(latencies, 95), percentile(latencies, 99), latencies[len(latencies)-1])
	}
}

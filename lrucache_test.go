package lrucache

import (
	"errors"
	"math/rand"
	"runtime"
	"strconv"
	"sync"
	"testing"
)

type varsize int

func (i varsize) Size() int64 {
	return int64(i)
}

type purgeable struct {
	purged bool
	why    PurgeReason
}

func (x *purgeable) Size() int64 {
	return 1
}

func (x *purgeable) OnPurge(why PurgeReason) {
	x.purged = true
	x.why = why
}

func syncCache(c *Cache) {
	c.Get("imblueifIweregreenIwoulddie")
}

func TestOnPurge_1(t *testing.T) {
	c := New(1)
	defer c.Close()
	var x, y purgeable
	c.Set("x", &x)
	c.Set("y", &y)
	syncCache(c)
	if !x.purged {
		t.Error("Element was not purged from full cache")
	}
	if x.why != CACHEFULL {
		t.Error("Element should have been purged but was deleted")
	}
}

func TestOnPurge_2(t *testing.T) {
	c := New(1)
	defer c.Close()
	var x purgeable
	c.Set("x", &x)
	c.Delete("x")
	syncCache(c)
	if !x.purged {
		t.Error("Element was not deleted from cache")
	}
	if x.why != EXPLICITDELETE {
		t.Error("Element should have been deleted but was purged")
	}
}

// Just test filling a cache with a type that does not implement NotifyPurge
func TestsafeOnPurge(t *testing.T) {
	c := New(1)
	defer c.Close()
	i := varsize(1)
	j := varsize(1)
	c.Set("i", i)
	c.Set("j", j)
	syncCache(c)
}

func TestSize(t *testing.T) {
	c := New(100)
	defer c.Close()
	// sum(0..14) = 105
	for i := 1; i < 15; i++ {
		c.Set(strconv.Itoa(i), varsize(i))
	}
	syncCache(c)
	// At this point, expect {0, 1, 2, 3} to be purged
	if c.Size() != 99 {
		t.Errorf("Unexpected size: %d", c.Size())
	}
	for i := 0; i < 4; i++ {
		if _, err := c.Get(strconv.Itoa(i)); err != ErrNotFound {
			t.Errorf("Expected %d to be purged", i)
		}
	}
	for i := 4; i < 15; i++ {
		if _, err := c.Get(strconv.Itoa(i)); err != nil {
			t.Errorf("Expected %d to be cached", i)
		}
	}
}

func TestOnMiss(t *testing.T) {
	c := New(10)
	defer c.Close()
	// Expected cache misses (arbitrary value)
	misses := map[string]int{}
	for i := 5; i < 10; i++ {
		misses[strconv.Itoa(i)] = 0
	}
	c.OnMiss(func(id string) (Cacheable, error) {
		if _, ok := misses[id]; !ok {
			return nil, nil
		}
		delete(misses, id)
		i, err := strconv.Atoi(id)
		if err != nil {
			return nil, errors.New("Illegal id: " + id)
		}
		return i, nil
	})
	for i := 0; i < 5; i++ {
		c.Set(strconv.Itoa(i), i)
	}
	for i := 0; i < 10; i++ {
		x, err := c.Get(strconv.Itoa(i))
		switch err {
		case nil:
			break
		case ErrNotFound:
			t.Errorf("Unexpected cache miss for %d", i)
			continue
		default:
			t.Fatal(err)
		}
		if j := x.(int); j != i {
			t.Errorf("Illegal cache value: expected %d, got %d", i, j)
		}
	}
	for k := range misses {
		t.Errorf("Expected %s to miss", k)
	}
}

func TestConcurrentOnMiss(t *testing.T) {
	c := New(10)
	defer c.Close()
	ch := make(chan int)
	// If key foo is requested but not cached, read it from the channel
	c.OnMiss(func(id string) (Cacheable, error) {
		if id == "foo" {
			// Indicate that we want a value
			ch <- 0
			// To be perfectly honest: I do not understand why this scheduler
			// call is necessary. Channel operations are not enough, here? If
			// this Gosched() is left out, a deadlock occurs. Why? What is the
			// idiomatic way to do this?
			runtime.Gosched()
			return <-ch, nil
		}
		return nil, nil
	})
	go func() {
		c.Get("foo")
	}()
	<-ch
	// Now we know for sure: a goroutine is blocking on c.Get("foo").
	// But other cache operations should be unaffected:
	c.Set("bar", 10)
	// Unlock that poor blocked goroutine
	ch <- 10
	result, err := c.Get("foo")
	switch {
	case err != nil:
		t.Error(`Error while fetching "foo":`, err)
	case result != 10:
		t.Error("Expected 10, got:", result)
	}
}

func TestZeroSize(t *testing.T) {
	c := New(2)
	defer c.Close()
	c.Set("a", varsize(0))
	c.Set("b", varsize(1))
	c.Set("c", varsize(2))
	if _, err := c.Get("a"); err != nil {
		t.Error("Purged element with size=0; should have left in cache")
	}
	c.Delete("a")
	c.Set("d", varsize(2))
	if _, err := c.Get("c"); err != ErrNotFound {
		t.Error("Kept `c' around for too long after removing empty element")
	}
	if _, err := c.Get("d"); err != nil {
		t.Error("Failed to cache `d' after removing empty element")
	}
}

func benchmarkGet(b *testing.B, conc int) {
	b.StopTimer()
	// Size doesn't matter (that's what she said)
	c := New(1000)
	defer c.Close()
	c.Set("x", 1)
	syncCache(c)
	var wg sync.WaitGroup
	wg.Add(conc)
	b.StartTimer()
	for i := 0; i < conc; i++ {
		go func() {
			for i := 0; i < b.N/conc; i++ {
				c.Get("x")
			}
			syncCache(c)
			wg.Done()
		}()
	}
	wg.Wait()
}

func benchmarkSet(b *testing.B, conc int) {
	b.StopTimer()
	// Size matters.
	c := New(int64(b.N) / 4)
	defer c.Close()
	syncCache(c)
	var wg sync.WaitGroup
	wg.Add(conc)
	b.StartTimer()
	for i := 0; i < conc; i++ {
		go func() {
			for i := 0; i < b.N/conc; i++ {
				c.Set(strconv.Itoa(i), i)
			}
			wg.Done()
		}()
	}
	wg.Wait()
	syncCache(c)
}

func benchmarkAll(b *testing.B, conc int) {
	b.StopTimer()
	// Size is definitely important, but what is the right size?
	c := New(int64(b.N) / 4)
	defer c.Close()
	syncCache(c)
	var wg sync.WaitGroup
	wg.Add(conc)
	b.StartTimer()
	for i := 0; i < conc; i++ {
		go func() {
			for i := 0; i < b.N/3/conc; i++ {
				c.Set(strconv.Itoa(rand.Int()), 1)
				c.Get(strconv.Itoa(rand.Int()))
				c.Delete(strconv.Itoa(rand.Int()))
			}
			wg.Done()
		}()
	}
	wg.Wait()
	syncCache(c)
}

func BenchmarkGet(b *testing.B) {
	benchmarkGet(b, 1)
}

func Benchmark5ConcurrentGet(b *testing.B) {
	benchmarkGet(b, 5)
}

func BenchmarkSet(b *testing.B) {
	benchmarkSet(b, 1)
}

func Benchmark5ConcurrentSet(b *testing.B) {
	benchmarkSet(b, 5)
}

func BenchmarkAll(b *testing.B) {
	benchmarkAll(b, 1)
}

func Benchmark5ConcurrentAll(b *testing.B) {
	benchmarkAll(b, 5)
}

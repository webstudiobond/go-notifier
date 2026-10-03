package ratelimit

import (
	"testing"
	"time"
)

func TestLimiter_Disabled(t *testing.T) {
	lim := NewLimiter(0, 0)
	for range 20 {
		if !lim.Allow() {
			t.Errorf("expected disabled limiter to allow all requests")
		}
	}
}

func TestLimiter_BurstExhaustion(t *testing.T) {
	burst := 3
	rate := 60
	lim := NewLimiter(rate, burst)

	for range burst {
		if !lim.Allow() {
			t.Errorf("expected initial burst requests to be allowed")
		}
	}

	if lim.Allow() {
		t.Errorf("expected request exceeding burst to be denied")
	}

	time.Sleep(1100 * time.Millisecond)

	if !lim.Allow() {
		t.Errorf("expected request after refill window to be allowed")
	}
}

func TestLimiter_DefaultBurst(t *testing.T) {
	tests := []struct {
		name    string
		wantCap float64
		burst   int
		rate    int
	}{
		{"zero_burst", 10.0, 0, 10},
		{"negative_burst", 15.0, -5, 15},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			lim := NewLimiter(tt.rate, tt.burst)
			if lim.capacity != tt.wantCap {
				t.Fatalf("expected capacity %v, got %v", tt.wantCap, lim.capacity)
			}
			if !lim.Allow() {
				t.Fatal("expected request to be allowed")
			}
		})
	}
}

func TestLimiter_CapacityClamping(t *testing.T) {
	lim := NewLimiter(600, 2)
	time.Sleep(10 * time.Millisecond)
	if !lim.Allow() {
		t.Fatal("expected request to be allowed")
	}
	if lim.tokens > lim.capacity {
		t.Fatalf("tokens %v exceeded capacity %v", lim.tokens, lim.capacity)
	}
}

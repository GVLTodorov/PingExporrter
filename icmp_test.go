package main

import "testing"

func TestMakePayload(t *testing.T) {
	p := makePayload(120)
	if len(p) != 120 {
		t.Fatalf("len(payload) = %d, want 120", len(p))
	}
	for i, b := range p[:10] {
		if b != byte(i) {
			t.Errorf("payload[%d] = %d, want %d", i, b, i)
		}
	}
}

func TestMakePayloadZeroAndNegative(t *testing.T) {
	if p := makePayload(0); len(p) != 0 {
		t.Errorf("len(makePayload(0)) = %d, want 0", len(p))
	}
	if p := makePayload(-5); len(p) != 0 {
		t.Errorf("len(makePayload(-5)) = %d, want 0", len(p))
	}
}

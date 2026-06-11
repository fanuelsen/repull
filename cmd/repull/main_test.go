package main

import (
	"testing"
	"time"

	// Embed the timezone database so DST tests work on systems without
	// /usr/share/zoneinfo (e.g. the alpine-based CI image).
	_ "time/tzdata"
)

func TestParseScheduleTime(t *testing.T) {
	tests := []struct {
		name     string
		schedule string
		wantErr  bool
		hour     int
		minute   int
	}{
		{name: "valid time", schedule: "23:00", hour: 23, minute: 0},
		{name: "valid morning time", schedule: "06:30", hour: 6, minute: 30},
		{name: "midnight", schedule: "00:00", hour: 0, minute: 0},
		{name: "missing colon", schedule: "2300", wantErr: true},
		{name: "too many parts", schedule: "23:00:00", wantErr: true},
		{name: "hour out of range", schedule: "24:00", wantErr: true},
		{name: "minute out of range", schedule: "23:60", wantErr: true},
		{name: "negative hour", schedule: "-1:00", wantErr: true},
		{name: "not numbers", schedule: "ab:cd", wantErr: true},
		{name: "empty", schedule: "", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseScheduleTime(tt.schedule)
			if (err != nil) != tt.wantErr {
				t.Fatalf("parseScheduleTime(%q) error = %v, wantErr %v", tt.schedule, err, tt.wantErr)
			}
			if err == nil && (got.Hour() != tt.hour || got.Minute() != tt.minute) {
				t.Errorf("parseScheduleTime(%q) = %02d:%02d, want %02d:%02d", tt.schedule, got.Hour(), got.Minute(), tt.hour, tt.minute)
			}
		})
	}
}

func TestNextOccurrence(t *testing.T) {
	oslo, err := time.LoadLocation("Europe/Oslo")
	if err != nil {
		t.Fatalf("failed to load timezone: %v", err)
	}

	target := func(hour, minute int) time.Time {
		return time.Date(2026, time.June, 11, hour, minute, 0, 0, oslo)
	}

	tests := []struct {
		name   string
		target time.Time
		now    time.Time
		want   time.Time
	}{
		{
			name:   "target later today",
			target: target(23, 0),
			now:    time.Date(2026, time.June, 11, 10, 0, 0, 0, oslo),
			want:   time.Date(2026, time.June, 11, 23, 0, 0, 0, oslo),
		},
		{
			name:   "target already passed today",
			target: target(10, 0),
			now:    time.Date(2026, time.June, 11, 15, 0, 0, 0, oslo),
			want:   time.Date(2026, time.June, 12, 10, 0, 0, 0, oslo),
		},
		{
			name:   "now exactly at target schedules tomorrow",
			target: target(10, 0),
			now:    time.Date(2026, time.June, 11, 10, 0, 0, 0, oslo),
			want:   time.Date(2026, time.June, 12, 10, 0, 0, 0, oslo),
		},
		{
			name:   "month rollover",
			target: target(10, 0),
			now:    time.Date(2026, time.June, 30, 15, 0, 0, 0, oslo),
			want:   time.Date(2026, time.July, 1, 10, 0, 0, 0, oslo),
		},
		{
			// DST starts 2026-03-29 02:00 in Oslo (23-hour day).
			// Add(24h) would land on March 30 00:00 — wrong day and time.
			name:   "spring forward keeps wall-clock time",
			target: target(23, 0),
			now:    time.Date(2026, time.March, 28, 23, 30, 0, 0, oslo),
			want:   time.Date(2026, time.March, 29, 23, 0, 0, 0, oslo),
		},
		{
			// DST ends 2026-10-25 03:00 in Oslo (25-hour day).
			// Add(24h) would land on October 25 22:00 — an hour early.
			name:   "fall back keeps wall-clock time",
			target: target(23, 0),
			now:    time.Date(2026, time.October, 24, 23, 30, 0, 0, oslo),
			want:   time.Date(2026, time.October, 25, 23, 0, 0, 0, oslo),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := nextOccurrence(tt.target, tt.now)
			if !got.Equal(tt.want) {
				t.Errorf("nextOccurrence() = %s, want %s", got, tt.want)
			}
		})
	}
}

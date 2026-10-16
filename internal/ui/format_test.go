package ui

import (
	"testing"
	"time"
)

func TestMoney(t *testing.T) {
	tests := []struct {
		amount   float64
		currency string
		want     string
	}{
		{1284.2, "USD", "$1,284.20"},
		{0, "USD", "$0.00"},
		{12, "USD", "$12.00"},
		{999.995, "USD", "$1,000.00"},
		{1234567.891, "USD", "$1,234,567.89"},
		{181.8, "EUR", "€181.80"},
		{42, "GBP", "£42.00"},
		{1284.2, "INR", "₹1,284.20"},
		{1284.2, "SEK", "SEK 1,284.20"},
		{1284.2, "sek", "SEK 1,284.20"},
		{1284.2, "", "1,284.20"},
		{-12, "USD", "−$12.00"},
		{-1284.2, "SEK", "−SEK 1,284.20"},
		{-0.004, "USD", "$0.00"},
		{100, "USD", "$100.00"},
		{1000, "USD", "$1,000.00"},
	}
	for _, tt := range tests {
		if got := Money(tt.amount, tt.currency); got != tt.want {
			t.Errorf("Money(%v, %q) = %q, want %q", tt.amount, tt.currency, got, tt.want)
		}
	}
}

func TestMoneyShort(t *testing.T) {
	tests := []struct {
		amount   float64
		currency string
		want     string
	}{
		{1284.2, "USD", "$1,284"},
		{1284.5, "USD", "$1,285"},
		{1284.49, "USD", "$1,284"},
		{0.4, "USD", "$0"},
		{999, "EUR", "€999"},
		{1500000, "SEK", "SEK 1,500,000"},
		{-12.6, "USD", "−$13"},
		{-0.4, "USD", "$0"},
	}
	for _, tt := range tests {
		if got := MoneyShort(tt.amount, tt.currency); got != tt.want {
			t.Errorf("MoneyShort(%v, %q) = %q, want %q", tt.amount, tt.currency, got, tt.want)
		}
	}
}

func TestClockUsesTheGivenZone(t *testing.T) {
	utc := time.Date(2026, 10, 15, 15, 30, 0, 0, time.UTC)
	cdt := time.FixedZone("CDT", -5*3600)
	if got := clock(utc, time.UTC); got != "15:30" {
		t.Errorf("UTC = %q", got)
	}
	if got := clock(utc, cdt); got != "10:30" {
		t.Errorf("CDT = %q", got)
	}
}

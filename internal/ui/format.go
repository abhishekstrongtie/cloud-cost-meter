// Package ui turns scheduler updates into the menu bar menu. menu.go builds a plain model of
// the menu (easy to test); tray.go puts that model on screen with systray.
package ui

import (
	"math"
	"strconv"
	"strings"
	"time"
)

var currencySymbols = map[string]string{"USD": "$", "EUR": "€", "GBP": "£", "INR": "₹"}

// Money formats an amount with cents for the menu: $1,284.20.
func Money(amount float64, currency string) string { return format(amount, currency, 2) }

// MoneyShort formats an amount in whole units for the menu bar title: $1,284.
func MoneyShort(amount float64, currency string) string { return format(amount, currency, 0) }

// format groups thousands, uses the currency's symbol when it has one (else its code, as in
// "SEK 1,284.20") and writes a negative amount, such as a credit, as −$12.00.
func format(amount float64, currency string, decimals int) string {
	scale := math.Pow10(decimals)
	rounded := math.Round(math.Abs(amount)*scale) / scale
	whole, frac, _ := strings.Cut(strconv.FormatFloat(rounded, 'f', decimals, 64), ".")
	number := groupThousands(whole)
	if decimals > 0 {
		number += "." + frac
	}

	code := strings.ToUpper(currency)
	var out string
	switch symbol, known := currencySymbols[code]; {
	case known:
		out = symbol + number
	case code != "":
		out = code + " " + number
	default:
		out = number
	}
	if amount < 0 && rounded != 0 { // never "−$0.00"
		out = "−" + out
	}
	return out
}

func groupThousands(digits string) string {
	if len(digits) <= 3 {
		return digits
	}
	var b strings.Builder
	first := len(digits) % 3
	if first > 0 {
		b.WriteString(digits[:first])
	}
	for i := first; i < len(digits); i += 3 {
		if b.Len() > 0 {
			b.WriteByte(',')
		}
		b.WriteString(digits[i : i+3])
	}
	return b.String()
}

// clock writes a time of day in the given zone as 15:04.
func clock(t time.Time, loc *time.Location) string { return t.In(loc).Format("15:04") }

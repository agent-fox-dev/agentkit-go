package main

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
)

// sample is the project the example indexes: a small Go shop with a Python
// report script and a note, so that a query has code, comments, two languages
// and prose to choose between.
var sample = map[string]string{
	"go.mod": "module example.com/shop\n\ngo 1.22\n",
	"cart/cart.go": `// Package cart holds what a customer is about to buy.
package cart

// Item is one line of a cart.
type Item struct {
	SKU   string
	Cents int
}

// Cart is a customer's basket.
type Cart struct {
	items []Item
}

// New returns an empty cart.
func New() *Cart { return &Cart{} }

// Add puts an item in the cart.
func (c *Cart) Add(it Item) { c.items = append(c.items, it) }

// Total is the price of the cart in cents.
func (c *Cart) Total() int {
	total := 0
	for _, it := range c.items {
		total += it.Cents
	}
	return total
}

// Empty reports whether nothing has been added.
func (c *Cart) Empty() bool { return len(c.items) == 0 }
`,
	"cart/cart_test.go": `package cart

import "testing"

func TestTotal(t *testing.T) {
	c := New()
	c.Add(Item{SKU: "pear", Cents: 120})
	c.Add(Item{SKU: "fig", Cents: 80})
	if got := c.Total(); got != 200 {
		t.Fatalf("Total() = %d, want 200", got)
	}
}
`,
	"checkout/checkout.go": `// Package checkout turns a cart into a paid receipt.
package checkout

import (
	"errors"

	"example.com/shop/cart"
)

// ErrEmptyCart is returned when there is nothing to pay for.
var ErrEmptyCart = errors.New("checkout: empty cart")

// Payer charges a card.
type Payer interface {
	Pay(cents int) (receiptID string, err error)
}

// Receipt proves a checkout.
type Receipt struct {
	ID    string
	Cents int
}

// Checkout charges the cart's total once. A payment that times out is
// retried by the payer, never here, so a customer is not charged twice.
func Checkout(c *cart.Cart, p Payer) (Receipt, error) {
	if c.Empty() {
		return Receipt{}, ErrEmptyCart
	}
	id, err := p.Pay(c.Total())
	if err != nil {
		return Receipt{}, err
	}
	return Receipt{ID: id, Cents: c.Total()}, nil
}
`,
	"payments/stripe.go": `// Package payments holds the payers checkout can use.
package payments

import (
	"errors"
	"time"
)

// ErrTimeout is returned when the card network does not answer.
var ErrTimeout = errors.New("payments: timeout")

// Stripe charges cards through Stripe.
type Stripe struct {
	Attempts int
	Wait     time.Duration
}

// Pay charges cents and retries a timeout with a growing wait.
func (s Stripe) Pay(cents int) (string, error) {
	var err error
	for attempt := 0; attempt < s.Attempts; attempt++ {
		var id string
		if id, err = s.charge(cents); err == nil {
			return id, nil
		}
		if !errors.Is(err, ErrTimeout) {
			return "", err
		}
		time.Sleep(s.Wait << attempt) // retry with backoff
	}
	return "", err
}

func (s Stripe) charge(cents int) (string, error) { return "ch_test", nil }
`,
	"scripts/report.py": `"""Weekly order report."""
import csv


def load_orders(path):
    """Read the orders from a CSV file."""
    with open(path) as f:
        return list(csv.DictReader(f))


class Report:
    """Totals of the orders, rendered for a person."""

    def __init__(self, orders):
        self.orders = orders

    def render(self):
        total = sum(int(o["cents"]) for o in self.orders)
        return f"{len(self.orders)} orders, {total} cents"
`,
	".gitignore":       "build/\n",
	"build/output.txt": "SecretBuildOutput is not indexed: .gitignore lists build/\n",
	"docs/checkout.md": "# Checkout\n\nA checkout charges the cart once. If the card network times out, the payer\nretries; see `payments.Stripe`. Never retry in `Checkout` itself.\n\nThe weekly report reads the orders with `load_orders`.\n",

	// Four files the index cannot search, one for each reason it reports:
	// a NUL byte, fewer than three bytes, more than a megabyte, and more
	// distinct trigrams than zoekt keeps per file (see writeGenerated).
	"assets/logo.png": "\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR",
	"data/flag.txt":   "x",
}

// genFiles is how many small generated files writeGenerated adds. They give a
// query many files to match, and the index enough files for a few edits to
// stay under its rebuild threshold.
const genFiles = 60

// writeSample writes sample and the generated files under dir.
func writeSample(dir string) error {
	for rel, content := range sample {
		if err := writeFile(dir, rel, []byte(content)); err != nil {
			return err
		}
	}
	for i := 1; i <= genFiles; i++ {
		src := fmt.Sprintf("package gen\n\n// Gen%02d is generated.\nfunc Gen%02d() int { return %d }\n", i, i, i)
		if err := writeFile(dir, fmt.Sprintf("gen/gen_%02d.go", i), []byte(src)); err != nil {
			return err
		}
	}
	// A bundle on one line, 60 000 characters from a 64-letter alphabet:
	// about as many distinct trigrams as a minified script has.
	var bundle bytes.Buffer
	for x, i := uint32(7), 0; i < 60000; i++ {
		x = x*1664525 + 1013904223
		bundle.WriteByte("abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789_$"[x>>26])
	}
	if err := writeFile(dir, "web/app.min.js", bundle.Bytes()); err != nil {
		return err
	}
	// An export over the 1 MiB a file may be.
	return writeFile(dir, "data/orders.csv", bytes.Repeat([]byte("1001,widget,1999\n"), 70000))
}

func writeFile(dir, rel string, content []byte) error {
	abs := filepath.Join(dir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		return err
	}
	return os.WriteFile(abs, content, 0o644)
}

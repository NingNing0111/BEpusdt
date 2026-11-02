package model

import (
	"os"
	"regexp"
)

// Credentials stay in the server environment, outside public configuration APIs.
func BinanceUID() string            { return os.Getenv("BINANCE_UID") }
func ValidBinanceUID(s string) bool { return regexp.MustCompile(`^[1-9][0-9]{3,19}$`).MatchString(s) }
func BinanceConfigured() bool {
	return ValidBinanceUID(BinanceUID()) && os.Getenv("BINANCE_API_KEY") != "" && os.Getenv("BINANCE_API_SECRET") != ""
}

// A persistent unique receipt prevents replay across restarts and concurrent workers.
type BinanceReceipt struct {
	TransactionID string `gorm:"primaryKey;size:128"`
	OrderID       int64  `gorm:"uniqueIndex;not null"`
}

func (BinanceReceipt) TableName() string { return "bep_binance_receipt" }

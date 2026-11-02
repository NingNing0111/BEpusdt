package model

import (
	"os"
	"regexp"
	"sync/atomic"
)

const (
	BinanceUIDKey    ConfKey = "binance_uid"
	BinanceAPIKey    ConfKey = "binance_api_key"
	BinanceAPISecret ConfKey = "binance_api_secret"
)

type BinanceCredentials struct{ UID, APIKey, APISecret, Source string }

var binanceCredentials atomic.Pointer[BinanceCredentials]

// Publish one immutable snapshot so pollers never mix credentials across a save.
func refreshBinanceCredentials(rows []Conf) {
	c := BinanceCredentials{Source: "environment"}
	values := map[ConfKey]string{}
	for _, row := range rows {
		values[row.K] = row.V
	}
	if _, ok := values[BinanceUIDKey]; ok {
		c = BinanceCredentials{values[BinanceUIDKey], values[BinanceAPIKey], values[BinanceAPISecret], "database"}
	}
	binanceCredentials.Store(&c)
}

func GetBinanceCredentials() BinanceCredentials {
	c := binanceCredentials.Load()
	if c != nil && c.Source == "database" {
		return *c
	}
	return BinanceCredentials{os.Getenv("BINANCE_UID"), os.Getenv("BINANCE_API_KEY"), os.Getenv("BINANCE_API_SECRET"), "environment"}
}
func BinanceUID() string            { return GetBinanceCredentials().UID }
func ValidBinanceUID(s string) bool { return regexp.MustCompile(`^[1-9][0-9]{3,19}$`).MatchString(s) }
func BinanceConfigured() bool {
	c := GetBinanceCredentials()
	return ValidBinanceUID(c.UID) && c.APIKey != "" && c.APISecret != ""
}
func IsBinanceConfKey(k ConfKey) bool {
	return k == BinanceUIDKey || k == BinanceAPIKey || k == BinanceAPISecret
}
func IsBinanceSecret(k ConfKey) bool { return k == BinanceAPIKey || k == BinanceAPISecret }

// A persistent unique receipt prevents replay across restarts and concurrent workers.
type BinanceReceipt struct {
	TransactionID string `gorm:"primaryKey;size:128"`
	OrderID       int64  `gorm:"uniqueIndex;not null"`
}

func (BinanceReceipt) TableName() string { return "bep_binance_receipt" }

package model

// BinanceReceipt permanently prevents reuse even after orders are deleted.
type BinanceReceipt struct {
	TransactionID string `gorm:"primaryKey;size:128"`
	OrderID       int64  `gorm:"uniqueIndex;not null"`
}

func (BinanceReceipt) TableName() string { return "bep_binance_receipt" }

package task

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"time"

	"github.com/shopspring/decimal"
	"github.com/v03413/bepusdt/app/log"
	"github.com/v03413/bepusdt/app/model"
	"gorm.io/gorm"
)

type binanceTrade struct {
	OrderType string `json:"orderType"`
	ID        string `json:"transactionId"`
	Time      int64  `json:"transactionTime"`
	Amount    string `json:"amount"`
	Currency  string `json:"currency"`
	Receiver  struct {
		UID string `json:"binanceId"`
	} `json:"receiverInfo"`
}

type binanceClient struct {
	client                *http.Client
	endpoint, key, secret string
}

func (c binanceClient) history(ctx context.Context, start, end int64) ([]binanceTrade, error) {
	q := url.Values{"startTime": {strconv.FormatInt(start, 10)}, "endTime": {strconv.FormatInt(end, 10)}, "limit": {"100"}, "recvWindow": {"5000"}, "timestamp": {strconv.FormatInt(time.Now().UnixMilli(), 10)}}
	mac := hmac.New(sha256.New, []byte(c.secret))
	mac.Write([]byte(q.Encode()))
	req, err := http.NewRequestWithContext(ctx, "GET", c.endpoint+"/sapi/v1/pay/transactions?"+q.Encode()+"&signature="+hex.EncodeToString(mac.Sum(nil)), nil)
	if err != nil {
		return nil, errors.New("invalid Binance request")
	}
	req.Header.Set("X-MBX-APIKEY", c.key)
	resp, err := c.client.Do(req)
	// Do not log transport errors containing signed URLs.
	if err != nil {
		return nil, errors.New("Binance API connection failed")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("Binance API HTTP %d", resp.StatusCode)
	}
	var result struct {
		Code    string         `json:"code"`
		Success bool           `json:"success"`
		Data    []binanceTrade `json:"data"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&result); err != nil {
		return nil, errors.New("invalid Binance response")
	}
	if !result.Success || result.Code != "000000" {
		return nil, errors.New("Binance API rejected history request")
	}
	return result.Data, nil
}

// Split saturated intervals instead of silently losing records at the 100-row limit.
func (c binanceClient) scan(ctx context.Context, start, end int64) ([]binanceTrade, error) {
	rows, err := c.history(ctx, start, end)
	if err != nil {
		return nil, err
	}
	if len(rows) < 100 {
		return rows, nil
	}
	if end <= start {
		return nil, errors.New("Binance history saturated within one millisecond; manual reconciliation required")
	}
	mid := start + (end-start)/2
	a, err := c.scan(ctx, start, mid)
	if err != nil {
		return nil, err
	}
	b, err := c.scan(ctx, mid+1, end)
	if err != nil {
		return nil, err
	}
	return append(a, b...), nil
}

func init() { Register(Task{Duration: time.Minute, Callback: binancePoll}) }

func binancePoll(ctx context.Context) {
	if !model.BinanceConfigured() {
		return
	}
	var orders []model.Order
	if err := model.Db.Where("trade_type = ? AND status in (?) AND expired_at > ?", model.Binance, []int{model.OrderStatusWaiting, model.OrderStatusExpired}, time.Now().Add(model.GetLookbackHour())).Order("created_at asc").Find(&orders).Error; err != nil || len(orders) == 0 {
		return
	}
	start := orders[0].CreatedAt.Time().UnixMilli()
	now := time.Now().UnixMilli()
	// Only order windows within Binance's supported query interval are scanned.
	if start < now-int64(90*24*time.Hour/time.Millisecond) {
		start = now - int64(90*24*time.Hour/time.Millisecond)
	}
	c := binanceClient{&http.Client{Timeout: 15 * time.Second}, "https://api.binance.com", os.Getenv("BINANCE_API_KEY"), os.Getenv("BINANCE_API_SECRET")}
	rows, err := c.scan(ctx, start, now)
	if err != nil {
		log.Task.Warn("Binance Pay: ", err)
		return
	}
	for _, row := range rows {
		for i, o := range orders {
			if !binanceMatches(o, row, model.BinanceUID()) {
				continue
			}
			paid, err := settleBinance(&o, row)
			if err != nil {
				log.Task.Warn("Binance Pay settlement failed")
			}
			if paid {
				orders[i] = o
				notifyOrderSuccess(o)
			}
			break
		}
	}
}

func binanceMatches(o model.Order, r binanceTrade, uid string) bool {
	amount, err := decimal.NewFromString(r.Amount)
	if err != nil || !amount.IsPositive() {
		return false
	}
	target, err := decimal.NewFromString(o.Amount)
	if err != nil {
		return false
	}
	at := time.UnixMilli(r.Time)
	return (o.Status == model.OrderStatusWaiting || o.Status == model.OrderStatusExpired) && o.TradeType == model.Binance && !o.AddressLocked && o.Address == uid && r.Receiver.UID == uid && r.ID != "" && (r.OrderType == "C2C" || r.OrderType == "PAY") && r.Currency == "USDT" && amount.Equal(target) && o.CreatedAt != nil && !at.Before(o.CreatedAt.Time()) && at.Before(o.ExpiredAt)
}

func settleBinance(o *model.Order, r binanceTrade) (bool, error) {
	err := model.Db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(&model.BinanceReceipt{TransactionID: r.ID, OrderID: o.ID}).Error; err != nil {
			return err
		}
		at := time.UnixMilli(r.Time)
		result := tx.Model(&model.Order{}).Where("id = ? AND trade_type = ? AND status in (?) AND address = ? AND amount = ? AND created_at <= ? AND expired_at > ? AND address_locked = ?", o.ID, model.Binance, []int{model.OrderStatusWaiting, model.OrderStatusExpired}, o.Address, o.Amount, at, at, false).Updates(map[string]any{"status": model.OrderStatusSuccess, "ref_hash": "binance:" + r.ID, "confirmed_at": at})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return errors.New("order no longer payable")
		}
		return nil
	})
	if err != nil {
		return false, err
	}
	// Reload the committed state for existing callback and notification handlers.
	if err := model.Db.First(o, o.ID).Error; err != nil {
		return false, err
	}
	return true, nil
}

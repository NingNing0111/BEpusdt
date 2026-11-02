package admin

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/v03413/bepusdt/app/model"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func TestBinanceConfigurationLifecycle(t *testing.T) {
	t.Setenv("BINANCE_UID", "12345678")
	t.Setenv("BINANCE_API_KEY", "environment-key")
	t.Setenv("BINANCE_API_SECRET", "environment-secret")
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "config.db")), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	if err = db.AutoMigrate(&model.Conf{}); err != nil {
		t.Fatal(err)
	}
	model.Db = db
	t.Cleanup(func() { sqlDB, _ := db.DB(); sqlDB.Close() })
	model.RefreshC()
	router := gin.New()
	h := Conf{}
	router.GET("/binance", h.BinanceGet)
	router.POST("/binance", h.BinanceSet)
	router.POST("/get", h.Get)
	router.POST("/gets", h.Gets)
	router.POST("/set", h.Set)
	router.POST("/sets", h.Sets)
	router.POST("/del", h.Del)
	request := func(method, path, body string) map[string]any {
		t.Helper()
		w := httptest.NewRecorder()
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		router.ServeHTTP(w, r)
		// No config response can disclose saved or environment credentials.
		for _, secret := range []string{"environment-key", "environment-secret", "saved-key", "saved-secret"} {
			if strings.Contains(w.Body.String(), secret) {
				t.Fatalf("credential leaked via %s", path)
			}
		}
		var result map[string]any
		if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		return result
	}
	initial := request(http.MethodGet, "/binance", "")["data"].(map[string]any)
	if initial["source"] != "environment" || initial["enabled"] != true {
		t.Fatal(initial)
	}
	if r := request(http.MethodPost, "/binance", `{"uid":"87654321","api_key":"saved-key","api_secret":"saved-secret"}`); r["code"] != float64(200) {
		t.Fatal(r)
	}
	c := model.GetBinanceCredentials()
	if c.UID != "87654321" || c.APIKey != "saved-key" || c.APISecret != "saved-secret" || c.Source != "database" {
		t.Fatal("saved config not active")
	}
	request(http.MethodPost, "/binance", `{"uid":"87654321","api_key":"","api_secret":""}`)
	if model.GetBinanceCredentials().APISecret != "saved-secret" {
		t.Fatal("blank secret replaced stored value")
	}
	request(http.MethodPost, "/get", `{"key":"binance_api_secret"}`)
	request(http.MethodPost, "/gets", `{"keys":["binance_api_key","binance_api_secret"]}`)
	for _, p := range []struct{ path, body string }{
		{"/set", `{"key":"binance_api_secret","value":"bypass"}`},
		{"/sets", `[{"key":"binance_uid","value":"11111111"}]`},
		{"/del", `{"key":"binance_uid"}`},
		{"/binance", `{"uid":"not-a-uid","api_key":"changed"}`},
	} {
		if r := request(http.MethodPost, p.path, p.body); r["code"] != float64(400) {
			t.Fatal("invalid update accepted", p.path)
		}
	}
	if model.BinanceUID() != "87654321" || model.GetBinanceCredentials().APIKey != "saved-key" {
		t.Fatal("invalid request modified configuration")
	}
	if err := db.Callback().Create().Before("gorm:create").Register("fail-config-write", func(tx *gorm.DB) { tx.AddError(errors.New("simulated write failure")) }); err != nil {
		t.Fatal(err)
	}
	if r := request(http.MethodPost, "/binance", `{"uid":"11111111","api_key":"replacement-key","api_secret":"replacement-secret"}`); r["code"] != float64(400) {
		t.Fatal("write failure was reported as success")
	}
	db.Callback().Create().Remove("fail-config-write")
	model.RefreshC()
	if model.BinanceUID() != "87654321" || model.GetBinanceCredentials().APISecret != "saved-secret" {
		t.Fatal("failed save modified configuration")
	}
	request(http.MethodPost, "/binance", `{"uid":""}`)
	if model.BinanceConfigured() {
		t.Fatal("empty UID did not disable payments")
	}
	request(http.MethodPost, "/binance", `{"uid":"87654321"}`)
	if !model.BinanceConfigured() {
		t.Fatal("could not reenable saved keys")
	}
	request(http.MethodPost, "/binance", `{"clear_credentials":true}`)
	model.RefreshC() // simulate loading persisted config again
	c = model.GetBinanceCredentials()
	if c.Source != "database" || c.UID != "" || c.APIKey != "" || c.APISecret != "" || model.BinanceConfigured() {
		t.Fatal("cleared config fell back to environment")
	}
	if r := request(http.MethodPost, "/binance", `{"uid":"87654321"}`); r["code"] != float64(400) {
		t.Fatal("enabled without keys")
	}
}

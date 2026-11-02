package admin

import (
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/v03413/bepusdt/app/handler/base"
	"github.com/v03413/bepusdt/app/model"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"gorm.io/gorm/logger"
)

func (Conf) BinanceGet(ctx *gin.Context) {
	c := model.GetBinanceCredentials()
	base.Ok(ctx, gin.H{"uid": c.UID, "api_key_configured": c.APIKey != "", "api_secret_configured": c.APISecret != "", "enabled": model.ValidBinanceUID(c.UID) && c.APIKey != "" && c.APISecret != "", "source": c.Source})
}

// Blank secrets preserve the current value; explicit clearing disables the account.
func (Conf) BinanceSet(ctx *gin.Context) {
	var req struct {
		UID       string `json:"uid"`
		APIKey    string `json:"api_key"`
		APISecret string `json:"api_secret"`
		Clear     bool   `json:"clear_credentials"`
	}
	if err := ctx.ShouldBindJSON(&req); err != nil {
		base.BadRequest(ctx, "配置格式错误")
		return
	}
	c := model.GetBinanceCredentials()
	c.UID = strings.TrimSpace(req.UID)
	if key := strings.TrimSpace(req.APIKey); key != "" {
		c.APIKey = key
	}
	if secret := strings.TrimSpace(req.APISecret); secret != "" {
		c.APISecret = secret
	}
	if req.Clear {
		c.UID = ""
		c.APIKey = ""
		c.APISecret = ""
	}
	if c.UID != "" && !model.ValidBinanceUID(c.UID) {
		base.BadRequest(ctx, "请输入有效的币安用户 UID（4–20 位数字）")
		return
	}
	if len(c.APIKey) > 512 || len(c.APISecret) > 512 {
		base.BadRequest(ctx, "API 密钥长度不能超过 512 字符")
		return
	}
	if c.UID != "" && (c.APIKey == "" || c.APISecret == "") {
		base.BadRequest(ctx, "启用 Binance Pay 需要同时配置 UID、API Key 和 API Secret")
		return
	}
	rows := []model.Conf{{K: model.BinanceUIDKey, V: c.UID}, {K: model.BinanceAPIKey, V: c.APIKey}, {K: model.BinanceAPISecret, V: c.APISecret}}
	err := model.Db.Session(&gorm.Session{Logger: logger.Discard}).Transaction(func(tx *gorm.DB) error {
		return tx.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "k"}}, DoUpdates: clause.AssignmentColumns([]string{"v"})}).Create(&rows).Error
	})
	// Do not include database errors that might contain credential values in logs or responses.
	if err != nil {
		base.BadRequest(ctx, "Binance Pay 配置保存失败，请重试")
		return
	}
	model.RefreshC()
	base.Ok(ctx, "配置已保存，无需重启")
}

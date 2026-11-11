package model

import (
	"errors"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// Hosted upstreams are private resources, deliberately separate from Channel.
type HostedAccount struct {
	UserID           int  `gorm:"primaryKey;autoIncrement:false" json:"-"`
	PlatformFallback bool `json:"platform_fallback"`
}

type HostedUpstream struct {
	ID           int    `json:"id"`
	UserID       int    `gorm:"index" json:"-"`
	CredentialID string `gorm:"size:36" json:"-"`
	Name         string `gorm:"size:80" json:"name"`
	BaseURL      string `gorm:"size:512" json:"base_url"`
	EncryptedKey string `gorm:"type:text" json:"-"`
	KeyHint      string `gorm:"size:8" json:"key_hint"`
	Models       string `gorm:"type:text" json:"models"`
	Enabled      bool   `json:"enabled"`
}

type HostedUsage struct {
	UserID int    `gorm:"primaryKey;autoIncrement:false" json:"-"`
	Day    string `gorm:"primaryKey;size:10" json:"day"`
	Used   int64  `json:"used_tokens"`
}

// A reservation initially counts its full bound. A crash or an ambiguous
// upstream response therefore cannot turn requests into unlimited free usage.
type HostedReservation struct {
	ID        string `gorm:"primaryKey;size:36"`
	UserID    int    `gorm:"index"`
	Day       string `gorm:"size:10"`
	Tokens    int64
	Settled   bool
	CreatedAt int64 `gorm:"index"`
}

var ErrHostedLimit = errors.New("Hosted API allowance exceeded")
var ErrHostedBusy = errors.New("Too many concurrent hosted API requests")

func HostedLimits(group string) (int, int64) {
	if group == "vip" {
		return 5, 100_000_000
	}
	return 1, 10_000_000
}

func HostedDay(now time.Time) string {
	return now.In(time.FixedZone("Asia/Shanghai", 8*3600)).Format("2006-01-02")
}

// Taking a write lock before reading also serializes SQLite transactions.
// Every mutation for one account uses this same lock, across worker processes.
func hostedTransaction(userID int, fn func(*gorm.DB, *User) error) error {
	return DB.Transaction(func(tx *gorm.DB) error {
		account := HostedAccount{UserID: userID}
		if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&account).Error; err != nil {
			return err
		}
		if err := tx.Model(&HostedAccount{}).Where("user_id = ?", userID).UpdateColumn("user_id", userID).Error; err != nil {
			return err
		}
		var user User
		if err := lockForUpdate(tx).Select("id", "group", "status").First(&user, userID).Error; err != nil {
			return err
		}
		if user.Status != common.UserStatusEnabled {
			return errors.New("Account is unavailable")
		}
		return fn(tx, &user)
	})
}

func GetHostedState(userID int) (HostedAccount, []HostedUpstream, HostedUsage, string, error) {
	account := HostedAccount{UserID: userID}
	upstreams := []HostedUpstream{}
	usage := HostedUsage{UserID: userID, Day: HostedDay(time.Now())}
	var group string
	err := hostedTransaction(userID, func(tx *gorm.DB, u *User) error {
		group = u.Group
		if err := tx.First(&account, "user_id = ?", userID).Error; err != nil {
			return err
		}
		if err := tx.Where("user_id = ?", userID).Order("id ASC").Find(&upstreams).Error; err != nil {
			return err
		}
		err := tx.First(&usage, "user_id = ? AND day = ?", userID, usage.Day).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil
		}
		return err
	})
	return account, upstreams, usage, group, err
}

func SaveHostedUpstream(userID int, value *HostedUpstream) error {
	return hostedTransaction(userID, func(tx *gorm.DB, u *User) error {
		limit, _ := HostedLimits(u.Group)
		if value.ID == 0 {
			var count int64
			if err := tx.Model(&HostedUpstream{}).Where("user_id = ?", userID).Count(&count).Error; err != nil {
				return err
			}
			if count >= int64(limit) {
				return ErrHostedLimit
			}
			value.UserID = userID
			return tx.Create(value).Error
		}
		var old HostedUpstream
		if err := tx.First(&old, "id = ? AND user_id = ?", value.ID, userID).Error; err != nil {
			return err
		}
		if value.EncryptedKey == "" {
			if value.BaseURL != old.BaseURL {
				return errors.New("Enter the upstream API key again when changing its Base URL")
			}
			value.EncryptedKey, value.KeyHint, value.CredentialID = old.EncryptedKey, old.KeyHint, old.CredentialID
		}
		value.UserID = userID
		return tx.Model(&old).Select("name", "base_url", "encrypted_key", "key_hint", "models", "enabled", "credential_id").Updates(value).Error
	})
}

func DeleteHostedUpstream(userID, id int) error {
	return hostedTransaction(userID, func(tx *gorm.DB, _ *User) error {
		result := tx.Where("id = ? AND user_id = ?", id, userID).Delete(&HostedUpstream{})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return gorm.ErrRecordNotFound
		}
		return nil
	})
}

func SetHostedFallback(userID int, enabled bool) error {
	return hostedTransaction(userID, func(tx *gorm.DB, _ *User) error {
		return tx.Model(&HostedAccount{}).Where("user_id = ?", userID).Update("platform_fallback", enabled).Error
	})
}

func ReserveHostedTokens(userID int, bound int64, now time.Time) (*HostedReservation, error) {
	if bound <= 0 || bound > 10_000_000 {
		return nil, ErrHostedLimit
	}
	reservation := &HostedReservation{ID: uuid.NewString(), UserID: userID, Day: HostedDay(now), Tokens: bound, CreatedAt: now.Unix()}
	err := hostedTransaction(userID, func(tx *gorm.DB, u *User) error {
		_, limit := HostedLimits(u.Group)
		usage := HostedUsage{UserID: userID, Day: reservation.Day}
		if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&usage).Error; err != nil {
			return err
		}
		if err := tx.First(&usage, "user_id = ? AND day = ?", userID, reservation.Day).Error; err != nil {
			return err
		}
		if usage.Used < 0 || usage.Used > limit-bound {
			return ErrHostedLimit
		}
		var active int64
		if err := tx.Model(&HostedReservation{}).Where("user_id = ? AND settled = ? AND created_at >= ?", userID, false, now.Add(-181*time.Second).Unix()).Count(&active).Error; err != nil {
			return err
		}
		if active >= 16 {
			return ErrHostedBusy
		}
		if err := tx.Model(&usage).Update("used", usage.Used+bound).Error; err != nil {
			return err
		}
		return tx.Create(reservation).Error
	})
	return reservation, err
}

// Passing the original bound conservatively settles missing usage/cancellation.
// Zero is reserved for an explicit upstream rejection before any completion.
func SettleHostedTokens(userID int, reservationID string, actual int64) error {
	return hostedTransaction(userID, func(tx *gorm.DB, _ *User) error {
		var reservation HostedReservation
		if err := tx.First(&reservation, "id = ? AND user_id = ?", reservationID, userID).Error; err != nil {
			return err
		}
		if actual < 0 || actual > reservation.Tokens {
			return errors.New("Invalid hosted token usage")
		}
		if reservation.Settled {
			return nil
		}
		var usage HostedUsage
		if err := tx.First(&usage, "user_id = ? AND day = ?", userID, reservation.Day).Error; err != nil {
			return err
		}
		refund := reservation.Tokens - actual
		if usage.Used < refund {
			return errors.New("Invalid hosted usage ledger")
		}
		if err := tx.Model(&usage).Update("used", usage.Used-refund).Error; err != nil {
			return err
		}
		return tx.Model(&reservation).Update("settled", true).Error
	})
}

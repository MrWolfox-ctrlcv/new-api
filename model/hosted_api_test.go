package model

import (
	"fmt"
	"net"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/glebarez/sqlite"
	sqlmysql "github.com/go-sql-driver/mysql"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/mysql"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
	"gorm.io/gorm/schema"
)

// BYOK_TEST_MYSQL_DSN and BYOK_TEST_POSTGRES_DSN must point to disposable
// databases. They are opt-in; normal unit test runs always exercise SQLite.
func TestHostedDatabaseMatrix(t *testing.T) {
	for _, engine := range []string{"sqlite", "mysql", "postgres"} {
		t.Run(engine, func(t *testing.T) {
			var dialector gorm.Dialector
			switch engine {
			case "sqlite":
				dialector = sqlite.Open("file:" + t.TempDir() + "/hosted.db?_pragma=busy_timeout(10000)")
			case "mysql":
				dsn := os.Getenv("BYOK_TEST_MYSQL_DSN")
				if dsn == "" {
					t.Skip("Set BYOK_TEST_MYSQL_DSN to run real MySQL verification")
				}
				config, err := sqlmysql.ParseDSN(dsn)
				require.NoError(t, err)
				host, _, err := net.SplitHostPort(config.Addr)
				require.NoError(t, err)
				require.True(t, net.ParseIP(host).IsLoopback(), "Only disposable loopback databases are allowed")
				dialector = mysql.Open(dsn)
			case "postgres":
				dsn := os.Getenv("BYOK_TEST_POSTGRES_DSN")
				if dsn == "" {
					t.Skip("Set BYOK_TEST_POSTGRES_DSN to run real PostgreSQL verification")
				}
				config, err := pgconn.ParseConfig(dsn)
				require.NoError(t, err)
				require.True(t, net.ParseIP(config.Host).IsLoopback(), "Only disposable loopback databases are allowed")
				dialector = postgres.Open(dsn)
			}
			original := DB
			oldType := common.MainDatabaseType()
			prefix := fmt.Sprintf("hv_%d_", time.Now().UnixNano())
			db, err := gorm.Open(dialector, &gorm.Config{Logger: logger.Default.LogMode(logger.Silent), NamingStrategy: schema.NamingStrategy{TablePrefix: prefix}})
			require.NoError(t, err)
			DB = db
			common.SetDatabaseTypes(common.DatabaseType(engine), common.DatabaseTypeSQLite)
			initCol()
			sqlDB, err := DB.DB()
			require.NoError(t, err)
			sqlDB.SetMaxOpenConns(8)
			t.Cleanup(func() {
				DB = original
				common.SetDatabaseTypes(oldType, common.DatabaseTypeSQLite)
				initCol()
				_ = sqlDB.Close()
			})
			models := []any{&HostedReservation{}, &HostedUsage{}, &HostedUpstream{}, &HostedAccount{}, &Token{}, &User{}}
			for _, value := range models {
				require.NoError(t, DB.Migrator().DropTable(value))
			}
			// Representative rc48 state before introducing hosted schemas.
			require.NoError(t, DB.AutoMigrate(&User{}, &Token{}))
			user := createReserveTestUser(t, 123456)
			legacy := createReserveTestToken(t, 4567)
			legacy.UserId = user.Id
			require.NoError(t, DB.Save(&legacy).Error)
			var beforeUser User
			var beforeToken Token
			require.NoError(t, DB.First(&beforeUser, user.Id).Error)
			require.NoError(t, DB.First(&beforeToken, legacy.Id).Error)
			for range 2 {
				require.NoError(t, DB.AutoMigrate(&HostedAccount{}, &HostedUpstream{}, &HostedUsage{}, &HostedReservation{}))
			}
			var afterUser User
			var afterToken Token
			require.NoError(t, DB.First(&afterUser, user.Id).Error)
			require.NoError(t, DB.First(&afterToken, legacy.Id).Error)
			require.Equal(t, beforeUser, afterUser)
			require.Equal(t, beforeToken, afterToken)
			duplicate := legacy
			duplicate.Id = 0
			require.Error(t, DB.Create(&duplicate).Error, "Existing token uniqueness must survive migration")
			var successes atomic.Int32
			errorsCh := make(chan error, 12)
			var wg sync.WaitGroup
			for i := range 12 {
				wg.Go(func() {
					err := SaveHostedUpstream(user.Id, &HostedUpstream{Name: fmt.Sprintf("own-%d", i), Models: `["chat-a","chat-b"]`, Enabled: true})
					if err == nil {
						successes.Add(1)
					} else {
						errorsCh <- err
					}
				})
			}
			wg.Wait()
			close(errorsCh)
			for err := range errorsCh {
				require.ErrorIs(t, err, ErrHostedLimit)
			}
			require.EqualValues(t, 1, successes.Load())
			account, upstreams, usage, group, err := GetHostedState(user.Id)
			require.NoError(t, err)
			require.False(t, account.PlatformFallback)
			require.Len(t, upstreams, 1)
			require.Zero(t, usage.Used)
			require.Equal(t, "default", group)
			// Cross-account IDs never authorize mutation or settlement.
			other := createReserveTestUser(t, 5)
			copy := upstreams[0]
			copy.Name = "intrusion"
			require.ErrorIs(t, SaveHostedUpstream(other.Id, &copy), gorm.ErrRecordNotFound)
			require.ErrorIs(t, DeleteHostedUpstream(other.Id, copy.ID), gorm.ErrRecordNotFound)
			now := time.Date(2026, 11, 11, 15, 59, 59, 0, time.UTC)
			successes.Store(0)
			var reservations sync.Map
			errorsCh = make(chan error, 12)
			for range 12 {
				wg.Go(func() {
					reservation, err := ReserveHostedTokens(user.Id, 1_000_000, now)
					if err == nil {
						successes.Add(1)
						reservations.Store(reservation.ID, reservation)
					} else {
						errorsCh <- err
					}
				})
			}
			wg.Wait()
			close(errorsCh)
			for err := range errorsCh {
				require.ErrorIs(t, err, ErrHostedLimit)
			}
			require.EqualValues(t, 10, successes.Load())
			var firstID string
			reservations.Range(func(key, value any) bool { firstID = key.(string); return false })
			require.Error(t, SettleHostedTokens(user.Id, firstID, -1))
			require.Error(t, SettleHostedTokens(user.Id, firstID, 1_000_001))
			require.ErrorIs(t, SettleHostedTokens(other.Id, firstID, 0), gorm.ErrRecordNotFound)
			require.NoError(t, SettleHostedTokens(user.Id, firstID, 600))
			require.NoError(t, SettleHostedTokens(user.Id, firstID, 0), "Duplicate settlement never refunds twice")
			var day HostedUsage
			require.NoError(t, DB.First(&day, "user_id = ? AND day = ?", user.Id, HostedDay(now)).Error)
			require.EqualValues(t, 9_000_600, day.Used)
			reservations.Range(func(key, value any) bool {
				reservation := value.(*HostedReservation)
				require.NoError(t, SettleHostedTokens(user.Id, reservation.ID, reservation.Tokens))
				return true
			})
			midnight, err := ReserveHostedTokens(user.Id, 1_000_000, now.Add(time.Second))
			require.NoError(t, err, "Beijing midnight has a new account-wide ledger")
			require.NoError(t, SettleHostedTokens(user.Id, midnight.ID, midnight.Tokens))
			require.NoError(t, DB.Model(&user).Update("group", "vip").Error)
			for i := range 4 {
				require.NoError(t, SaveHostedUpstream(user.Id, &HostedUpstream{Name: fmt.Sprintf("vip-%d", i)}))
			}
			require.ErrorIs(t, SaveHostedUpstream(user.Id, &HostedUpstream{Name: "sixth"}), ErrHostedLimit)
			for range 9 {
				_, err = ReserveHostedTokens(user.Id, 10_000_000, now.Add(time.Second))
				require.NoError(t, err)
			}
			_, err = ReserveHostedTokens(user.Id, 10_000_000, now.Add(time.Second))
			require.ErrorIs(t, err, ErrHostedLimit)
			require.NoError(t, SetHostedFallback(user.Id, true))
			account, _, _, _, err = GetHostedState(user.Id)
			require.NoError(t, err)
			require.True(t, account.PlatformFallback)
			require.NoError(t, DB.Model(&user).Update("group", "default").Error)
			require.ErrorIs(t, SaveHostedUpstream(user.Id, &HostedUpstream{Name: "downgrade"}), ErrHostedLimit)
			var activeReservation *HostedReservation
			for range 16 {
				activeReservation, err = ReserveHostedTokens(other.Id, 10, now.Add(48*time.Hour))
				require.NoError(t, err)
			}
			_, err = ReserveHostedTokens(other.Id, 10, now.Add(48*time.Hour))
			require.ErrorIs(t, err, ErrHostedBusy)
			require.NoError(t, SettleHostedTokens(other.Id, activeReservation.ID, 5))
			_, err = ReserveHostedTokens(other.Id, 10, now.Add(48*time.Hour))
			require.NoError(t, err)
			// Restart migration is idempotent and keeps hosted data and compound keys.
			require.NoError(t, DB.AutoMigrate(&HostedAccount{}, &HostedUpstream{}, &HostedUsage{}, &HostedReservation{}))
			_, connections, _, _, err := GetHostedState(user.Id)
			require.NoError(t, err)
			require.Len(t, connections, 5)
			require.NoError(t, DB.First(&afterUser, user.Id).Error)
			require.Equal(t, beforeUser.Quota, afterUser.Quota)
			require.NoError(t, DB.First(&afterToken, legacy.Id).Error)
			require.Equal(t, beforeToken, afterToken, "Hosted calls never touch platform key quota")
			for _, value := range models {
				require.NoError(t, DB.Migrator().DropTable(value))
			}
			// Fresh-install hosted migration, repeated once more.
			for range 2 {
				require.NoError(t, DB.AutoMigrate(&HostedAccount{}, &HostedUpstream{}, &HostedUsage{}, &HostedReservation{}))
			}
		})
	}
}

func TestHostedLimitsIgnoreClientPricingGroups(t *testing.T) {
	for _, group := range []string{"default", "owner", "auto", "VIP", "vip,default"} {
		connections, tokens := HostedLimits(group)
		require.Equal(t, 1, connections)
		require.EqualValues(t, 10_000_000, tokens)
	}
	connections, tokens := HostedLimits("vip")
	require.Equal(t, 5, connections)
	require.EqualValues(t, 100_000_000, tokens)
}

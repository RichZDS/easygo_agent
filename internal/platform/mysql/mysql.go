package mysql

import (
	"context"
	"fmt"
	"net"
	"strconv"
	"time"

	"easygo-agent/internal/config"
	"easygo-agent/internal/platform/logger"

	mysqldriver "github.com/go-sql-driver/mysql"
	"go.uber.org/zap"
	gormmysql "gorm.io/driver/mysql"
	"gorm.io/gorm"
)

func Open(ctx context.Context, cfg config.MySQL) (*gorm.DB, error) {
	dsnConfig := mysqldriver.NewConfig()
	dsnConfig.User = cfg.User
	dsnConfig.Passwd = cfg.Password
	dsnConfig.Net = "tcp"
	dsnConfig.Addr = net.JoinHostPort(cfg.Host, strconv.Itoa(cfg.Port))
	dsnConfig.DBName = cfg.Database
	dsnConfig.Params = map[string]string{"charset": cfg.Charset}
	dsnConfig.ParseTime = true
	dsnConfig.Loc = time.Local
	dsnConfig.Timeout = 5 * time.Second
	dsnConfig.ReadTimeout = 5 * time.Second
	dsnConfig.WriteTimeout = 5 * time.Second
	dsn := dsnConfig.FormatDSN()

	db, err := gorm.Open(gormmysql.Open(dsn), &gorm.Config{
		Logger:         newGORMLogger(logger.L()),
		TranslateError: true,
	})
	if err != nil {
		return nil, fmt.Errorf("open mysql: %w", err)
	}

	sqlDB, err := db.DB()
	if err != nil {
		return nil, fmt.Errorf("get mysql connection pool: %w", err)
	}
	sqlDB.SetMaxOpenConns(cfg.MaxOpenConns)
	sqlDB.SetMaxIdleConns(cfg.MaxIdleConns)
	sqlDB.SetConnMaxLifetime(time.Duration(cfg.ConnMaxLifetime) * time.Second)

	pingCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := sqlDB.PingContext(pingCtx); err != nil {
		_ = sqlDB.Close()
		return nil, fmt.Errorf("ping mysql: %w", err)
	}

	logger.Info("mysql connected",
		zap.Int("max_open_connections", cfg.MaxOpenConns),
		zap.Int("max_idle_connections", cfg.MaxIdleConns),
	)
	return db, nil
}

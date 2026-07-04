package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/jieguo-coder/mini-gateway/internal/config"
)

func main() {
	// 解析命令行参数
	configPath := flag.String("config", "config.yaml", "path to gateway configuration file")
	flag.Parse()

	// 初始化默认结构化日志（配置加载前使用 Info 级别）
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	}))
	slog.SetDefault(logger)

	// 加载配置
	cfg, err := config.LoadConfig(*configPath)
	if err != nil {
		slog.Error("failed to load configuration", "error", err, "path", *configPath)
		os.Exit(1)
	}

	// 根据配置文件动态更新日志级别
	updateLogLevel(cfg.Logging.Level)

	slog.Info("configuration loaded successfully",
		"host", cfg.Server.Host,
		"port", cfg.Server.Port,
		"routes_count", len(cfg.Routes),
		"log_level", cfg.Logging.Level,
	)

	// 构建 HTTP Server
	addr := fmt.Sprintf("%s:%d", cfg.Server.Host, cfg.Server.Port)

	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		fmt.Fprintln(w, "Gateway is running")
	})

	srv := &http.Server{
		Addr:         addr,
		Handler:      mux,
		ReadTimeout:  cfg.Server.ReadTimeout,
		WriteTimeout: cfg.Server.WriteTimeout,
		IdleTimeout:  cfg.Server.IdleTimeout,
	}

	// errChan 用于接收 ListenAndServe 的致命错误（端口占用等）
	errChan := make(chan error, 1)
	go func() {
		slog.Info("gateway server starting", "addr", addr)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			errChan <- fmt.Errorf("server listen failed: %w", err)
		}
	}()

	// 等待中断信号或服务崩溃
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)

	select {
	case sig := <-quit:
		slog.Info("received shutdown signal, starting graceful shutdown...", "signal", sig.String())
	case err := <-errChan:
		slog.Error("server encountered fatal error, shutting down...", "error", err)
	}

	// 优雅关停：在配置的超时时间内等待活跃请求完成
	ctx, cancel := context.WithTimeout(context.Background(), cfg.Server.ShutdownTimeout)
	defer cancel()

	if err := srv.Shutdown(ctx); err != nil {
		slog.Error("server forced to shutdown", "error", err)
		os.Exit(1)
	}

	slog.Info("server exited gracefully")
}

// updateLogLevel 根据配置字符串动态设置 slog 的日志级别。
func updateLogLevel(level string) {
	var lvl slog.Level
	switch strings.ToLower(level) {
	case "debug":
		lvl = slog.LevelDebug
	case "info":
		lvl = slog.LevelInfo
	case "warn":
		lvl = slog.LevelWarn
	case "error":
		lvl = slog.LevelError
	default:
		slog.Warn("unknown log level, falling back to info", "level", level)
		lvl = slog.LevelInfo
	}

	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: lvl,
	}))
	slog.SetDefault(logger)
}
